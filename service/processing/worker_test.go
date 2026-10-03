package processing

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type fakeProcessor struct {
	batches []int
	calls   int
	cutoff  time.Time
	limit   int
	failAt  int
	cancel  context.CancelFunc
}

func (f *fakeProcessor) ProcessBatch(_ context.Context, cutoff time.Time, limit int) (int, error) {
	f.calls++
	if !f.cutoff.IsZero() && !f.cutoff.Equal(cutoff) {
		return 0, errors.New("cutoff changed")
	}
	f.cutoff = cutoff
	f.limit = limit
	if f.calls == f.failAt {
		return 0, errors.New("database unavailable")
	}
	if f.cancel != nil {
		f.cancel()
	}
	if f.calls > len(f.batches) {
		return 0, nil
	}
	return f.batches[f.calls-1], nil
}
func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func TestDrainProcessesFullAndShortBatches(t *testing.T) {
	f := &fakeProcessor{batches: []int{500, 2, 1, 0}}
	w := &Worker{Processor: f, BatchSize: 500, Logger: quiet()}
	cutoff := time.Now()
	if err := w.Drain(context.Background(), cutoff); err != nil {
		t.Fatal(err)
	}
	if f.calls != 4 || f.limit != 500 || !f.cutoff.Equal(cutoff) {
		t.Fatalf("unexpected drain: %+v", f)
	}
}
func TestDrainStopsOnFailureAndCancellation(t *testing.T) {
	f := &fakeProcessor{failAt: 1}
	w := &Worker{Processor: f, BatchSize: 500, Logger: quiet()}
	if err := w.Drain(context.Background(), time.Now()); err == nil || f.calls != 1 {
		t.Fatal("failure not propagated")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f = &fakeProcessor{batches: []int{1}, cancel: cancel}
	w.Processor = f
	if err := w.Drain(ctx, time.Now()); !errors.Is(err, context.Canceled) || f.calls != 1 {
		t.Fatal("drain continued after cancellation")
	}
}
func TestRunWaitsForFirstTick(t *testing.T) {
	f := &fakeProcessor{}
	w := &Worker{Processor: f, Interval: time.Hour, BatchSize: 500, Logger: quiet()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.Run(ctx)
	if f.calls != 0 {
		t.Fatal("worker ran before first tick")
	}
}

type tickProcessor struct{ attempts chan time.Time }

func (p *tickProcessor) ProcessBatch(ctx context.Context, cutoff time.Time, _ int) (int, error) {
	select {
	case p.attempts <- cutoff:
		return 0, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func TestRunProcessesRecurringTicksAndStops(t *testing.T) {
	processor := &tickProcessor{attempts: make(chan time.Time, 2)}
	worker := &Worker{Processor: processor, Interval: 25 * time.Millisecond, BatchSize: 500, Logger: quiet()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("worker did not stop after cancellation")
		}
	})
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	var previous time.Time
	for i := 0; i < 2; i++ {
		select {
		case cutoff := <-processor.attempts:
			if !cutoff.After(previous) {
				t.Fatal("recurring runs did not advance their cutoff")
			}
			previous = cutoff
		case <-deadline.C:
			t.Fatal("worker did not process two real ticker events")
		}
	}
	cancel()
	select {
	case <-done:
	case <-deadline.C:
		t.Fatal("worker did not stop after processing ticks")
	}
	if snapshot := worker.Status.Snapshot(time.Now()); snapshot.State != "stopped" || snapshot.Running {
		t.Fatalf("unexpected worker state after shutdown: %+v", snapshot)
	}
}
