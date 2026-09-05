package daemon

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage"
)

func watchingService(t *testing.T, shares []config.Share, quiet time.Duration, builder func(context.Context, []config.Share) (*soulseek.ShareIndex, error)) *Service {
	t.Helper()
	cfg := testConfig(t)
	cfg.Shares = shares
	service, err := New(cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	service.SetConfigPath(filepath.Join(t.TempDir(), "config.json"))
	if err := service.Rescan(); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	if builder != nil {
		service.shareIndexBuilder = builder
	}
	service.shareRescanDelay = quiet
	service.runCtx, service.runCancel = context.WithCancel(context.Background())
	service.restartShareWatcherLocked()
	service.mu.Unlock()
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func hasLocalFile(service *Service, virtual, name string, size int64) bool {
	entries, err := service.BrowseLocal(virtual)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.Name == name && !entry.Directory && (size < 0 || entry.Size == uint64(size)) {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not satisfied")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func shareStorageService(t *testing.T) (*Service, string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "song.flac"), []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	cfg.Shares = []config.Share{{Name: "Music", Path: root}}
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	service, err := New(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service, root, path
}

func TestShareIndexSQLiteRoundTripPreservesFields(t *testing.T) {
	service, root, _ := shareStorageService(t)
	index, err := soulseek.RestoreShareIndexWithExclusions(
		[]soulseek.ShareRoot{{Name: "Music", Path: root}},
		[]soulseek.ShareFile{{Root: "Music", Path: "song.flac", Size: ^uint64(0), AudioMetadata: soulseek.AudioMetadata{Bitrate: 320, Duration: 123, SampleRate: 96000, BitDepth: 24}, AudioFingerprint: soulseek.AudioFingerprint{Size: ^uint64(0), MTimeUnixNano: 11, CTimeUnixNano: 22, ExtractorVersion: "probe"}, AudioSource: "ffprobe"}},
		[]string{"tmp/*", "*.bak"},
	)
	if err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	service.shares = index
	service.mu.Unlock()
	service.persistShareIndex(index)
	loaded, err := service.loadShareIndexCache(service.cfg.Shares, index.Exclusions())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Files(), index.Files()) || !reflect.DeepEqual(loaded.Exclusions(), index.Exclusions()) {
		t.Fatalf("round trip changed index: %#v %#v", loaded.Files(), loaded.Exclusions())
	}
}

func TestShareSnapshotStagingIsInvisibleAndCancellationKeepsHead(t *testing.T) {
	service, root, _ := shareStorageService(t)
	old, err := buildShareIndex(context.Background(), service.cfg.Shares)
	if err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	service.shares = old
	service.mu.Unlock()
	service.persistShareIndex(old)
	before, err := service.loadShareIndexCache(service.cfg.Shares, nil)
	if err != nil {
		t.Fatal(err)
	}
	newIndex, err := soulseek.RestoreShareIndex([]soulseek.ShareRoot{{Name: "Music", Path: root}}, []soulseek.ShareFile{{Root: "Music", Path: "new.flac", Size: 1}})
	if err != nil {
		t.Fatal(err)
	}
	service.shareStorageMu.Lock()
	staged, err := stageShareSnapshot(context.Background(), service.stateDB, "local", "", newIndex.Roots(), nil, newIndex.Files(), nil)
	if err != nil {
		service.shareStorageMu.Unlock()
		t.Fatal(err)
	}
	loaded, err := service.loadShareIndexCache(service.cfg.Shares, nil)
	if err != nil || !reflect.DeepEqual(loaded.Files(), before.Files()) {
		service.shareStorageMu.Unlock()
		t.Fatalf("staging leaked through head: %v", err)
	}
	if err := deleteShareSnapshotRows(context.Background(), service.stateDB, staged); err != nil {
		service.shareStorageMu.Unlock()
		t.Fatal(err)
	}
	service.shareStorageMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := stageShareSnapshot(ctx, service.stateDB, "local", "", newIndex.Roots(), nil, newIndex.Files(), nil); err == nil {
		t.Fatal("canceled staging succeeded")
	}
	loaded, err = service.loadShareIndexCache(service.cfg.Shares, nil)
	if err != nil || !reflect.DeepEqual(loaded.Files(), before.Files()) {
		t.Fatalf("canceled staging changed head: %v", err)
	}
}

func TestShareGCDeletesChildrenInBoundedBatches(t *testing.T) {
	service, root, _ := shareStorageService(t)
	files := make([]soulseek.ShareFile, storage.ShareBatchSize+1)
	for i := range files {
		files[i] = soulseek.ShareFile{Root: "Music", Path: filepath.Join("dir", string(rune('a'+i%26))+".mp3"), Size: uint64(i)}
	}
	// Duplicate paths are invalid for a restored index, so only exercise the
	// staging/GC path with unique virtual names.
	for i := range files {
		files[i].Path = filepath.ToSlash(filepath.Join("dir", "file-"+itoa(i)+".mp3"))
	}
	service.shareStorageMu.Lock()
	id, err := stageShareSnapshot(context.Background(), service.stateDB, "local", "", []soulseek.ShareRoot{{Name: "Music", Path: root}}, nil, files, nil)
	if err != nil {
		service.shareStorageMu.Unlock()
		t.Fatal(err)
	}
	if err := deleteShareSnapshotRows(context.Background(), service.stateDB, id); err != nil {
		service.shareStorageMu.Unlock()
		t.Fatal(err)
	}
	service.shareStorageMu.Unlock()
	if _, err := service.stateDB.Queries().GetShareSnapshot(context.Background(), id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("staging snapshot remained: %v", err)
	}
}

func itoa(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var out [20]byte
	i := len(out)
	for n > 0 {
		i--
		out[i] = digits[n%10]
		n /= 10
	}
	return string(out[i:])
}
