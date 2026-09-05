package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestAutoClearNewDownloadsAndSequence(t *testing.T) {
	s := downloadService(t)
	s.SetConfigPath(filepath.Join(t.TempDir(), "config.json"))
	s.desktopNotify = func(context.Context, string, string) error { return errors.New("delivery failed") }
	if s.cfg.Downloads.AutoClearCompleted || s.cfg.Uploads.AutoClearCompleted {
		t.Fatal("auto-clear must default off")
	}
	downloads, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "Album/old", Size: 4}, {Filename: "Album/two", Size: 4}, {Filename: "Album/three", Size: 4}}}})
	if err != nil {
		t.Fatal(err)
	}
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
	if err := s.UpdateConfig(next); err != nil {
		t.Fatal(err)
	}
	if len(s.Downloads()) != 3 || s.shareScan != nil {
		t.Fatal("hot enable swept history or scanned")
	}
	finish(downloads[1])
	finish(downloads[2])
	if rows := s.Downloads(); len(rows) != 1 || rows[0].ID != downloads[0].ID {
		t.Fatalf("new completions not cleared: %+v", rows)
	}
	if s.Snapshot().DownloadNotification.Sequence != 1 {
		t.Fatal("folder completion notification lost")
	}
	waitFor(t, func() bool { b, _ := os.ReadFile(logPath); return strings.Count(string(b), "\n") == 3 })
	for _, d := range downloads {
		if b, err := os.ReadFile(filepath.Join(d.DownloadDir, d.Destination)); err != nil || string(b) != "data" {
			t.Fatalf("download removed: %v", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := New(next, s.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Downloads()) != 1 {
		t.Fatal("startup swept old history")
	}
	if err := restored.clearCompletedDownload(downloads[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err = New(next, s.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if len(restored.Downloads()) != 0 {
		t.Fatal("history not empty")
	}
	queued, err := restored.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "next", Size: 1}}}})
	if err != nil || queued[0].ID != "d-4" {
		t.Fatalf("high-water ID reused: %+v %v", queued, err)
	}
}

func TestAutoClearJournalFailuresAndNonCompleted(t *testing.T) {
	s := downloadService(t)
	s.cfg.Downloads.AutoClearCompleted = true
	rows, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "Album/song", Size: 4}}}})
	if err != nil {
		t.Fatal(err)
	}
	d := rows[0]
	for _, state := range []string{"queued", "running", "paused", "cancelled", "failed", "retrying", "finalizing"} {
		s.journal.Downloads[0].State = state
		if err := s.clearCompletedDownload(d.ID); err != nil || len(s.Downloads()) != 1 {
			t.Fatalf("cleared %s: %v", state, err)
		}
	}
	s.updateDownload(d.ID, "running", 4, nil)
	path := s.journalPath
	s.journalPath = t.TempDir() // completed-state save cannot replace a directory
	s.completeDownload(d.ID, d.DownloadDir, putPartial(t, d.ID, "data"))
	if rows := s.Downloads(); len(rows) != 1 || rows[0].State != "completed" || s.Snapshot().DownloadNotification.Sequence != 0 {
		t.Fatal("completion-save failure cleared history or notified")
	}
	if err := s.clearCompletedDownload(d.ID); err == nil || len(s.Downloads()) != 1 {
		t.Fatal("cleanup-save failure lost history")
	}
	s.journalPath = path
	if err := s.saveJournalLocked(); err != nil {
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
	if len(s.Transfers()) != 1 || s.Transfers()[0].Filename != "one" {
		t.Fatalf("cleared old history or resurrected completion: %+v", s.Transfers())
	}
	event.Attempt, event.State = 3, "queued"
	s.uploadUpdate(s.uploadEpoch, event)
	event.State = "failed"
	s.uploadUpdate(s.uploadEpoch, event)
	if len(s.Transfers()) != 2 {
		t.Fatal("failed upload cleared or fresh attempt rejected")
	}
	if s.cfg.Downloads.AutoClearCompleted {
		t.Fatal("upload option changed downloads")
	}
}

func TestOldJournalSequenceMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	if err := config.SaveJSON(path, Journal{Downloads: []Download{{ID: "d-42", State: "completed"}}}); err != nil {
		t.Fatal(err)
	}
	s, err := New(testConfig(t), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.clearCompletedDownload("d-42"); err != nil || s.seq != 42 {
		t.Fatalf("migration: %v seq=%d", err, s.seq)
	}
}
