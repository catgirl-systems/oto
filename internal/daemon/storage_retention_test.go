package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/stats"
	"github.com/catgirl-systems/oto/internal/storage"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

func TestStoragePruningPreservesOtherDomainsAndRecovery(t *testing.T) {
	s := downloadService(t)
	ctx, q := context.Background(), s.stateDB.Queries()
	queued, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "retained.flac", Size: 10}}}})
	must(t, err)
	id := queued[0].ID
	s.mu.Lock()
	s.transfers[id] = Transfer{ID: id, Username: "peer", Filename: "retained.flac", Direction: "download", State: "running", Total: 10}
	s.statsBeginLocked(id, accountKey(s.cfg))
	s.mu.Unlock()
	must(t, s.updateDownload(id, "running", 0, nil))
	must(t, q.UpsertWishlist(ctx, db.UpsertWishlistParams{ID: "w-1", Query: "keep", AddedAt: time.Now().UTC().UnixNano(), NotificationSequence: storage.EncodeUint64(1)}))
	must(t, q.UpsertHistory(ctx, db.UpsertHistoryParams{Kind: "search", Value: "keep", Recency: storage.EncodeUint64(1)}))
	must(t, q.UpsertUIPreference(ctx, db.UpsertUIPreferenceParams{Key: "test", Value: "keep"}))
	snapshot, err := stageShareSnapshot(ctx, s.stateDB, "remote", "peer", nil, nil, nil, []soulseek.ShareEntry{{Name: "keep.flac", Size: 10}})
	if err == nil {
		err = publishShareSnapshot(ctx, s.stateDB, snapshot, "remote", "peer")
	}
	must(t, err)
	old := stats.Event{ID: "old", Account: accountKey(s.cfg), Session: s.telemetry.session, Peer: "peer", Direction: "download", Kind: stats.KindProgress, At: time.Now().UTC().Add(-72 * time.Hour), Bytes: 5}
	must(t, s.telemetry.store.RecordBatch([]stats.Event{old}))
	counts := map[string]int{}
	for _, table := range []string{"state_meta", "downloads", "active_attempts", "wishlist", "history", "ui_preferences", "share_snapshots", "share_heads", "share_entries"} {
		counts[table] = storageCount(t, s, "SELECT count(*) FROM "+table)
	}
	result, err := s.telemetry.store.Prune(time.Now().UTC().Add(-24*time.Hour), true, true, true)
	failIfFmt(t, err != nil || result.Logs != 1 || result.Daily == 0, "prune: %+v %v", result, err)
	for table, count := range counts {
		if got := storageCount(t, s, "SELECT count(*) FROM "+table); got != count {
			t.Fatalf("prune changed %s: %d != %d", table, got, count)
		}
	}
	must(t, s.telemetry.store.RecordBatch([]stats.Event{old}))
	if storageCount(t, s, "SELECT count(*) FROM events WHERE id = 'old'") != 0 {
		t.Fatal("replay resurrected pruned event")
	}
	if totals := storageDownloadStats(t, s); totals.Bytes != 5 {
		t.Fatalf("lifetime totals: %+v", totals)
	}
}
