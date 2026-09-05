package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func newWishlistService(t *testing.T) *Service {
	t.Helper()
	s, err := New(testConfig(t), filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestWishlistStateRoundTripAndUpsert(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, "state.sqlite3")
	s, err := New(testConfig(t), journal)
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.PutWishlist("rare album", "type:audio")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.PutWishlist("rare album", "type:flac")
	if err != nil || updated.ID != item.ID || len(s.Wishlist()) != 1 {
		t.Fatalf("upsert: %+v %v", updated, err)
	}
	row, err := s.stateDB.Queries().GetWishlist(context.Background(), "w-1")
	if err != nil || row.Query != "rare album" {
		t.Fatalf("wishlist row: %+v %v", row, err)
	}
	_ = s.Close()

	reloaded, err := New(testConfig(t), journal)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	items := reloaded.Wishlist()
	if len(items) != 1 || items[0].ID != item.ID || items[0].Filter != "type:flac" {
		t.Fatalf("reloaded wishlist: %+v", items)
	}
	if err := reloaded.RemoveWishlist("missing"); !errors.Is(err, ErrWishlistNotFound) {
		t.Fatalf("unknown remove: %v", err)
	}
	if err := reloaded.RemoveWishlist(item.ID); err != nil || len(reloaded.Wishlist()) != 0 {
		t.Fatalf("remove: %v %+v", err, reloaded.Wishlist())
	}

}

func TestWishlistRunsFilterNotifyAndReplace(t *testing.T) {
	s := newWishlistService(t)
	item, err := s.PutWishlist("rare", "type:flac")
	if err != nil {
		t.Fatal(err)
	}
	s.client = &soulseek.Client{}
	results := []soulseek.SearchResult{
		{Username: "a", Path: "x.mp3", Size: 1, Extension: "mp3", Public: true},
		{Username: "b", Path: "x.flac", Size: 2, Extension: "flac", Public: true},
	}
	s.wishlistSearch = func(context.Context, *soulseek.Client, string, bool) ([]soulseek.SearchResult, error) {
		return append([]soulseek.SearchResult(nil), results...), nil
	}
	notifications := 0
	s.wishlistNotify = func(context.Context, string, int) error { notifications++; return nil }

	page, err := s.runWishlist(context.Background(), item.ID, true)
	if err != nil || page.Total != 1 || len(page.Results) != 1 {
		t.Fatalf("automatic page: %+v %v", page, err)
	}
	got := s.Wishlist()[0]
	if got.ResultCount != 1 || !got.Unread || got.NotificationSequence != 1 || notifications != 1 || page.ID != wishlistSearchID(item.ID) {
		t.Fatalf("automatic state: %+v notifications=%d", got, notifications)
	}
	persisted, err := s.stateDB.Queries().GetWishlist(context.Background(), item.ID)
	if err != nil || persisted.Unread != 1 || persisted.ResultSignature == "" {
		t.Fatalf("result metadata was not persisted: %+v %v", persisted, err)
	}
	if _, err := s.runWishlist(context.Background(), item.ID, true); err != nil || notifications != 1 {
		t.Fatalf("unchanged results renotified: %v %d", err, notifications)
	}
	if _, err := s.OpenWishlist(item.ID); err != nil || s.Wishlist()[0].Unread {
		t.Fatalf("open did not mark read: %v %+v", err, s.Wishlist()[0])
	}
	if _, err := s.runWishlist(context.Background(), item.ID, true); err != nil || s.Wishlist()[0].Unread || notifications != 1 {
		t.Fatalf("read unchanged results renotified: %v %+v", err, s.Wishlist()[0])
	}

	results[1].Path = "new.flac"
	if _, err := s.runWishlist(context.Background(), item.ID, true); err != nil || notifications != 2 || s.Wishlist()[0].NotificationSequence != 2 {
		t.Fatalf("changed results did not notify: %v %+v", err, s.Wishlist()[0])
	}
	results = nil
	if _, err := s.runWishlist(context.Background(), item.ID, true); err != nil || s.Wishlist()[0].Unread || s.Wishlist()[0].ResultCount != 0 {
		t.Fatalf("empty results did not clear: %v %+v", err, s.Wishlist()[0])
	}
	results = []soulseek.SearchResult{{Username: "c", Path: "manual.flac", Size: 3, Extension: "flac", Public: true}}
	if _, err := s.RunWishlist(context.Background(), item.ID); err != nil || s.Wishlist()[0].Unread || notifications != 2 {
		t.Fatalf("manual run notified: %v %+v", err, s.Wishlist()[0])
	}
	if len(s.searches) != 1 {
		t.Fatalf("automatic searches leaked cache entries: %d", len(s.searches))
	}

	s.wishlistSearch = func(context.Context, *soulseek.Client, string, bool) ([]soulseek.SearchResult, error) {
		return nil, errors.New("network failed")
	}
	if _, err := s.runWishlist(context.Background(), item.ID, true); err == nil || s.Wishlist()[0].ResultCount != 1 {
		t.Fatalf("failed run replaced results: %v %+v", err, s.Wishlist()[0])
	}
}

func TestWishlistIntervalRoundRobinAndStaleCompletion(t *testing.T) {
	s := newWishlistService(t)
	first, _ := s.PutWishlist("one", "")
	second, _ := s.PutWishlist("two", "")
	s.client = &soulseek.Client{}
	s.wishlistServerInterval = 30 * time.Minute
	if delay, ok := s.wishlistIntervalLocked(); !ok || delay != 30*time.Minute {
		t.Fatalf("server interval clamp: %v %v", delay, ok)
	}
	if s.nextWishlistID() != first.ID || s.nextWishlistID() != second.ID || s.nextWishlistID() != first.ID {
		t.Fatal("wishlist did not rotate in insertion order")
	}
	s.cfg.Search.WishlistIntervalMinutes = 0
	if _, ok := s.wishlistIntervalLocked(); ok {
		t.Fatal("disabled wishlist reported available")
	}
	s.cfg.Search.WishlistIntervalMinutes = 15
	s.client = nil
	if _, ok := s.wishlistIntervalLocked(); ok {
		t.Fatal("offline wishlist reported available")
	}
	s.client, s.wishlistServerInterval = &soulseek.Client{}, 0
	if _, ok := s.wishlistIntervalLocked(); ok {
		t.Fatal("wishlist ran before the server advertised an interval")
	}
	s.wishlistServerInterval = 30 * time.Minute

	started, release := make(chan struct{}), make(chan struct{})
	s.wishlistSearch = func(context.Context, *soulseek.Client, string, bool) ([]soulseek.SearchResult, error) {
		close(started)
		<-release
		return nil, nil
	}
	done := make(chan error, 1)
	go func() { _, err := s.runWishlist(context.Background(), first.ID, true); done <- err }()
	<-started
	if _, err := s.runWishlist(context.Background(), first.ID, true); err == nil {
		t.Fatal("second wishlist run started while one was in flight")
	}
	if _, err := s.PutWishlist("one", "type:audio"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrWishlistNotFound) || s.Wishlist()[0].Running {
		t.Fatalf("stale completion applied: %v %+v", err, s.Wishlist()[0])
	}
}
