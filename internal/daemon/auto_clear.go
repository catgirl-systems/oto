package daemon

import (
	"errors"
	"github.com/catgirl-systems/oto/internal/config"
)

// A completion worker must not use TransferAction: that action joins workers.
// Save a new journal before changing memory; this never removes a data file.
func (s *Service) clearCompletedDownload(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, download := range s.journal.Downloads {
		if download.ID != id || download.State != "completed" {
			continue
		}
		if s.telemetry != nil && s.statsPendingLocked(id) {
			s.telemetry.clear[id] = true
			return errors.New("statistics not durable; completed history retained")
		}
		next := s.journal
		next.DownloadSequence = s.seq
		next.Downloads = make([]Download, 0, len(s.journal.Downloads)-1)
		next.Downloads = append(next.Downloads, s.journal.Downloads[:i]...)
		next.Downloads = append(next.Downloads, s.journal.Downloads[i+1:]...)
		if err := config.SaveJSON(s.journalPath, next); err != nil {
			return err
		}
		s.journal = next
		delete(s.transfers, id)
		s.forgetTransferLocked(id)
		break
	}
	return nil
}
