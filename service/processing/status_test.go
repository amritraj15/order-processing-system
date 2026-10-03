package processing

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestStatusBoundariesFailureRecovery(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	i := time.Minute
	s := NewStatus(now, i)
	check := func(at time.Time, want string) {
		t.Helper()
		if got := s.Snapshot(at).State; got != want {
			t.Fatalf("got %s want %s at %v", got, want, at)
		}
	}
	check(now, "starting")
	check(now.Add(2*i), "stale")
	s.Start(now.Add(i))
	s.Finish(now.Add(i), errors.New("DB failed"))
	check(now.Add(i), "failed")
	s.Start(now.Add(2 * i))
	check(now.Add(2*i), "failed")
	s.Finish(now.Add(2*i), nil)
	check(now.Add(2*i), "ok")
	check(now.Add(4*i), "stale")
	s.Start(now.Add(3 * i))
	check(now.Add(4*i), "stale")
	s.Finish(now.Add(4*i), nil)
	check(now.Add(4*i), "ok")
	s.Stop()
	check(now, "stopped")
}
func TestConcurrentSnapshots(t *testing.T) {
	now := time.Now()
	s := NewStatus(now, time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				s.Start(now)
				s.Snapshot(now)
				s.Finish(now, nil)
			}
		}()
	}
	wg.Wait()
}
