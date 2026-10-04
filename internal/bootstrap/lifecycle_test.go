package bootstrap

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"order_management/api/rest"
	"order_management/api/rest/middleware"
	"order_management/api/rest/routes"
)

// A pipe listener exercises net/http shutdown without needing host TCP access.
type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *pipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (l *pipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-ctx.Done():
		client.Close()
		server.Close()
		return nil, ctx.Err()
	case <-l.done:
		client.Close()
		server.Close()
		return nil, net.ErrClosed
	}
}
func awaitDraining(t *testing.T, limits *middleware.RequestLimits) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for !limits.Draining() {
		select {
		case <-deadline.C:
			t.Fatal("did not enter draining")
		case <-time.After(time.Millisecond):
		}
	}
}
func TestShutdownDrainsAcceptedRequests(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	limits := middleware.NewRequestLimits(2, time.Second)
	e := rest.NewServer(logger)
	e.Use(limits.Middleware)
	routes.Mount(e, routes.MountConfig{Draining: limits.Draining})
	entered, release := make(chan struct{}), make(chan struct{})
	e.GET("/hold", func(c *echo.Context) error {
		close(entered)
		select {
		case <-release:
			return c.NoContent(204)
		case <-c.Request().Context().Done():
			return c.Request().Context().Err()
		}
	})
	l := &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
	transport := &http.Transport{DialContext: l.dial}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	workerDone := make(chan struct{})
	go func() {
		finished <- serveListener(ctx, &http.Server{Handler: e}, l, limits, func(ctx context.Context) { <-ctx.Done(); close(workerDone) }, 200*time.Millisecond, time.Second, logger)
	}()
	response := make(chan int, 1)
	go func() {
		r, err := client.Get("http://local/hold")
		if err != nil {
			response <- 0
			return
		}
		defer r.Body.Close()
		response <- r.StatusCode
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	cancel()
	awaitDraining(t, limits)
	for _, path := range []string{"/api/v1/ready", "/hold"} {
		r, err := client.Get("http://local" + path)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 503 {
			t.Fatalf("draining %s: %d", path, r.StatusCode)
		}
	}
	close(release)
	if code := <-response; code != 204 {
		t.Fatalf("accepted request canceled on termination: %d", code)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	select {
	case <-workerDone:
	default:
		t.Fatal("worker not stopped")
	}
}
func TestShutdownDeadlineCancelsStalledRequest(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	limits := middleware.NewRequestLimits(1, time.Second)
	entered, canceled := make(chan struct{}), make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(canceled) })
	l := &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
	transport := &http.Transport{DialContext: l.dial}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- serveListener(ctx, &http.Server{Handler: handler}, l, limits, func(ctx context.Context) { <-ctx.Done() }, 0, 30*time.Millisecond, logger)
	}()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		r, err := client.Get("http://local/hold")
		if err == nil {
			r.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown outcome: %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("stalled request not canceled")
	}
	<-requestDone
}
func TestWorkerPanicInitiatesControlledShutdown(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	limits := middleware.NewRequestLimits(1, time.Second)
	l := &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
	err := serveListener(context.Background(), &http.Server{Handler: http.NewServeMux()}, l, limits, func(context.Context) { panic("private") }, 0, time.Second, logger)
	if err == nil || !limits.Draining() {
		t.Fatal("worker panic did not shut down service")
	}
}
