package daemon

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/catgirl-systems/oto/internal/diagnostics"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

// SetDiagnostics must precede Start and publication of the service to other callers.
func (s *Service) SetDiagnostics(manager *diagnostics.Manager) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runCancel != nil || s.closed || s.diagnostics != nil {
		return errors.New("daemon: diagnostics already initialized or service started")
	}
	s.diagnostics = manager
	s.applyLogLevel(s.cfg.Logging.Level)
	return nil
}

func (s *Service) logger() *slog.Logger {
	if s.diagnostics == nil {
		return nil
	}
	return s.diagnostics.Logger()
}
func (s *Service) event(level slog.Level, name string, err error, attrs ...slog.Attr) {
	diagnostics.Event(s.logger(), level, name, err, attrs...)
}
func (s *Service) applyLogLevel(value string) {
	if s.diagnostics != nil {
		var level slog.Level
		_ = level.UnmarshalText([]byte(value))
		s.diagnostics.SetLevel(level)
	}
}
func (s *Service) loggingStatus() *diagnostics.Status {
	s.mu.RLock()
	manager := s.diagnostics
	s.mu.RUnlock()
	if manager == nil {
		return nil
	}
	status := manager.Status()
	return &status
}
func (s *Service) transferContext(ctx context.Context, id, username string) context.Context {
	logger := s.logger()
	if logger == nil {
		return ctx
	}
	var attempt uint64
	s.mu.RLock()
	if s.telemetry != nil {
		if a := s.telemetry.attempts[id]; a != nil {
			attempt = a.event.Attempt
		}
	}
	s.mu.RUnlock()
	return diagnostics.WithLogger(ctx, logger.With("component", "soulseek", "operation_id", id, "transfer_id", id, "attempt_id", attempt, "peer_username", username))
}

func (s *Service) uploadEvent(name string, level slog.Level, err error, id string, session uint64, event soulseek.TransferEvent) {
	// Legacy IDs may embed filenames; only the journal's numeric IDs are loggable.
	if _, parseErr := strconv.ParseUint(strings.TrimPrefix(id, "upload:"), 10, 64); parseErr != nil || !strings.HasPrefix(id, "upload:") {
		id = ""
	}
	s.event(level, name, err, slog.String("transfer_id", id), slog.Uint64("session_id", session), slog.Uint64("attempt_id", event.Attempt), slog.String("peer_username", event.Username), slog.String("state", event.State))
}
