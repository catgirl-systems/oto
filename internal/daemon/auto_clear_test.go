package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage"
)

func TestAutoClearNewDownloadsAndSequence(t *testing.T) {
	s := downloadService(t)
	s.SetConfigPath(filepath.Join(t.TempDir(), "config.json"))
	s.desktopNotify = func(context.Context, string, string) error { return errors.New("delivery failed") }
	failIf(t, s.cfg.Downloads.AutoClearCompleted || s.cfg.Uploads.AutoClearCompleted, "auto-clear must default off")
	downloads, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "Album/old", Size: 4}, {Filename: "Album/two", Size: 4}, {Filename: "Album/three", Size: 4}}}})
	must(t, err)
	finish := func(d Download) {
		s.updateDownload(d.ID, "running", 4, nil)
		s.completeDownload(d.ID, d.DownloadDir, putPartial(t, d.ID, "data"))
	}
	finish(downloads[0])
	logPath := filepath.Join(t.TempDir(), "hooks")
	t.Setenv("OTO_TEST_HOOK_LOG", logPath)
	next := s.cfg
	next.Downloads.AutoClearCompleted = true
	next.Downloads.FolderNotifications = true
	next.Downloads.AfterFileCommand = `printf 'file\n' >> "$OTO_TEST_HOOK_LOG"; exit 1`
	next.Downloads.AfterFolderCommand = `printf 'folder\n' >> "$OTO_TEST_HOOK_LOG"`
	must(t, s.UpdateConfig(next))
	failIf(t, len(s.Downloads()) != 3 || s.shareScan != nil, "hot enable swept history or scanned")
	finish(downloads[1])
	finish(downloads[2])
	if rows := s.Downloads(); len(rows) != 1 || rows[0].ID != downloads[0].ID {
		t.Fatalf("new completions not cleared: %+v", rows)
	}
	failIf(t, s.Snapshot().DownloadNotification.Sequence != 1, "folder completion notification lost")
	waitFor(t, func() bool { b, _ := os.ReadFile(logPath); return strings.Count(string(b), "\n") == 3 })
	for _, d := range downloads {
		if b, err := os.ReadFile(filepath.Join(d.DownloadDir, d.Destination)); err != nil || string(b) != "data" {
			t.Fatalf("download removed: %v", err)
		}
	}
	must(t, s.Close())
	restored, err := New(next, s.journalPath)
	must(t, err)
	failIf(t, len(restored.Downloads()) != 1, "startup swept old history")
	must(t, restored.clearCompletedDownload(downloads[0].ID))
	must(t, restored.Close())
	restored, err = New(next, s.journalPath)
	must(t, err)
	defer restored.Close()
	failIf(t, len(restored.Downloads()) != 0, "history not empty")
	queued, err := restored.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "next", Size: 1}}}})
	failIfFmt(t, err != nil || queued[0].ID != "d-4", "high-water ID reused: %+v %v", queued, err)
}

func TestAutoClearJournalFailuresAndNonCompleted(t *testing.T) {
	s := downloadService(t)
	s.cfg.Downloads.AutoClearCompleted = true
	rows, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "Album/song", Size: 4}}}})
	must(t, err)
	d := rows[0]
	for _, state := range []string{"queued", "running", "paused", "cancelled", "failed", "retrying", "finalizing"} {
		s.journal.Downloads[0].State = state
		if err := s.clearCompletedDownload(d.ID); err != nil || len(s.Downloads()) != 1 {
			t.Fatalf("cleared %s: %v", state, err)
		}
	}
	s.updateDownload(d.ID, "running", 4, nil)
	if _, err := s.stateDB.SQL().Exec("CREATE TRIGGER fail_download_update BEFORE UPDATE OF state ON downloads BEGIN SELECT RAISE(ABORT, 'injected update failure'); END"); err != nil {
		t.Fatal(err)
	}
	s.completeDownload(d.ID, d.DownloadDir, putPartial(t, d.ID, "data"))
	if rows := s.Downloads(); len(rows) != 1 || rows[0].State != "completed" || rows[0].Offset != 4 || s.Snapshot().DownloadNotification.Sequence != 0 {
		t.Fatalf("completion failure changed state: %+v notification=%+v", s.Downloads(), s.Snapshot().DownloadNotification)
	}
	if _, err := s.stateDB.SQL().Exec("DROP TRIGGER fail_download_update"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.stateDB.SQL().Exec("CREATE TRIGGER fail_download_delete BEFORE DELETE ON downloads BEGIN SELECT RAISE(ABORT, 'injected delete failure'); END"); err != nil {
		t.Fatal(err)
	}
	must(t, s.flushStats())
	if err := s.clearCompletedDownload(d.ID); err == nil || len(s.Downloads()) != 1 {
		t.Fatal("cleanup failure lost history")
	}
	if _, err := s.stateDB.SQL().Exec("DROP TRIGGER fail_download_delete"); err != nil {
		t.Fatal(err)
	}
	if err := s.clearCompletedDownload(d.ID); err != nil || len(s.Downloads()) != 0 {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(d.DownloadDir, d.Destination)); err != nil {
		t.Fatal("cleanup deleted data")
	}
}

func TestAutoClearUploadsKeepsAttemptGuards(t *testing.T) {
	s := downloadService(t)
	event := soulseek.TransferEvent{Direction: "upload", Username: "peer", Filename: "one", Attempt: 1, State: "queued", Total: 4}
	s.uploadUpdate(s.uploadEpoch, event)
	event.State = "completed"
	s.uploadUpdate(s.uploadEpoch, event)
	s.cfg.Uploads.AutoClearCompleted = true
	s.uploadUpdate(s.uploadEpoch, event) // duplicate old success is not a new completion
	event.Filename, event.Attempt, event.State = "two", 2, "queued"
	s.uploadUpdate(s.uploadEpoch, event)
	event.State = "completed"
	s.uploadUpdate(s.uploadEpoch, event)
	for _, state := range []string{"running", "completed", "queued", "failed"} {
		event.State = state
		s.uploadUpdate(s.uploadEpoch, event)
	}
	must(t, s.flushStats())
	failIfFmt(t, len(s.Transfers()) != 1 || s.Transfers()[0].Filename != "one", "cleared old history or resurrected completion: %+v", s.Transfers())
	event.Attempt, event.State = 3, "queued"
	s.uploadUpdate(s.uploadEpoch, event)
	event.State = "failed"
	s.uploadUpdate(s.uploadEpoch, event)
	failIf(t, len(s.Transfers()) != 2, "failed upload cleared or fresh attempt rejected")
	failIf(t, s.cfg.Downloads.AutoClearCompleted, "upload option changed downloads")
}

func TestAutoClearCancelledOnlyNewAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	cfg := testConfig(t)
	s, err := New(cfg, path)
	must(t, err)
	event := soulseek.TransferEvent{Direction: "upload", Username: "peer", Filename: "old", Attempt: 1, State: "queued", Total: 1}
	s.uploadUpdate(s.uploadEpoch, event)
	event.State = "cancelled"
	s.uploadUpdate(s.uploadEpoch, event)
	failIf(t, len(s.Transfers()) != 1, "disabled auto-clear removed old cancellation")
	next := s.cfg
	next.Uploads.AutoClearCancelled = true
	s.SetConfigPath(filepath.Join(t.TempDir(), "config.json"))
	must(t, s.UpdateConfig(next))
	must(t, s.flushStats())
	failIf(t, len(s.Transfers()) != 1, "enabling auto-clear swept old cancellation")
	must(t, s.Close())
	restored, err := New(next, path)
	must(t, err)
	newEvent := soulseek.TransferEvent{Direction: "upload", Username: "peer", Filename: "new", Attempt: 2, State: "queued", Total: 1}
	restored.uploadUpdate(restored.uploadEpoch, newEvent)
	newEvent.State = "cancelled"
	restored.uploadUpdate(restored.uploadEpoch, newEvent)
	if rows := restored.Transfers(); len(rows) != 1 || rows[0].Filename != "old" {
		t.Fatalf("restart/new cancellation cleanup: %+v", rows)
	}
	must(t, restored.Close())
}

func TestAutoClearCancelledRetriesUpdateAndDelete(t *testing.T) {
	s := downloadService(t)
	s.cfg.Uploads.AutoClearCancelled = true
	event := soulseek.TransferEvent{Direction: "upload", Username: "peer", Filename: "update", Attempt: 1, State: "queued", Total: 1}
	s.uploadUpdate(s.uploadEpoch, event)
	event.State = "running"
	s.uploadUpdate(s.uploadEpoch, event)
	id := s.journal.Uploads[0].ID
	storageTrigger(t, s, "fail_cancel_update", fmt.Sprintf("CREATE TRIGGER fail_cancel_update BEFORE UPDATE OF state ON uploads WHEN NEW.id = '%s' AND NEW.state = 'cancelled' BEGIN SELECT RAISE(ABORT, 'cancel update failure'); END", id))
	event.State = "cancelled"
	s.uploadUpdate(s.uploadEpoch, event)
	if got := uploadRow(t, s, "peer", "update"); got.State != "cancelled" {
		t.Fatalf("in-memory cancellation: %+v", got)
	}
	var state string
	must(t, s.stateDB.SQL().QueryRow("SELECT state FROM uploads WHERE id = ?", id).Scan(&state))
	failIfFmt(t, state != "running", "failed update reached disk: %s", state)
	dropStorageTrigger(t, s, "fail_cancel_update")
	must(t, s.flushStats())
	if count, totals := storageCount(t, s, "SELECT count(*) FROM uploads WHERE id = ?", id), storageUploadStats(t, s); count != 0 || totals.AttemptsCancelled != 1 {
		t.Fatalf("telemetry did not retry cancelled update and deletion: count=%d totals=%+v", count, totals)
	}

	event.Filename, event.Attempt, event.State = "delete", 2, "queued"
	s.uploadUpdate(s.uploadEpoch, event)
	deleteID := uploadRow(t, s, "peer", "delete").ID
	storageTrigger(t, s, "fail_cancel_delete", "CREATE TRIGGER fail_cancel_delete BEFORE DELETE ON uploads BEGIN SELECT RAISE(ABORT, 'cancel delete failure'); END")
	event.State = "cancelled"
	s.uploadUpdate(s.uploadEpoch, event)
	failIf(t, len(s.Transfers()) != 1, "delete failure lost cancelled history")
	must(t, s.flushStats())
	if storageCount(t, s, "SELECT count(*) FROM uploads WHERE id = ?", deleteID) != 1 {
		t.Fatal("delete failure removed history")
	}
	dropStorageTrigger(t, s, "fail_cancel_delete")
	must(t, s.flushStats())
	if storageCount(t, s, "SELECT count(*) FROM uploads WHERE id = ?", deleteID) != 0 {
		t.Fatal("telemetry did not retry cancelled deletion")
	}
}

func TestOldJournalSequenceMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	db, err := storage.Open(path)
	must(t, err)
	if err := db.Queries().UpsertDownload(context.Background(), downloadParams(Download{ID: "d-42", State: "completed"})); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	must(t, db.Close())
	s, err := New(testConfig(t), path)
	must(t, err)
	defer s.Close()
	if err := s.clearCompletedDownload("d-42"); err != nil || s.seq != 42 {
		t.Fatalf("migration: %v seq=%d", err, s.seq)
	}
}
