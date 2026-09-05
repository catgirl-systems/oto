package daemon

import "errors"

var (
	ErrScanCancelled = errors.New("daemon: share scan cancelled by user")
	ErrScanConflict  = errors.New("daemon: share scan is no longer cancellable")
)

// CancelShareScan deliberately does not take lifecycleMu: a settings scan may own it.
func (s *Service) CancelShareScan(id uint64) error {
	if id == 0 {
		return errors.New("daemon: a nonzero share scan ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.shareScan == nil || s.shareScan.ID != id {
		return ErrScanConflict
	}
	if s.shareScan.State == "cancelling" {
		return nil
	}
	if s.shareScan.State != "scanning" || s.activeScanCancel == nil {
		return ErrScanConflict
	}
	s.shareScan.State = "cancelling"
	s.activeScanCancel(ErrScanCancelled)
	s.shareCancelEpoch++
	if s.shareCancelWake != nil {
		close(s.shareCancelWake)
	}
	s.shareCancelWake = make(chan struct{})
	return nil
}

func (s *Service) scanCancellationWake() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shareCancelWake == nil {
		s.shareCancelWake = make(chan struct{})
	}
	return s.shareCancelWake
}

func (s *Service) userCancelledScan() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.shareScan != nil && (s.shareScan.State == "cancelling" || s.shareScan.State == "cancelled" && s.shareScan.Error == "")
}
