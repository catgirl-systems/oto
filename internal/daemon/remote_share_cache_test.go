package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func remoteShareService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	service, err := New(testConfig(t), filepath.Join(dir, "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func TestBrowseProgressIgnoresStaleOperations(t *testing.T) {
	service := remoteShareService(t)
	firstKey, firstGeneration, firstUpdate := service.beginBrowseProgress("Peer")
	if progress := service.BrowseProgress("peer"); progress == nil || progress.Username != "Peer" {
		t.Fatalf("initial progress: %+v", progress)
	}
	firstUpdate(25, 100)
	secondKey, secondGeneration, secondUpdate := service.beginBrowseProgress("peer")
	firstUpdate(100, 100)
	service.finishBrowseProgress(firstKey, firstGeneration, false)
	if progress := service.BrowseProgress("peer"); progress == nil || progress.Received != 0 {
		t.Fatalf("stale operation changed replacement: %+v", progress)
	}
	secondUpdate(200, 200)
	service.finishBrowseProgress(secondKey, secondGeneration, true)
	if progress := service.BrowseProgress("peer"); progress == nil || progress.Received != 200 {
		t.Fatalf("completed progress: %+v", progress)
	}
}

func TestBrowseCompletePublishesProgress(t *testing.T) {
	service := remoteShareService(t)
	service.client = &soulseek.Client{}
	started := make(chan func(uint64, uint64), 1)
	release := make(chan struct{})
	service.fullBrowse = func(_ context.Context, _ *soulseek.Client, _ string, progress func(uint64, uint64)) ([]soulseek.ShareEntry, error) {
		started <- progress
		<-release
		progress(100, 100)
		return nil, nil
	}
	done := make(chan error, 1)
	go func() { _, err := service.BrowseComplete(context.Background(), "peer"); done <- err }()
	progressUpdate := <-started
	progressUpdate(50, 100)
	if progress := service.BrowseProgress("peer"); progress == nil || progress.Received != 50 {
		t.Fatalf("receiving progress: %+v", progress)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRemoteShareCacheRoundTripPreservesOrderAndDuplicates(t *testing.T) {
	service := remoteShareService(t)
	entries := []soulseek.ShareEntry{
		{Name: `Music`, Directory: true, Private: true},
		{Name: `Music\song.flac`, Size: ^uint64(0), Private: true, Extension: "flac", Bitrate: 320, Duration: 125, VBR: true, VBRKnown: true, SampleRate: 44100, BitDepth: 24},
		{Name: `Music\song.flac`, Size: 42, Extension: "flac"},
	}
	service.client = &soulseek.Client{}
	service.fullBrowse = func(context.Context, *soulseek.Client, string, func(uint64, uint64)) ([]soulseek.ShareEntry, error) {
		return entries, nil
	}
	live, err := service.BrowseComplete(context.Background(), "Alice")
	if err != nil || live.Cached || !reflect.DeepEqual(live.Entries, entries) {
		t.Fatalf("live browse: %+v %v", live, err)
	}
	saved, err := service.SaveBrowse("alice", live.Revision)
	if err != nil || saved.Username != "alice" || saved.SavedAt.IsZero() {
		t.Fatalf("save browse: %+v %v", saved, err)
	}
	browses, err := service.SavedBrowses()
	if err != nil || len(browses) != 1 || browses[0].Username != "Alice" || !browses[0].SavedAt.Equal(saved.SavedAt) {
		t.Fatalf("saved browses: %+v %v", browses, err)
	}
	path := service.journalPath
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	fresh, err := New(testConfig(t), path)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	cached, err := fresh.BrowseComplete(context.Background(), "aLiCe")
	if err != nil || !cached.Cached || !cached.SavedAt.Equal(saved.SavedAt) || !reflect.DeepEqual(cached.Entries, entries) {
		t.Fatalf("cached browse: %+v %v", cached, err)
	}
}

func TestRemoteShareCacheFallbackAndStaleRevision(t *testing.T) {
	service := remoteShareService(t)
	oldEntries := []soulseek.ShareEntry{{Name: `Old\song.mp3`, Size: 1}}
	old := service.rememberBrowse("peer", oldEntries, false, time.Time{})
	if _, err := service.SaveBrowse("peer", old.Revision); err != nil {
		t.Fatal(err)
	}
	service.client = &soulseek.Client{}
	remoteErr := errors.New("peer unavailable")
	service.fullBrowse = func(context.Context, *soulseek.Client, string, func(uint64, uint64)) ([]soulseek.ShareEntry, error) {
		return nil, remoteErr
	}
	fallback, err := service.BrowseComplete(context.Background(), "peer")
	if err != nil || !fallback.Cached || !reflect.DeepEqual(fallback.Entries, oldEntries) {
		t.Fatalf("fallback: %+v %v", fallback, err)
	}
	newEntries := []soulseek.ShareEntry{{Name: `New\song.mp3`, Size: 2}}
	service.fullBrowse = func(context.Context, *soulseek.Client, string, func(uint64, uint64)) ([]soulseek.ShareEntry, error) {
		return newEntries, nil
	}
	live, err := service.BrowseComplete(context.Background(), "peer")
	if err != nil || live.Cached || !reflect.DeepEqual(live.Entries, newEntries) {
		t.Fatalf("refresh: %+v %v", live, err)
	}
	if _, err := service.SaveBrowse("peer", fallback.Revision); !errors.Is(err, ErrBrowseRevision) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	if _, err := service.SaveBrowse("peer", live.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteShareCacheMissingIsNotCreated(t *testing.T) {
	service := remoteShareService(t)
	if _, err := service.SaveBrowse("missing", 1); !errors.Is(err, ErrBrowseNotLoaded) {
		t.Fatalf("missing browse: %v", err)
	}
	if _, err := service.BrowseComplete(context.Background(), "missing"); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("missing cache: %v", err)
	}
	if saved, err := service.SavedBrowses(); err != nil || len(saved) != 0 {
		t.Fatalf("unexpected saved browse: %+v %v", saved, err)
	}
}
