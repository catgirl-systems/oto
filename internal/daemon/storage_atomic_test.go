package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/stats"
	"github.com/catgirl-systems/oto/internal/storage"
)

func storageTrigger(t *testing.T, s *Service, name, sql string) {
	t.Helper()
	if _, err := s.stateDB.SQL().Exec(sql); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.stateDB.SQL().Exec("DROP TRIGGER IF EXISTS " + name); err != nil {
			t.Errorf("drop trigger %s: %v", name, err)
		}
	})
}

func dropStorageTrigger(t *testing.T, s *Service, name string) {
	t.Helper()
	if _, err := s.stateDB.SQL().Exec("DROP TRIGGER " + name); err != nil {
		t.Fatal(err)
	}
}

func storageCount(t *testing.T, s *Service, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.stateDB.SQL().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func storageStats(t *testing.T, s *Service, direction string) stats.Totals {
	t.Helper()
	overview, err := s.Statistics(stats.Filter{Direction: direction})
	if err != nil {
		t.Fatal(err)
	}
	if direction == "upload" {
		return overview.Lifetime.Upload
	}
	return overview.Lifetime.Download
}

func storageDownloadStats(t *testing.T, s *Service) stats.Totals {
	return storageStats(t, s, "download")
}

func storageUploadStats(t *testing.T, s *Service) stats.Totals {
	return storageStats(t, s, "upload")
}

func TestStorageForceDownloadsRollbackAcrossRows(t *testing.T) {
	s := downloadService(t)
	s.cfg.Downloads.FiltersEnabled = true
	s.cfg.Downloads.FilterPatterns = []string{"*.exe"}
	rows, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "one.exe", Size: 1}, {Filename: "two.exe", Size: 2}}}})
	if err != nil {
		t.Fatal(err)
	}
	beforeRows := s.Downloads()
	beforeStats := storageDownloadStats(t, s)
	beforeEvents := storageCount(t, s, "SELECT count(*) FROM events")
	beforeSeen := storageCount(t, s, "SELECT count(*) FROM seen")
	s.mu.Lock()
	beforeMemory := s.snapshotLocked()
	s.mu.Unlock()

	second := rows[1].ID
	storageTrigger(t, s, "fail_force_second", fmt.Sprintf("CREATE TRIGGER fail_force_second BEFORE UPDATE OF state ON downloads WHEN NEW.id = '%s' BEGIN SELECT RAISE(ABORT, 'force failure'); END", second))
	if _, err := s.stateDB.SQL().Exec("UPDATE downloads SET state = state WHERE id = ?", rows[0].ID); err != nil {
		t.Fatalf("unrelated-row trigger fired: %v", err)
	}
	if _, err := s.ForceDownloads([]string{rows[0].ID, rows[1].ID}); err == nil {
		t.Fatal("forced batch unexpectedly committed")
	}
	if got := s.Downloads(); !reflect.DeepEqual(got, beforeRows) {
		t.Fatalf("in-memory rows changed: got %#v want %#v", got, beforeRows)
	}
	s.mu.Lock()
	afterMemory := s.snapshotLocked()
	s.mu.Unlock()
	if !reflect.DeepEqual(afterMemory, beforeMemory) {
		t.Fatal("in-memory rollback lost state")
	}
	if got := storageDownloadStats(t, s); got != beforeStats {
		t.Fatalf("stats changed on rollback: got %#v want %#v", got, beforeStats)
	}
	if got := storageCount(t, s, "SELECT count(*) FROM events"); got != beforeEvents || storageCount(t, s, "SELECT count(*) FROM seen") != beforeSeen {
		t.Fatalf("stats rows changed on rollback: events=%d seen=%d", got, storageCount(t, s, "SELECT count(*) FROM seen"))
	}

	dropStorageTrigger(t, s, "fail_force_second")
	result, err := s.ForceDownloads([]string{rows[0].ID, rows[1].ID})
	if err != nil || result.Changed != 2 || result.Skipped != 0 || len(result.Errors) != 0 {
		t.Fatalf("retry forced result: %+v %v", result, err)
	}
	for _, d := range s.Downloads() {
		if d.FilterBypass != true || d.State != "queued" {
			t.Fatalf("forced row: %+v", d)
		}
	}
	statsAfter := storageDownloadStats(t, s)
	if statsAfter.Forced != beforeStats.Forced+2 || statsAfter.Filtered != beforeStats.Filtered {
		t.Fatalf("forced accounting: before=%#v after=%#v", beforeStats, statsAfter)
	}
	if got := storageCount(t, s, "SELECT count(*) FROM events"); got != beforeEvents+2 {
		t.Fatalf("forced event count=%d, want %d", got, beforeEvents+2)
	}
}

func TestStorageQueueDownloadsRollbackSequenceAndEvents(t *testing.T) {
	s := downloadService(t)
	storageTrigger(t, s, "fail_queue_second", "CREATE TRIGGER fail_queue_second BEFORE INSERT ON downloads WHEN NEW.id = 'd-2' BEGIN SELECT RAISE(ABORT, 'queue failure'); END")
	if _, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "one", Size: 1}, {Filename: "two", Size: 2}}}}); err == nil {
		t.Fatal("queued batch unexpectedly committed")
	}
	if len(s.Downloads()) != 0 || s.seq != 0 || len(s.transfers) != 0 {
		t.Fatalf("in-memory queue advanced: rows=%d seq=%d transfers=%d", len(s.Downloads()), s.seq, len(s.transfers))
	}
	meta, err := s.stateDB.Queries().GetStateMeta(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	seq, err := storage.Uint64FromBytes(meta.DownloadSequence)
	if err != nil {
		t.Fatal(err)
	}
	if seq != 0 || storageCount(t, s, "SELECT count(*) FROM downloads") != 0 || storageCount(t, s, "SELECT count(*) FROM events") != 0 || storageCount(t, s, "SELECT count(*) FROM seen") != 0 {
		t.Fatalf("failed queue advanced disk: seq=%d downloads=%d events=%d seen=%d", seq, storageCount(t, s, "SELECT count(*) FROM downloads"), storageCount(t, s, "SELECT count(*) FROM events"), storageCount(t, s, "SELECT count(*) FROM seen"))
	}
}

func TestStorageFailedUploadWaitsForExplicitFlush(t *testing.T) {
	s := downloadService(t)
	event := soulseek.TransferEvent{Direction: "upload", Username: "peer", Filename: "file.bin", Attempt: 1, State: "queued", Total: 4}
	s.uploadUpdate(s.uploadEpoch, event)
	event.State, event.Done = "running", 0
	s.uploadUpdate(s.uploadEpoch, event)
	id := s.journal.Uploads[0].ID
	if got := storageCount(t, s, "SELECT count(*) FROM active_attempts WHERE transfer_id = ?", id); got != 1 {
		t.Fatalf("active marker count=%d, want 1", got)
	}
	beforeStats := storageUploadStats(t, s)
	beforeEvents := storageCount(t, s, "SELECT count(*) FROM events")
	storageTrigger(t, s, "fail_upload_terminal", fmt.Sprintf("CREATE TRIGGER fail_upload_terminal BEFORE UPDATE OF state ON uploads WHEN NEW.id = '%s' AND NEW.state = 'failed' BEGIN SELECT RAISE(ABORT, 'upload failure'); END", id))

	event.State, event.Error, event.Done = "failed", "failed", 4
	s.uploadUpdate(s.uploadEpoch, event)
	if got := storageCount(t, s, "SELECT count(*) FROM active_attempts WHERE transfer_id = ?", id); got != 1 {
		t.Fatalf("terminal failure cleared active marker: %d", got)
	}
	var state string
	if err := s.stateDB.SQL().QueryRow("SELECT state FROM uploads WHERE id = ?", id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "running" {
		t.Fatalf("failed upload reached disk before flush: %s", state)
	}
	if got := storageUploadStats(t, s); got != beforeStats || storageCount(t, s, "SELECT count(*) FROM events") != beforeEvents {
		t.Fatal("failed upload accounting committed by its terminal attempt")
	}
	if _, err := s.QueueDownloads([]DownloadRequest{{Username: "other", Files: []DownloadItem{{Filename: "unrelated", Size: 1}}}}); err != nil {
		t.Fatal(err)
	}
	unrelatedEvents := storageCount(t, s, "SELECT count(*) FROM events")
	if got := storageCount(t, s, "SELECT count(*) FROM active_attempts WHERE transfer_id = ?", id); got != 1 {
		t.Fatalf("unrelated queue cleared active marker: %d", got)
	}
	if err := s.stateDB.SQL().QueryRow("SELECT state FROM uploads WHERE id = ?", id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "running" || storageUploadStats(t, s) != beforeStats {
		t.Fatal("unrelated queue committed failed upload")
	}

	dropStorageTrigger(t, s, "fail_upload_terminal")
	if err := s.flushStats(); err != nil {
		t.Fatal(err)
	}
	if err := s.stateDB.SQL().QueryRow("SELECT state FROM uploads WHERE id = ?", id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || storageCount(t, s, "SELECT count(*) FROM active_attempts WHERE transfer_id = ?", id) != 0 {
		t.Fatalf("flush did not commit terminal upload: state=%s active=%d", state, storageCount(t, s, "SELECT count(*) FROM active_attempts WHERE transfer_id = ?", id))
	}
	firstStats := storageUploadStats(t, s)
	if firstStats.AttemptsFailed != beforeStats.AttemptsFailed+1 || storageCount(t, s, "SELECT count(*) FROM events") != unrelatedEvents+1 {
		t.Fatalf("failed accounting after flush: before=%#v after=%#v", beforeStats, firstStats)
	}
	if err := s.flushStats(); err != nil {
		t.Fatal(err)
	}
	if got := storageUploadStats(t, s); got != firstStats || storageCount(t, s, "SELECT count(*) FROM events") != unrelatedEvents+1 {
		t.Fatal("repeated flush duplicated upload accounting")
	}
}

func TestStorageCompletedFileFlushesAfterDBFailure(t *testing.T) {
	s := downloadService(t)
	s.cfg.Downloads.FileNotifications = true
	var notifications atomic.Int32
	s.desktopNotify = func(context.Context, string, string) error {
		notifications.Add(1)
		return nil
	}
	rows, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "song", Size: 4}}}})
	if err != nil {
		t.Fatal(err)
	}
	d := rows[0]
	if err := s.updateDownload(d.ID, "running", d.Size, nil); err != nil {
		t.Fatal(err)
	}
	beforeStats := storageDownloadStats(t, s)
	beforeEvents := storageCount(t, s, "SELECT count(*) FROM events")
	storageTrigger(t, s, "fail_download_completion", fmt.Sprintf("CREATE TRIGGER fail_download_completion BEFORE UPDATE OF state ON downloads WHEN NEW.id = '%s' AND NEW.state = 'completed' BEGIN SELECT RAISE(ABORT, 'completion failure'); END", d.ID))

	part := putPartial(t, d.ID, "data")
	s.completeDownload(d.ID, d.DownloadDir, part)
	completed := s.Downloads()[0]
	target := filepath.Join(completed.DownloadDir, completed.Destination)
	if got, err := os.ReadFile(target); err != nil || string(got) != "data" {
		t.Fatalf("completed file was not retained: %q %v", got, err)
	}
	if notifications.Load() != 0 || s.Snapshot().DownloadNotification.Sequence != 0 {
		t.Fatal("completion notified before durable commit")
	}
	if got := storageCount(t, s, "SELECT count(*) FROM events"); got != beforeEvents {
		t.Fatalf("completion accounting committed before flush: %d", got)
	}

	dropStorageTrigger(t, s, "fail_download_completion")
	if err := s.flushStats(); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	if got := s.Downloads()[0]; got.State != "completed" || got.Offset != d.Size {
		t.Fatalf("completed state after flush: %+v", got)
	}
	if notifications.Load() != 1 || s.Snapshot().DownloadNotification.Sequence != 1 {
		t.Fatalf("completion notification count=%d snapshot=%+v", notifications.Load(), s.Snapshot().DownloadNotification)
	}
	statsAfter := storageDownloadStats(t, s)
	if statsAfter.CompletedFiles != beforeStats.CompletedFiles+1 || storageCount(t, s, "SELECT count(*) FROM events") != beforeEvents+1 {
		t.Fatalf("completion accounting: before=%#v after=%#v", beforeStats, statsAfter)
	}
	paths, err := filepath.Glob(filepath.Join(d.DownloadDir, "peer", "song*"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("completed path count=%d err=%v paths=%v", len(paths), err, paths)
	}
	if err := s.flushStats(); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	if notifications.Load() != 1 || storageDownloadStats(t, s) != statsAfter {
		t.Fatal("repeated flush duplicated completion")
	}
}
