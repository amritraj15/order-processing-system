package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"order_management/api/rest/middleware"
	"order_management/internal/logging"
)

// serve owns separate lifetimes for requests, worker, and termination signals.
// Accepted HTTP requests survive SIGTERM; only the shutdown deadline forces them
// to cancel. The worker may roll back independently without canceling requests.
func serve(ctx context.Context, server *http.Server, limits *middleware.RequestLimits,
	runWorker func(context.Context), drainDelay, shutdownTimeout time.Duration, logger *slog.Logger) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}
	return serveListener(ctx, server, listener, limits, runWorker, drainDelay, shutdownTimeout, logger)
}

func serveListener(ctx context.Context, server *http.Server, listener net.Listener, limits *middleware.RequestLimits, runWorker func(context.Context), drainDelay, shutdownTimeout time.Duration, logger *slog.Logger) error {
	defer listener.Close()
	requests, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	server.BaseContext = func(net.Listener) context.Context { return requests }
	workerResult := make(chan error, 1)
	go func() { workerResult <- guardedWorker(workerCtx, runWorker, logger) }()
	serverResult := make(chan error, 1)
	go func() { serverResult <- server.Serve(listener) }()
	logger.InfoContext(ctx, "server started", "address", listener.Addr().String())
	var err error
	workerStopped := false
	select {
	case <-ctx.Done():
	case err = <-serverResult:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	case err = <-workerResult:
		workerStopped = true
		if err == nil {
			err = errors.New("worker stopped unexpectedly")
		}
	}
	limits.Drain()
	cancelWorker()
	logger.Info("server draining")
	// During deregistration, probes can observe draining and new work gets 503.
	if drainDelay > 0 {
		timer := time.NewTimer(drainDelay)
		<-timer.C
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()
	shutdownErr := server.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		cancelRequests()
		_ = server.Close()
		logger.Error("server shutdown failed", append(logging.ErrorAttrs(shutdownErr), "operation", "http_shutdown")...)
	}
	if !workerStopped {
		select {
		case workerErr := <-workerResult:
			err = errors.Join(err, workerErr)
		case <-shutdownCtx.Done():
			err = errors.Join(err, shutdownCtx.Err())
		}
	}
	return errors.Join(err, shutdownErr)
}

func guardedWorker(ctx context.Context, run func(context.Context), logger *slog.Logger) (err error) {
	defer func() {
		if recover() != nil {
			logger.ErrorContext(ctx, "worker panic", "operation", "processing_worker", "stack", logging.StackFrames())
			err = errors.New("processing worker panicked")
		}
	}()
	run(ctx)
	return nil
}
