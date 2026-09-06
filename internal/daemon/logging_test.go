package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/diagnostics"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/stats"
)

func TestLoggingSavePathsAndGlobalStatus(t *testing.T) {
	cfg := testConfig(t)
	cfg.Soulseek.ConnectOnStartup = false
	cfg.Logging.Level = "info"
	s, err := New(cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	s.SetConfigPath(path)
	m := diagnostics.New(t.TempDir(), slog.LevelError, nil)
	if err := s.SetDiagnostics(m); err != nil {
		t.Fatal(err)
	}
	if m.Status().Level != "INFO" {
		t.Fatal("startup level not applied")
	}
	client := soulseek.NewClient(soulseek.ClientConfig{Logger: m.Logger()})
	s.client = client
	called := false
	index := s.shares
	s.shareIndexBuilder = func(context.Context, []config.Share) (*soulseek.ShareIndex, error) { called = true; return index, nil }
	next := cfg
	next.Logging.Level = "dEbUg"
	if err := s.UpdateConfig(next); err != nil {
		t.Fatal(err)
	}
	if called || s.client != client || m.Status().Level != "DEBUG" {
		t.Fatal("level change rebuilt/reconnected or failed")
	}
	saved, err := config.Load(path)
	if err != nil || saved.Logging.Level != "DEBUG" {
		t.Fatalf("save: %v", err)
	}
	next = s.cfg
	next.Logging.Level = "WARN"
	next.AudioMetadata = !next.AudioMetadata
	if err := s.UpdateConfig(next); err != nil {
		t.Fatal(err)
	}
	if !called || m.Status().Level != "WARN" {
		t.Fatal("mixed update missed level")
	}
	s.SetConfigPath(t.TempDir())
	next.Logging.Level = "ERROR"
	if err := s.UpdateConfig(next); err == nil {
		t.Fatal("expected save failure")
	}
	if m.Status().Level != "WARN" || s.cfg.Logging.Level != "WARN" {
		t.Fatal("failed save changed effective level")
	}
	snapshot := s.Snapshot()
	if snapshot.Logging == nil || snapshot.Logging.Level != "WARN" {
		t.Fatal("missing status")
	}
	global, err := s.Statistics(stats.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	s.telemetry.warning = "history warning"
	filtered, err := s.Statistics(stats.Filter{Account: "unrelated"})
	if err != nil {
		t.Fatal(err)
	}
	if *global.Logging != *filtered.Logging || filtered.Warning != "history warning" {
		t.Fatal("filter changed logs or lost history warning")
	}
	encoded, err := json.Marshal(filtered)
	if err != nil {
		t.Fatal(err)
	}
	var decoded StatsOverview
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.Logging == nil {
		t.Fatal("logging IPC round trip")
	}
	if err := json.Unmarshal([]byte(`{"online_seconds":0}`), &StatsOverview{}); err != nil {
		t.Fatal("old IPC rejected")
	}
}

type blockedDiagnosticOutput chan struct{}

func (w blockedDiagnosticOutput) Write(p []byte) (int, error) { <-w; return len(p), nil }
func TestBlockedLoggerRetainsStateOwnership(t *testing.T) {
	cfg := testConfig(t)
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	first, err := New(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	output := make(blockedDiagnosticOutput)
	manager := diagnostics.New(t.TempDir(), slog.LevelInfo, output)
	defer func() {
		select {
		case <-output:
		default:
			close(output)
		}
		manager.Close()
		first.stateDB.Close()
	}()
	if err := first.SetDiagnostics(manager); err != nil {
		t.Fatal(err)
	}
	manager.Logger().Info("blocked")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if second, err := New(cfg, path); err == nil {
		second.Close()
		t.Fatal("state lock released while logger still writes")
	}
	close(output)
	<-manager.Done()
	if err := first.stateDB.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := New(cfg, path)
	if err != nil {
		t.Fatal("state not released after logging stopped", err)
	}
	second.Close()
}
