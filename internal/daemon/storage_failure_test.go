package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/stats"
)

func TestStorageFailedDownloadOutcomeRetried(t *testing.T) {
	s := downloadService(t)
	queued, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "retry.flac", Size: 10}}}})
	if err != nil {
		t.Fatal(err)
	}
	id := queued[0].ID
	s.mu.Lock()
	s.transfers[id] = Transfer{ID: id, Username: "peer", Filename: queued[0].Filename, Direction: "download", State: "running", Total: queued[0].Size}
	s.statsBeginLocked(id, accountKey(s.cfg))
	s.startTransferLocked(id, 0)
	s.mu.Unlock()
	if err := s.updateDownload(id, "running", 0, nil); err != nil {
		t.Fatal(err)
	}
	s.updateTransferProgress(id, soulseek.Progress{Done: 4})
	storageTrigger(t, s, "reject_outcome", "CREATE TRIGGER reject_outcome BEFORE UPDATE ON downloads WHEN NEW.state = 'failed' BEGIN SELECT RAISE(ABORT, 'injected failure'); END")
	if err := s.updateDownload(id, "failed", 4, errors.New("network lost")); err == nil {
		t.Fatal("failed write reported success")
	}
	row, err := s.stateDB.Queries().GetDownload(context.Background(), id)
	if err != nil || row.State != "running" {
		t.Fatalf("rollback: %+v %v", row, err)
	}
	if totals := storageDownloadStats(t, s); totals.Bytes != 0 || totals.AttemptsFailed != 0 {
		t.Fatalf("rolled-back accounting: %+v", totals)
	}
	dropStorageTrigger(t, s, "reject_outcome")
	if err := s.flushStats(); err != nil {
		t.Fatal(err)
	}
	row, err = s.stateDB.Queries().GetDownload(context.Background(), id)
	if err != nil || row.State != "failed" {
		t.Fatalf("retry: %+v %v", row, err)
	}
	before := storageDownloadStats(t, s)
	if before.Bytes != 4 || before.AttemptsFailed != 1 {
		t.Fatalf("lost outcome: %+v", before)
	}
	if err := s.flushStats(); err != nil {
		t.Fatal(err)
	}
	if after := storageDownloadStats(t, s); before != after {
		t.Fatalf("retry double counted: %+v => %+v", before, after)
	}
}

func TestStorageOverflowRollsBackDownloadAndMarker(t *testing.T) {
	s := downloadService(t)
	queued, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "overflow.flac", Size: 2}}}})
	if err != nil {
		t.Fatal(err)
	}
	id := queued[0].ID
	s.mu.Lock()
	s.transfers[id] = Transfer{ID: id, Username: "peer", Filename: queued[0].Filename, Direction: "download", State: "running", Total: queued[0].Size}
	s.statsBeginLocked(id, accountKey(s.cfg))
	s.startTransferLocked(id, 0)
	s.mu.Unlock()
	if err := s.updateDownload(id, "running", 0, nil); err != nil {
		t.Fatal(err)
	}
	seed := stats.Event{ID: "overflow-seed", Account: accountKey(s.cfg), Session: s.telemetry.session, Peer: "peer", Direction: "download", Kind: stats.KindProgress, At: time.Now().UTC(), Bytes: ^uint64(0)}
	if err := s.telemetry.store.RecordBatch([]stats.Event{seed}); err != nil {
		t.Fatal(err)
	}
	before := storageCount(t, s, "SELECT count(*) FROM events")
	s.updateTransferProgress(id, soulseek.Progress{Done: 1})
	if err := s.updateDownload(id, "failed", 1, errors.New("network lost")); err == nil || !strings.Contains(err.Error(), "counter overflow") {
		t.Fatalf("overflow: %v", err)
	}
	row, err := s.stateDB.Queries().GetDownload(context.Background(), id)
	if err != nil || row.State != "running" {
		t.Fatalf("transfer committed without accounting: %+v %v", row, err)
	}
	if storageCount(t, s, "SELECT count(*) FROM events") != before || storageCount(t, s, "SELECT count(*) FROM active_attempts") != 1 {
		t.Fatal("overflow changed events or recovery marker")
	}
	if totals := storageDownloadStats(t, s); totals.Bytes != ^uint64(0) || totals.AttemptsFailed != 0 {
		t.Fatalf("overflow changed totals: %+v", totals)
	}
}
