package daemon

import (
	"path/filepath"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/catgirl-systems/oto/internal/stats"
)

func TestAccountingOffsetsRetriesReplayAndAccounts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := testConfig(t)
		s, err := New(cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		s.transfers["d-1"] = Transfer{ID: "d-1", Username: "peer", Direction: "download", Filename: "song.flac", State: "running", Total: 200}
		first := accountKey(cfg)
		s.mu.Lock()
		s.statsBeginLocked("d-1", first)
		s.startTransferLocked("d-1", 100)
		s.progressTransferLocked("d-1", 150)
		s.mu.Unlock()
		time.Sleep(2 * time.Second)
		s.mu.Lock()
		s.statsStateLocked("d-1", "failed")
		s.stopTransferLocked("d-1")
		s.prepareTransferLocked("d-1")
		s.statsBeginLocked("d-1", "server/other")
		s.startTransferLocked("d-1", 130)
		s.progressTransferLocked("d-1", 200)
		s.cfg.Soulseek.Username = "edited later"
		s.mu.Unlock()
		time.Sleep(time.Second)
		s.mu.Lock()
		s.statsStateLocked("d-1", "completed")
		pending := slices.Clone(s.telemetry.pending)
		s.mu.Unlock()
		if err = s.flushStats(); err != nil {
			t.Fatal(err)
		}
		a, err := s.telemetry.store.Totals(stats.Filter{Account: first})
		if err != nil || a.Bytes != 50 || a.CompletedFiles != 0 || a.AttemptsFailed != 1 || a.ActiveMillis != 2000 {
			t.Fatalf("first account: %+v %v", a, err)
		}
		b, err := s.telemetry.store.Totals(stats.Filter{Account: "server/other"})
		if err != nil || b.Bytes != 70 || b.CompletedFiles != 1 || b.CompletedBytes != 200 || b.AttemptsCompleted != 1 || b.ActiveMillis != 1000 {
			t.Fatalf("second account: %+v %v", b, err)
		}
		s.mu.Lock()
		s.telemetry.pending = append(s.telemetry.pending, pending...)
		s.mu.Unlock()
		if err = s.flushStats(); err != nil {
			t.Fatal(err)
		}
		all, err := s.telemetry.store.Totals(stats.Filter{})
		if err != nil || all.Bytes != 120 || all.CompletedFiles != 1 {
			t.Fatalf("replay double counted: %+v %v", all, err)
		}
	})
}
func TestActiveTimeSplitsUTCMidnight(t *testing.T) {
	before := time.Date(2024, 12, 31, 23, 59, 59, 0, time.UTC)
	a := attemptStats{event: stats.Event{Account: "a", Session: "s", Direction: "upload"}, activeAt: before, days: map[string]stats.Event{}}
	a.checkpoint(before.Add(2 * time.Second))
	if len(a.days) != 2 || a.days["2024-12-31"].ActiveMillis != 1000 || a.days["2025-01-01"].ActiveMillis != 1000 {
		t.Fatalf("UTC split: %+v", a.days)
	}
}
