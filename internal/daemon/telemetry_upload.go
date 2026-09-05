package daemon

import (
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/stats"
	"time"
)

// Retired callbacks may finish accounting, but cannot revive UI or recovery state.
func (s *Service) statsRetiredUploadLocked(epoch uint64, event soulseek.TransferEvent) {
	if s.telemetry == nil {
		return
	}
	for id, owner := range s.uploadOwners {
		if owner.session != epoch || owner.target.Attempt != event.Attempt || owner.target.Username != event.Username || owner.target.Filename != event.Filename {
			continue
		}
		a := s.telemetry.attempts[id]
		if a == nil || !a.interrupted || !a.streamBegan || event.Done <= a.done {
			return
		}
		e := a.event
		e.Kind = stats.KindProgress
		e.At = time.Now().UTC()
		e.Bytes = event.Done - a.done
		a.done = event.Done
		s.statsEventLocked(e)
		return
	}
}
func (s *Service) uploadRejected(epoch uint64, event soulseek.TransferEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.telemetry == nil || epoch != s.uploadEpoch {
		return
	}
	s.statsEventLocked(stats.Event{Account: s.uploadAccountLocked(epoch), Peer: event.Username, Direction: "upload", Kind: stats.KindRejected, Filename: event.Filename, Error: event.Error})
	if err := s.saveJournalLocked(); err != nil {
		s.telemetry.warning = "Statistics persistence: " + err.Error()
	}
	select {
	case s.telemetry.wake <- struct{}{}:
	default:
	}
}
