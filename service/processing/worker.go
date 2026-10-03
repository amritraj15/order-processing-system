package processing

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"log/slog"
	"time"

	"order_management/ports"
)

type Worker struct {
	Status    *Status
	Clock     ports.Clock
	Processor ports.PendingProcessor
	Interval  time.Duration
	BatchSize int
	Logger    *slog.Logger
	Purge     func(context.Context) error
}

// Run is sequential, so a process never starts overlapping drains. PostgreSQL
// row locks coordinate other processes. There is no immediate startup run.
func (w *Worker) Run(ctx context.Context) {
	if w.Status == nil {
		w.Status = NewStatus(w.now(), w.Interval)
	}
	defer w.Status.Stop()
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			started := w.now()
			w.Status.Start(started)
			runCtx, cancel := context.WithTimeout(ctx, w.Interval)
			err := w.Drain(runCtx, started)
			cancel()
			w.Status.Finish(w.now(), err)
			if w.Purge != nil && ctx.Err() == nil {
				cleanupCtx, cleanupCancel := context.WithTimeout(ctx, min(w.Interval, 30*time.Second))
				err := w.Purge(cleanupCtx)
				cleanupCancel()
				if err != nil {
					w.Logger.ErrorContext(ctx, "maintenance failed", "actor_type", "system", "error_kind", "cleanup")
				}
			}
		}
	}
}
func (w *Worker) Drain(ctx context.Context, cutoff time.Time) (err error) {
	started := w.now()
	runID := uuid.NewString()
	processed := 0
	defer func() {
		outcome := "success"
		if err != nil {
			outcome = "failed"
		}
		w.Logger.InfoContext(ctx, "order processing finished", "actor_type", "system", "run_id", runID, "outcome", outcome, "processed", processed, "duration", w.now().Sub(started))
	}()
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("drain pending orders: %w", err)
		}
		count, err := w.Processor.ProcessBatch(ctx, cutoff, w.BatchSize)
		if err != nil {
			return fmt.Errorf("drain pending orders: %w", err)
		}
		processed += count
		// A short batch can reflect locks held by another worker. Stop only on
		// an empty batch; still-locked orders are revisited on the next tick.
		if count == 0 {
			return nil
		}
	}
}

func (w *Worker) now() time.Time {
	if w.Clock != nil {
		return w.Clock.Now()
	}
	return time.Now()
}
