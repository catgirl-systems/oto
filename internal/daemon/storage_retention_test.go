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
	if err != nil {
		t.Fatal(err)
	}
	id := queued[0].ID
	s.mu.Lock()
	s.transfers[id] = Transfer{ID: id, Username: "peer", Filename: "retained.flac", Direction: "download", State: "running", Total: 10}
	s.statsBeginLocked(id, accountKey(s.cfg))
	s.mu.Unlock()
	if err := s.updateDownload(id, "running", 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := q.UpsertWishlist(ctx, db.UpsertWishlistParams{ID: "w-1", Query: "keep", AddedAt: time.Now().UTC().UnixNano(), NotificationSequence: storage.EncodeUint64(1)}); err != nil {
		t.Fatal(err)
	}
	if err := q.UpsertHistory(ctx, db.UpsertHistoryParams{Kind: "search", Value: "keep", Recency: storage.EncodeUint64(1)}); err != nil {
		t.Fatal(err)
	}
	if err := q.UpsertUIPreference(ctx, db.UpsertUIPreferenceParams{Key: "test", Value: "keep"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := stageShareSnapshot(ctx, s.stateDB, "remote", "peer", nil, nil, nil, []soulseek.ShareEntry{{Name: "keep.flac", Size: 10}})
	if err == nil {
		err = publishShareSnapshot(ctx, s.stateDB, snapshot, "remote", "peer")
	}
	if err != nil {
		t.Fatal(err)
	}
	old := stats.Event{ID: "old", Account: accountKey(s.cfg), Session: s.telemetry.session, Peer: "peer", Direction: "download", Kind: stats.KindProgress, At: time.Now().UTC().Add(-72 * time.Hour), Bytes: 5}
	if err := s.telemetry.store.RecordBatch([]stats.Event{old}); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, table := range []string{"state_meta", "downloads", "active_attempts", "wishlist", "history", "ui_preferences", "share_snapshots", "share_heads", "share_entries"} {
		counts[table] = storageCount(t, s, "SELECT count(*) FROM "+table)
	}
	result, err := s.telemetry.store.Prune(time.Now().UTC().Add(-24*time.Hour), true, true, true)
	if err != nil || result.Logs != 1 || result.Daily == 0 {
		t.Fatalf("prune: %+v %v", result, err)
	}
	for table, count := range counts {
		if got := storageCount(t, s, "SELECT count(*) FROM "+table); got != count {
			t.Fatalf("prune changed %s: %d != %d", table, got, count)
		}
	}
	if err := s.telemetry.store.RecordBatch([]stats.Event{old}); err != nil {
		t.Fatal(err)
	}
	if storageCount(t, s, "SELECT count(*) FROM events WHERE id = 'old'") != 0 {
		t.Fatal("replay resurrected pruned event")
	}
	if totals := storageDownloadStats(t, s); totals.Bytes != 5 {
		t.Fatalf("lifetime totals: %+v", totals)
	}
}
