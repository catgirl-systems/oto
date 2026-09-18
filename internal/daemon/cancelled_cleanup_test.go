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
					failIfFmt(t, exists == wantCleared, "%s exists=%t", state, exists)
					if wantCleared {
						for _, u := range s.journal.Uploads {
							failIf(t, u.ID == id, "cleared row remains in journal")
						}
					}
				}
				failIf(t, s.cfg.Downloads.AutoClearCompleted, "upload setting affected downloads")
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
	failIf(t, s.telemetry.warning == "" || len(s.uploadCancelEligible) != 1, "cleanup failure not retained and reported")
	next := s.cfg
	next.Uploads.AutoClearCancelled = false
	must(t, s.UpdateConfig(next))
	failIf(t, len(s.uploadCancelEligible) != 0, "disable kept pending cleanup")
	next.Uploads.AutoClearCancelled = true
	must(t, s.UpdateConfig(next))
	dropStorageTrigger(t, s, "fail_cleanup")
	must(t, s.flushStats())
	result, err := s.UploadAction(UploadActionRequest{Action: "cancel", IDs: []string{id}})
	failIfFmt(t, err != nil || result.Skipped != 1 || len(s.Transfers()) != 1, "old cancellation swept: %+v %v", result, err)
	failIf(t, s.shareScan != nil, "toggle started a share scan")

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
	must(t, s.flushStats())
	failIf(t, uploadRow(t, s, "peer", "one").State != "queued" || len(s.uploadCancelEligible) != 0, "stale cleanup touched replacement")
	e.State = "cancelled"
	s.uploadUpdate(s.uploadEpoch, e)
	for _, state := range []string{"queued", "running", "completed", "cancelled"} {
		e.State = state
		s.uploadUpdate(s.uploadEpoch, e)
	}
	failIf(t, len(s.Transfers()) != 0 || len(s.journal.Uploads) != 0, "late callback resurrected cleared upload")
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
	failIfFmt(t, err != nil || result.Changed != 2 || len(result.Errors) != 0 || len(s.Transfers()) != 0 || len(s.journal.Uploads) != 0, "retained cancellation: %+v %v", result, err)
	totals := storageUploadStats(t, s)
	events := storageCount(t, s, "SELECT count(*) FROM events")
	seq := s.journal.UploadSequence
	must(t, s.Close())
	restored, err := New(s.cfg, s.journalPath)
	must(t, err)
	defer restored.Close()
	failIf(t, len(restored.Transfers()) != 0 || storageUploadStats(t, restored) != totals || storageCount(t, restored, "SELECT count(*) FROM events") != events, "restart lost accounting or restored cleared rows")
	e := soulseek.TransferEvent{Direction: "upload", Username: "peer", Filename: "fresh", Attempt: 1, State: "queued", Total: 4}
	restored.uploadUpdate(restored.uploadEpoch, e)
	failIf(t, restored.journal.UploadSequence <= seq, "upload ID reused after restart")
}
