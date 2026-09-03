package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func remoteShareService(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	service, err := New(testConfig(t), filepath.Join(dir, "downloads.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service, dir
}

func TestRemoteShareCacheIsExplicitAndRoundTrips(t *testing.T) {
	service, dir := remoteShareService(t)
	entries := []soulseek.ShareEntry{
		{Name: `Music`, Directory: true},
		{Name: `Music\song.flac`, Size: 42, Private: true, Extension: "flac", Bitrate: 320, Duration: 125, VBR: true, SampleRate: 44100, BitDepth: 24},
	}
	service.client = &soulseek.Client{}
	service.fullBrowse = func(context.Context, *soulseek.Client, string) ([]soulseek.ShareEntry, error) { return entries, nil }

	live, err := service.BrowseComplete(context.Background(), "Alice")
	if err != nil || live.Cached || live.Revision == 0 || !reflect.DeepEqual(live.Entries, entries) {
		t.Fatalf("live browse: %+v %v", live, err)
	}
	if saved, err := service.SavedBrowses(); err != nil || len(saved) != 0 {
		t.Fatalf("browse persisted without save: %+v %v", saved, err)
	}
	if _, err := service.SaveBrowse("alice", live.Revision+1); !errors.Is(err, ErrBrowseRevision) {
		t.Fatalf("stale revision: %v", err)
	}
	saved, err := service.SaveBrowse("ALICE", live.Revision)
	if err != nil || saved.Username != "alice" || saved.SavedAt.IsZero() {
		t.Fatalf("save browse: %+v %v", saved, err)
	}

	cachePath, err := remoteShareCachePath(filepath.Join(dir, "usershares"), "../../ALICE")
	if err != nil || filepath.Dir(cachePath) != filepath.Join(dir, "usershares") {
		t.Fatalf("unsafe cache path %q: %v", cachePath, err)
	}
	actualPath, _ := remoteShareCachePath(filepath.Join(dir, "usershares"), "alice")
	if info, err := os.Stat(actualPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("cache mode: %v %v", info, err)
	}
	if info, err := os.Stat(filepath.Dir(actualPath)); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("cache directory mode: %v %v", info, err)
	}

	fresh, err := New(testConfig(t), filepath.Join(dir, "downloads.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	cached, err := fresh.BrowseComplete(context.Background(), "aLiCe")
	if err != nil || !cached.Cached || cached.SavedAt.IsZero() || !reflect.DeepEqual(cached.Entries, entries) {
		t.Fatalf("cached browse: %+v %v", cached, err)
	}
}

func TestRemoteShareCacheFallbackAndOverwrite(t *testing.T) {
	service, dir := remoteShareService(t)
	oldEntries := []soulseek.ShareEntry{{Name: `Old\song.mp3`, Size: 1}}
	old := service.rememberBrowse("peer", oldEntries, false, time.Time{})
	if _, err := service.SaveBrowse("peer", old.Revision); err != nil {
		t.Fatal(err)
	}

	service.client = &soulseek.Client{}
	remoteErr := errors.New("peer unavailable")
	service.fullBrowse = func(context.Context, *soulseek.Client, string) ([]soulseek.ShareEntry, error) { return nil, remoteErr }
	fallback, err := service.BrowseComplete(context.Background(), "peer")
	if err != nil || !fallback.Cached || !reflect.DeepEqual(fallback.Entries, oldEntries) {
		t.Fatalf("fallback: %+v %v", fallback, err)
	}

	newEntries := []soulseek.ShareEntry{{Name: `New\song.mp3`, Size: 2}}
	service.fullBrowse = func(context.Context, *soulseek.Client, string) ([]soulseek.ShareEntry, error) { return newEntries, nil }
	live, err := service.BrowseComplete(context.Background(), "peer")
	if err != nil || live.Cached || !reflect.DeepEqual(live.Entries, newEntries) {
		t.Fatalf("refresh: %+v %v", live, err)
	}
	if _, err := service.SaveBrowse("peer", fallback.Revision); !errors.Is(err, ErrBrowseRevision) {
		t.Fatalf("replaced revision accepted: %v", err)
	}
	if _, err := service.SaveBrowse("peer", live.Revision); err != nil {
		t.Fatal(err)
	}

	fresh, err := New(testConfig(t), filepath.Join(dir, "downloads.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	cached, err := fresh.BrowseComplete(context.Background(), "peer")
	if err != nil || !reflect.DeepEqual(cached.Entries, newEntries) {
		t.Fatalf("overwritten cache: %+v %v", cached, err)
	}
}

func TestRemoteShareCacheRejectsInvalidFilesAndListsWithoutParsing(t *testing.T) {
	service, dir := remoteShareService(t)
	if _, err := service.SaveBrowse("missing", 1); !errors.Is(err, ErrBrowseNotLoaded) {
		t.Fatalf("missing browse: %v", err)
	}
	if _, err := service.BrowseComplete(context.Background(), "missing"); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("missing cache: %v", err)
	}

	cacheDir := filepath.Join(dir, "usershares")
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	corruptPath, _ := remoteShareCachePath(cacheDir, "broken")
	if err := os.WriteFile(corruptPath, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BrowseComplete(context.Background(), "broken"); err == nil || !strings.Contains(err.Error(), "load saved share list") {
		t.Fatalf("corrupt cache accepted: %v", err)
	}
	wrongPath, _ := remoteShareCachePath(cacheDir, "wrong")
	if err := config.SaveJSON(wrongPath, remoteShareCache{Version: remoteShareCacheVersion, Username: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BrowseComplete(context.Background(), "wrong"); err == nil || !strings.Contains(err.Error(), "username mismatch") {
		t.Fatalf("mismatched cache accepted: %v", err)
	}
	versionPath, _ := remoteShareCachePath(cacheDir, "future")
	if err := config.SaveJSON(versionPath, remoteShareCache{Version: remoteShareCacheVersion + 1, Username: "future"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BrowseComplete(context.Background(), "future"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("future cache accepted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "not-a-cache"), nil, 0600); err != nil {
		t.Fatal(err)
	}

	saved, err := service.SavedBrowses()
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(saved))
	for i := range saved {
		got[i] = saved[i].Username
		if saved[i].SavedAt.IsZero() {
			t.Fatalf("missing timestamp: %+v", saved[i])
		}
	}
	want := []string{"broken", "future", "wrong"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("saved users = %v, want %v", got, want)
	}
}
