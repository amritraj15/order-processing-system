package processing

import (
	"sync"
	"time"
)

type Snapshot struct {
	State                                 string
	LastAttempt, LastSuccess, LastFailure time.Time
	Running                               bool
}
type Status struct {
	mu              sync.RWMutex
	started         time.Time
	interval        time.Duration
	snapshot        Snapshot
	failed, stopped bool
}

func NewStatus(now time.Time, interval time.Duration) *Status {
	return &Status{started: now, interval: interval}
}
func (s *Status) Start(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot.LastAttempt = now
	s.snapshot.Running = true
}
func (s *Status) Finish(now time.Time, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot.Running = false
	s.failed = err != nil
	if err != nil {
		s.snapshot.LastFailure = now
	} else {
		s.snapshot.LastSuccess = now
	}
}
func (s *Status) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	s.snapshot.Running = false
}
func (s *Status) Snapshot(now time.Time) Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := s.snapshot
	v.State = "ok"
	switch {
	case s.stopped:
		v.State = "stopped"
	case s.failed:
		v.State = "failed"
	case v.Running && now.Sub(v.LastAttempt) >= s.interval:
		v.State = "stale"
	case v.LastSuccess.IsZero():
		if now.Sub(s.started) >= 2*s.interval {
			v.State = "stale"
		} else {
			v.State = "starting"
		}
	case now.Sub(v.LastSuccess) >= 2*s.interval:
		v.State = "stale"
	}
	return v
}
