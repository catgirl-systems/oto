package daemon

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestCancelledCleanupPolicies(t *testing.T) {
	for _, completed := range []bool{false, true} {
		for _, cancelled := range []bool{false, true} {
			t.Run(fmt.Sprintf("completed=%t/cancelled=%t", completed, cancelled), func(t *testing.T) {
				s := downloadService(t)
				s.cfg.Uploads.AutoClearCompleted, s.cfg.Uploads.AutoClearCancelled = completed, cancelled
				for i, state := range []string{"queued", "running", "failed", "interrupted", "completed", "cancelled"} {
					e := soulseek.TransferEvent{Direction: "upload", Username: "peer", Filename: state, Attempt: uint64(i + 1), State: "queued", Total: 4}
					s.uploadUpdate(s.uploadEpoch, e)
					id := uploadRow(t, s, "peer", state).ID
					e.State = state
					s.uploadUpdate(s.uploadEpoch, e)
					_, exists := s.transfers[id]
					wantCleared := state == "completed" && completed || state == "cancelled" && cancelled
					if exists == wantCleared {
						t.Fatalf("%s exists=%t", state, exists)
					}
					if wantCleared {
						for _, u := range s.journal.Uploads {
							if u.ID == id {
								t.Fatal("cleared row remains in journal")
							}
						}
					}
				}
				if s.cfg.Downloads.AutoClearCompleted {
					t.Fatal("upload setting affected downloads")
				}
			})
		}
	}
}

func TestCancelledCleanupDisableAndReplacement(t *testing.T) {
	s := downloadService(t)
	s.SetConfigPath(filepath.Join(t.TempDir(), "config.json"))
	s.cfg.Uploads.AutoClearCancelled = true
	e := soulseek.TransferEvent{Direction: "upload", Username: "peer", Filename: "one", Attempt: 1, State: "queued", Total: 4}
	s.uploadUpdate(s.uploadEpoch, e)
	id := uploadRow(t, s, "peer", "one").ID
	storageTrigger(t, s, "fail_cleanup", "CREATE TRIGGER fail_cleanup BEFORE DELETE ON uploads BEGIN SELECT RAISE(ABORT, 'cleanup failure'); END")
	e.State = "cancelled"
	s.uploadUpdate(s.uploadEpoch, e)
	if s.telemetry.warning == "" || len(s.uploadCancelEligible) != 1 {
		t.Fatal("cleanup failure not retained and reported")
	}
	next := s.cfg
	next.Uploads.AutoClearCancelled = false
	if err := s.UpdateConfig(next); err != nil {
		t.Fatal(err)
	}
	if len(s.uploadCancelEligible) != 0 {
		t.Fatal("disable kept pending cleanup")
	}
	next.Uploads.AutoClearCancelled = true
	if err := s.UpdateConfig(next); err != nil {
		t.Fatal(err)
	}
	dropStorageTrigger(t, s, "fail_cleanup")
	if err := s.flushStats(); err != nil {
		t.Fatal(err)
	}
	result, err := s.UploadAction(UploadActionRequest{Action: "cancel", IDs: []string{id}})
	if err != nil || result.Skipped != 1 || len(s.Transfers()) != 1 {
		t.Fatalf("old cancellation swept: %+v %v", result, err)
	}
	if s.shareScan != nil {
		t.Fatal("toggle started a share scan")
	}

	// A pending deletion must never follow a reused ID into another attempt.
	e.Attempt, e.State = 2, "queued"
	s.uploadUpdate(s.uploadEpoch, e)
	storageTrigger(t, s, "fail_cleanup2", "CREATE TRIGGER fail_cleanup2 BEFORE DELETE ON uploads BEGIN SELECT RAISE(ABORT, 'cleanup failure'); END")
	e.State = "cancelled"
	s.uploadUpdate(s.uploadEpoch, e)
	old := e
	e.Attempt, e.State = 3, "queued"
	s.uploadUpdate(s.uploadEpoch, e)
	dropStorageTrigger(t, s, "fail_cleanup2")
	s.uploadUpdate(s.uploadEpoch, old)
	if err := s.flushStats(); err != nil {
		t.Fatal(err)
	}
	if uploadRow(t, s, "peer", "one").State != "queued" || len(s.uploadCancelEligible) != 0 {
		t.Fatal("stale cleanup touched replacement")
	}
	e.State = "cancelled"
	s.uploadUpdate(s.uploadEpoch, e)
	for _, state := range []string{"queued", "running", "completed", "cancelled"} {
		e.State = state
		s.uploadUpdate(s.uploadEpoch, e)
	}
	if len(s.Transfers()) != 0 || len(s.journal.Uploads) != 0 {
		t.Fatal("late callback resurrected cleared upload")
	}
}

func TestCancelRetainedUploadsAndRestart(t *testing.T) {
	s := downloadService(t)
	s.cfg.Uploads.AutoClearCancelled = true
	for i, state := range []string{"failed", "interrupted"} {
		e := soulseek.TransferEvent{Direction: "upload", Username: "peer", Filename: state, Attempt: uint64(i + 1), State: "queued", Total: 4}
		s.uploadUpdate(s.uploadEpoch, e)
		e.State = state
		s.uploadUpdate(s.uploadEpoch, e)
	}
	result, err := s.UploadAction(UploadActionRequest{Action: "cancel", Usernames: []string{"peer"}})
	if err != nil || result.Changed != 2 || len(result.Errors) != 0 || len(s.Transfers()) != 0 || len(s.journal.Uploads) != 0 {
		t.Fatalf("retained cancellation: %+v %v", result, err)
	}
	totals := storageUploadStats(t, s)
	events := storageCount(t, s, "SELECT count(*) FROM events")
	seq := s.journal.UploadSequence
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := New(s.cfg, s.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if len(restored.Transfers()) != 0 || storageUploadStats(t, restored) != totals || storageCount(t, restored, "SELECT count(*) FROM events") != events {
		t.Fatal("restart lost accounting or restored cleared rows")
	}
	e := soulseek.TransferEvent{Direction: "upload", Username: "peer", Filename: "fresh", Attempt: 1, State: "queued", Total: 4}
	restored.uploadUpdate(restored.uploadEpoch, e)
	if restored.journal.UploadSequence <= seq {
		t.Fatal("upload ID reused after restart")
	}
}
