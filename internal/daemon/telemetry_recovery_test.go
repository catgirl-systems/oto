package daemon

import (
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/stats"
)

func TestCheckpointCrashAndLateCancellationAccounting(t *testing.T) {
	cfg := testConfig(t)
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	s, err := New(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.transfers["d-1"] = Transfer{ID: "d-1", Username: "peer", Direction: "download", Filename: "song", State: "running", Total: 200}
	s.mu.Lock()
	s.statsBeginLocked("d-1", accountKey(cfg))
	s.startTransferLocked("d-1", 100)
	s.mu.Unlock()
	s.updateTransferProgress("d-1", soulseek.Progress{Done: 150})
	if err = s.flushStats(); err != nil {
		t.Fatal(err)
	}
	// Simulate process loss after the checkpoint, without graceful-stop accounting.
	if err = s.stateDB.Close(); err != nil {
		t.Fatal(err)
	}
	s.stateDB = nil
	s.telemetry = nil
	restored, err := New(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = restored.flushStats(); err != nil {
		t.Fatal(err)
	}
	totals, err := restored.telemetry.store.Totals(stats.Filter{})
	if err != nil || totals.Bytes != 50 || totals.AttemptsStarted != 1 || totals.AttemptsInterrupted != 1 {
		t.Fatalf("crash checkpoint: %+v %v", totals, err)
	}
	restored.Close()
	again, err := New(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if err = again.flushStats(); err != nil {
		t.Fatal(err)
	}
	totals, err = again.telemetry.store.Totals(stats.Filter{})
	if err != nil || totals.AttemptsInterrupted != 1 {
		t.Fatalf("interruption replay: %+v %v", totals, err)
	}
	again.transfers["d-2"] = Transfer{ID: "d-2", Username: "peer", Direction: "download", Filename: "other", State: "running", Total: 200}
	again.mu.Lock()
	again.statsBeginLocked("d-2", accountKey(cfg))
	again.startTransferLocked("d-2", 100)
	again.stopTransferLocked("d-2")
	tr := again.transfers["d-2"]
	tr.State = "cancelled"
	again.transfers["d-2"] = tr
	again.mu.Unlock()
	again.updateTransferProgress("d-2", soulseek.Progress{Done: 110})
	again.mu.Lock()
	again.statsStateLocked("d-2", "cancelled")
	again.mu.Unlock()
	if err = again.flushStats(); err != nil {
		t.Fatal(err)
	}
	totals, err = again.telemetry.store.Totals(stats.Filter{})
	if err != nil || totals.Bytes != 60 || totals.AttemptsCancelled != 1 {
		t.Fatalf("last cancellation payload: %+v %v", totals, err)
	}
}
