package daemon

import (
	"math"
	"sort"
	"time"
)

// Timing belongs to the daemon run, not the journal or an attached frontend.
type transferTiming struct {
	elapsed                         time.Duration
	startedAt, sampleAt, lastByteAt time.Time
	sampleDone, done, speed         uint64
	known, streamBegan              bool
}

func (t *transferTiming) start(now time.Time, done uint64) {
	if !t.startedAt.IsZero() {
		return
	}
	t.known, t.streamBegan = true, true
	t.startedAt, t.sampleAt, t.lastByteAt = now, now, now
	t.sampleDone, t.done, t.speed = done, done, 0
}

func (t *transferTiming) progress(now time.Time, done uint64) {
	if t.startedAt.IsZero() || done <= t.done {
		return
	}
	t.done, t.lastByteAt = done, now
	if interval := now.Sub(t.sampleAt); interval >= time.Second {
		rate := float64(done-t.sampleDone) / interval.Seconds()
		if rate >= float64(uint64(math.MaxUint64)) {
			t.speed = math.MaxUint64
		} else {
			t.speed = uint64(rate)
		}
		t.sampleAt, t.sampleDone = now, done
	}
}

func (t *transferTiming) stop(now time.Time) {
	if !t.startedAt.IsZero() {
		t.elapsed += max(0, now.Sub(t.startedAt))
		t.startedAt = time.Time{}
	}
	t.speed = 0
}

func (t transferTiming) snapshot(x Transfer, now time.Time) Transfer {
	x.ElapsedMS, x.ETASeconds, x.SpeedBPS = nil, nil, 0
	if t.known {
		elapsed := t.elapsed
		if !t.startedAt.IsZero() {
			elapsed += max(0, now.Sub(t.startedAt))
		}
		ms := uint64(max(0, elapsed.Milliseconds()))
		x.ElapsedMS = &ms
	}
	if x.State == "completed" {
		zero := uint64(0)
		x.ETASeconds = &zero
	} else if x.State == "running" && !t.startedAt.IsZero() && now.Sub(t.lastByteAt) < 3*time.Second {
		x.SpeedBPS = t.speed
		if t.speed > 0 {
			eta := uint64(0)
			if x.Total > x.Done {
				eta = (x.Total-x.Done-1)/t.speed + 1
			}
			x.ETASeconds = &eta
		}
	}
	return x
}

// Forget accounting only after history removal commits; upload callback guards stay separate.
func (s *Service) forgetTransferLocked(id string) {
	delete(s.transferTiming, id)
	if s.telemetry != nil {
		delete(s.telemetry.attempts, id)
		delete(s.telemetry.queued, id)
	}
}
func (s *Service) prepareTransferLocked(id string) {
	t := s.transferTiming[id]
	t.stop(time.Now())
	t.streamBegan = false
	if s.transferTiming == nil {
		s.transferTiming = make(map[string]transferTiming)
	}
	s.transferTiming[id] = t
}

func (s *Service) startTransferLocked(id string, done uint64) {
	x := s.transfers[id]
	if s.closed || x.ID == "" || (x.State != "running" && x.State != "queued") {
		return
	}
	x.State, x.Done, x.Queue = "running", done, 0
	s.transfers[id] = x
	if s.transferTiming == nil {
		s.transferTiming = make(map[string]transferTiming)
	}
	t := s.transferTiming[id]
	t.start(time.Now(), done)
	s.statsStreamLocked(id, done)
	s.transferTiming[id] = t
}

func (s *Service) startTransfer(id string, done uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startTransferLocked(id, done)
}

func (s *Service) progressTransferLocked(id string, done uint64) {
	s.statsProgressLocked(id, done)
	t, ok := s.transferTiming[id]
	if ok {
		t.progress(time.Now(), done)
		s.transferTiming[id] = t
	}
}

func (s *Service) stopTransferLocked(id string) {
	s.statsStopLocked(id)
	if t, ok := s.transferTiming[id]; ok {
		t.stop(time.Now())
		s.transferTiming[id] = t
	}
}

func (s *Service) transferValuesLocked(now time.Time) []Transfer {
	out := make([]Transfer, 0, len(s.transfers))
	for _, x := range s.transfers {
		out = append(out, s.transferTiming[x.ID].snapshot(x, now))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
