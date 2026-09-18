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
	must(t, err)
	id := queued[0].ID
	s.mu.Lock()
	s.transfers[id] = Transfer{ID: id, Username: "peer", Filename: queued[0].Filename, Direction: "download", State: "running", Total: queued[0].Size}
	s.statsBeginLocked(id, accountKey(s.cfg))
	s.startTransferLocked(id, 0)
	s.mu.Unlock()
	must(t, s.updateDownload(id, "running", 0, nil))
	s.updateTransferProgress(id, soulseek.Progress{Done: 4})
	storageTrigger(t, s, "reject_outcome", "CREATE TRIGGER reject_outcome BEFORE UPDATE ON downloads WHEN NEW.state = 'failed' BEGIN SELECT RAISE(ABORT, 'injected failure'); END")
	if err := s.updateDownload(id, "failed", 4, errors.New("network lost")); err == nil {
		t.Fatal("failed write reported success")
	}
	row, err := s.stateDB.Queries().GetDownload(context.Background(), id)
	failIfFmt(t, err != nil || row.State != "running", "rollback: %+v %v", row, err)
	if totals := storageDownloadStats(t, s); totals.Bytes != 0 || totals.AttemptsFailed != 0 {
		t.Fatalf("rolled-back accounting: %+v", totals)
	}
	dropStorageTrigger(t, s, "reject_outcome")
	must(t, s.flushStats())
	row, err = s.stateDB.Queries().GetDownload(context.Background(), id)
	failIfFmt(t, err != nil || row.State != "failed", "retry: %+v %v", row, err)
	before := storageDownloadStats(t, s)
	failIfFmt(t, before.Bytes != 4 || before.AttemptsFailed != 1, "lost outcome: %+v", before)
	must(t, s.flushStats())
	if after := storageDownloadStats(t, s); before != after {
		t.Fatalf("retry double counted: %+v => %+v", before, after)
	}
}

func TestStorageOverflowRollsBackDownloadAndMarker(t *testing.T) {
	s := downloadService(t)
	queued, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "overflow.flac", Size: 2}}}})
	must(t, err)
	id := queued[0].ID
	s.mu.Lock()
	s.transfers[id] = Transfer{ID: id, Username: "peer", Filename: queued[0].Filename, Direction: "download", State: "running", Total: queued[0].Size}
	s.statsBeginLocked(id, accountKey(s.cfg))
	s.startTransferLocked(id, 0)
	s.mu.Unlock()
	must(t, s.updateDownload(id, "running", 0, nil))
	seed := stats.Event{ID: "overflow-seed", Account: accountKey(s.cfg), Session: s.telemetry.session, Peer: "peer", Direction: "download", Kind: stats.KindProgress, At: time.Now().UTC(), Bytes: ^uint64(0)}
	must(t, s.telemetry.store.RecordBatch([]stats.Event{seed}))
	before := storageCount(t, s, "SELECT count(*) FROM events")
	s.updateTransferProgress(id, soulseek.Progress{Done: 1})
	if err := s.updateDownload(id, "failed", 1, errors.New("network lost")); err == nil || !strings.Contains(err.Error(), "counter overflow") {
		t.Fatalf("overflow: %v", err)
	}
	row, err := s.stateDB.Queries().GetDownload(context.Background(), id)
	failIfFmt(t, err != nil || row.State != "running", "transfer committed without accounting: %+v %v", row, err)
	failIf(t, storageCount(t, s, "SELECT count(*) FROM events") != before || storageCount(t, s, "SELECT count(*) FROM active_attempts") != 1, "overflow changed events or recovery marker")
	if totals := storageDownloadStats(t, s); totals.Bytes != ^uint64(0) || totals.AttemptsFailed != 0 {
		t.Fatalf("overflow changed totals: %+v", totals)
	}
}
