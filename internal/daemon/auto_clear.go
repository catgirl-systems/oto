package daemon

// clearUploadLocked commits the terminal row before deleting its history.
// Never remove a data file or the owner watermark guarding late callbacks.
func (s *Service) clearUploadLocked(id string, expected uploadOwner) error {
	tr, ok := s.transfers[id]
	if !ok {
		delete(s.uploadCancelEligible, id)
		return nil
	}
	if s.uploadOwners[id] != expected {
		return nil
	}
	if err := s.persistUploadLocked(id); err != nil {
		return err
	}
	delete(s.transfers, id)
	if err := s.persistUploadLocked(id); err != nil {
		s.transfers[id] = tr
		return err
	}
	delete(s.uploadCancelEligible, id)
	s.forgetTransferLocked(id)
	return nil
}

// A completion worker must not use TransferAction: that action joins workers.
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
