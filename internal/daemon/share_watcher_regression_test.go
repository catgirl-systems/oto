package daemon

import (
	"context"
	"errors"
	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestShareWatcherReindexesFilesAndDirectories(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "Existing")
	must(t, os.Mkdir(nested, 0700))
	service := watchingService(t, []config.Share{{Name: "Music", Path: root}}, 30*time.Millisecond, nil)

	existing := filepath.Join(nested, "existing.flac")
	must(t, os.WriteFile(existing, []byte("old"), 0600))
	waitFor(t, func() bool { return hasLocalFile(service, "Music/Existing", "existing.flac", 3) })
	must(t, os.WriteFile(existing, []byte("changed"), 0600))
	waitFor(t, func() bool { return hasLocalFile(service, "Music/Existing", "existing.flac", 7) })

	renamed := filepath.Join(nested, "renamed.flac")
	must(t, os.Rename(existing, renamed))
	waitFor(t, func() bool {
		return !hasLocalFile(service, "Music/Existing", "existing.flac", -1) && hasLocalFile(service, "Music/Existing", "renamed.flac", 7)
	})
	must(t, os.Remove(renamed))
	waitFor(t, func() bool { return !hasLocalFile(service, "Music/Existing", "renamed.flac", -1) })

	created := filepath.Join(root, "Created", "Disc")
	must(t, os.MkdirAll(created, 0700))
	must(t, os.WriteFile(filepath.Join(created, "new.flac"), []byte("new"), 0600))
	waitFor(t, func() bool { return hasLocalFile(service, "Music/Created/Disc", "new.flac", 3) })
}

func TestShareWatcherDebouncesEventBursts(t *testing.T) {
	root := t.TempDir()
	var scans atomic.Int32
	builder := func(ctx context.Context, shares []config.Share) (*soulseek.ShareIndex, error) {
		scans.Add(1)
		return buildShareIndex(ctx, shares)
	}
	service := watchingService(t, []config.Share{{Name: "Music", Path: root}}, 100*time.Millisecond, builder)
	waitFor(t, func() bool { return scans.Load() == 1 })

	for i := 0; i < 3; i++ {
		must(t, os.WriteFile(filepath.Join(root, string(rune('a'+i))+".flac"), []byte("x"), 0600))
		time.Sleep(40 * time.Millisecond)
	}
	if got := scans.Load(); got != 1 {
		t.Fatalf("scan started before quiet period: %d", got)
	}
	waitFor(t, func() bool { return scans.Load() == 2 })
	time.Sleep(150 * time.Millisecond)
	if got := scans.Load(); got != 2 {
		t.Fatalf("event burst produced %d scans", got-1)
	}
	failIf(t, !hasLocalFile(service, "Music", "c.flac", 1), "debounced scan was not published")
}

func TestShareWatcherDiscardsScanDirtiedWhileRunning(t *testing.T) {
	root := t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, "old.flac"), []byte("old"), 0600))
	started := make(chan struct{})
	release := make(chan struct{})
	var scans atomic.Int32
	builder := func(ctx context.Context, shares []config.Share) (*soulseek.ShareIndex, error) {
		if scans.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return buildShareIndex(ctx, shares)
	}
	service := watchingService(t, []config.Share{{Name: "Music", Path: root}}, 150*time.Millisecond, builder)
	<-started
	must(t, os.WriteFile(filepath.Join(root, "during.flac"), []byte("new"), 0600))
	time.Sleep(50 * time.Millisecond)
	close(release)
	time.Sleep(50 * time.Millisecond)
	failIf(t, hasLocalFile(service, "Music", "during.flac", -1), "dirty shadow scan replaced the old index")
	waitFor(t, func() bool { return scans.Load() >= 2 && hasLocalFile(service, "Music", "during.flac", 3) })
}

func TestShareWatcherFailedScanPreservesIndexAndRetries(t *testing.T) {
	root := t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, "old.flac"), []byte("old"), 0600))
	var scans atomic.Int32
	builder := func(ctx context.Context, shares []config.Share) (*soulseek.ShareIndex, error) {
		if scans.Add(1) == 1 {
			return nil, errors.New("scan failed")
		}
		return buildShareIndex(ctx, shares)
	}
	service := watchingService(t, []config.Share{{Name: "Music", Path: root}}, 30*time.Millisecond, builder)
	failIf(t, !hasLocalFile(service, "Music", "old.flac", 3), "initial index missing")
	waitFor(t, func() bool { return scans.Load() >= 2 })
	failIf(t, !hasLocalFile(service, "Music", "old.flac", 3), "failed scan replaced the last good index")
}

func TestShareWatcherSkipsHiddenDirectoriesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	var scans atomic.Int32
	builder := func(ctx context.Context, shares []config.Share) (*soulseek.ShareIndex, error) {
		scans.Add(1)
		return buildShareIndex(ctx, shares)
	}
	service := watchingService(t, []config.Share{{Name: "Music", Path: root}}, 30*time.Millisecond, builder)
	waitFor(t, func() bool { return scans.Load() == 1 })
	must(t, os.Mkdir(filepath.Join(root, ".hidden"), 0700))
	hiddenFile := filepath.Join(root, ".hidden", "secret.flac")
	must(t, os.WriteFile(hiddenFile, []byte("x"), 0600))
	outsideFile := filepath.Join(outside, "outside.flac")
	must(t, os.WriteFile(outsideFile, []byte("x"), 0600))
	must(t, os.Symlink(outside, filepath.Join(root, "linked")))
	// Clearly hidden and symlink events no longer need a scan.
	time.Sleep(75 * time.Millisecond)
	failIf(t, scans.Load() != 1, "excluded creation caused a scan")
	if _, err := service.BrowseLocal("Music/.hidden"); err == nil {
		t.Fatal("hidden directory was indexed")
	}
	if _, err := service.BrowseLocal("Music/linked"); err == nil {
		t.Fatal("symlinked directory was indexed")
	}
	settled := scans.Load()
	must(t, os.WriteFile(hiddenFile, []byte("changed"), 0600))
	must(t, os.WriteFile(outsideFile, []byte("changed"), 0600))
	time.Sleep(100 * time.Millisecond)
	failIf(t, scans.Load() != settled, "hidden or symlinked directory was watched")
}

func TestShareWatcherCanBeDisabledAndManuallyRescanned(t *testing.T) {
	root := t.TempDir()
	service := watchingService(t, []config.Share{{Name: "Music", Path: root}}, 0, nil)
	must(t, os.WriteFile(filepath.Join(root, "manual.flac"), []byte("x"), 0600))
	time.Sleep(75 * time.Millisecond)
	failIf(t, hasLocalFile(service, "Music", "manual.flac", -1), "disabled watcher reindexed a filesystem event")
	if err := service.Rescan(); err != nil || !hasLocalFile(service, "Music", "manual.flac", 1) {
		t.Fatalf("manual rescan failed: %v", err)
	}
}

func TestShareWatcherRootChangeInvalidatesOldScan(t *testing.T) {
	oldRoot, newRoot := t.TempDir(), t.TempDir()
	must(t, os.WriteFile(filepath.Join(oldRoot, "old.flac"), []byte("old"), 0600))
	must(t, os.WriteFile(filepath.Join(newRoot, "new.flac"), []byte("new"), 0600))
	started := make(chan struct{})
	var scans atomic.Int32
	builder := func(ctx context.Context, shares []config.Share) (*soulseek.ShareIndex, error) {
		if len(shares) == 1 && scans.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return buildShareIndex(ctx, shares)
	}
	service := watchingService(t, []config.Share{{Name: "Old", Path: oldRoot}}, 30*time.Millisecond, builder)
	<-started
	must(t, service.AddShare(config.Share{Name: "New", Path: newRoot}))
	waitFor(t, func() bool { return hasLocalFile(service, "New", "new.flac", 3) })
	time.Sleep(100 * time.Millisecond)
	failIf(t, !hasLocalFile(service, "New", "new.flac", 3), "obsolete scan replaced the new-root index")
	must(t, service.RemoveShare("Old"))
	if _, err := service.BrowseLocal("Old"); err == nil || !hasLocalFile(service, "New", "new.flac", 3) {
		t.Fatalf("removed root remained or current root disappeared: %v", err)
	}
}
