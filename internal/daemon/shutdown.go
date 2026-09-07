package daemon

import (
	"context"
	"log/slog"
)

// ShutdownStatus is absent until shutdown has been requested.
type ShutdownStatus struct {
	Draining      bool `json:"draining"`
	ActiveUploads int  `json:"active_uploads"`
}

// WaitForUploads freezes the session before optional upload draining. The caller
// must keep the service lifetime alive until this returns, then call Close.
// Cancelling ctx skips the wait, not the unconditional cleanup in Close.
func (s *Service) WaitForUploads(ctx context.Context) {
	// Unblock a scan/config mutation before taking the lifecycle lock.
	s.scanCancel()
	s.lifecycleMu.Lock()
	s.uploadMu.Lock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.uploadMu.Unlock()
		s.lifecycleMu.Unlock()
		return
	}
	first := !s.shuttingDown
	s.shuttingDown = true
	if first && s.cfg.Uploads.WaitForActiveUploadsOnQuit {
		s.shutdownClient = s.client
	}
	client := s.shutdownClient
	s.mu.Unlock()
	var done <-chan struct{}
	if client != nil {
		done = client.DrainUploads()
	}
	s.uploadMu.Unlock()
	s.lifecycleMu.Unlock()
	if client == nil {
		return
	}
	_, active := client.UploadDrainStatus()
	s.event(slog.LevelInfo, "upload_drain_started", nil, slog.Int("active_uploads", active))
	select {
	case <-done:
		s.event(slog.LevelInfo, "upload_drain_completed", nil)
	case <-ctx.Done():
		s.event(slog.LevelInfo, "upload_drain_forced", ctx.Err())
	}
}
