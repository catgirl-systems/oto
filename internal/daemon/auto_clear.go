package daemon

// A completion worker must not use TransferAction: that action joins workers.
// Delete history and commit its accounting together; never remove a data file.
func (s *Service) clearCompletedDownload(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, download := range s.journal.Downloads {
		if download.ID != id || download.State != "completed" {
			continue
		}
		if err := s.deleteDownloadLocked(id); err != nil {
			return err
		}
		s.journal.Downloads = append(s.journal.Downloads[:i], s.journal.Downloads[i+1:]...)
		delete(s.transfers, id)
		s.forgetTransferLocked(id)
		break
	}
	return nil
}
