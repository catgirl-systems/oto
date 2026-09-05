package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestExclusionSettingsPublishAndRollback(t *testing.T) {
	s := downloadService(t)
	s.SetConfigPath(filepath.Join(t.TempDir(), "config.json"))
	s.shareRescanDelay = 0
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "song.tmp"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.AddShare(config.Share{Name: "Music", Path: root}); err != nil {
		t.Fatal(err)
	}
	if hasLocalFile(s, "Music", "song.tmp", 1) {
		t.Fatal("default not applied")
	}
	cfg := s.cfg
	cfg.ShareExclusions = []string{}
	// An exclusion-only update must not tear down a running session.
	s.cancel = func() { t.Error("exclusions reconnected Soulseek") }
	if err := s.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	s.cancel = nil
	if !hasLocalFile(s, "Music", "song.tmp", 1) || s.Snapshot().Config.ShareExclusions == nil {
		t.Fatal("empty policy was not published")
	}
	loaded, err := config.Load(s.configPath)
	if err != nil || loaded.ShareExclusions == nil || len(loaded.ShareExclusions) != 0 {
		t.Fatalf("empty policy not persisted: %+v %v", loaded.ShareExclusions, err)
	}
	if _, err := loadShareIndexCache(s.shareIndexPath, cfg.Shares, nil); err == nil {
		t.Fatal("differently filtered cache accepted")
	}
	if _, err := loadShareIndexCache(s.shareIndexPath, cfg.Shares, []string{}); err != nil {
		t.Fatal(err)
	}
	index := s.shares
	cache, _ := os.ReadFile(s.shareIndexPath)
	disk, _ := os.ReadFile(s.configPath)
	for _, failure := range []string{"validation", "scan", "save", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			next := cfg
			next.ShareExclusions = []string{"*.tmp"}
			path := s.configPath
			defer func() { s.shareIndexBuilder = nil; s.configPath = path }()
			switch failure {
			case "validation":
				next.ShareExclusions = []string{"../bad"}
			case "save":
				s.configPath = t.TempDir()
			case "scan":
				s.shareIndexBuilder = func(context.Context, []config.Share) (*soulseek.ShareIndex, error) {
					return nil, errors.New("failed scan")
				}
			case "cancel":
				entered := make(chan struct{})
				s.shareIndexBuilder = func(ctx context.Context, _ []config.Share) (*soulseek.ShareIndex, error) {
					close(entered)
					<-ctx.Done()
					return nil, ctx.Err()
				}
				done := make(chan error, 1)
				go func() { done <- s.UpdateConfig(next) }()
				<-entered
				if err := s.CancelShareScan(s.Snapshot().ShareScan.ID); err != nil {
					t.Fatal(err)
				}
				if err := <-done; !errors.Is(err, ErrScanCancelled) {
					t.Fatalf("cancel: %v", err)
				}
			}
			if failure != "cancel" {
				if err := s.UpdateConfig(next); err == nil {
					t.Fatal("failure accepted")
				}
			}
			gotDisk, _ := os.ReadFile(path)
			gotCache, _ := os.ReadFile(s.shareIndexPath)
			if s.shares != index || !slices.Equal(s.cfg.ShareExclusions, cfg.ShareExclusions) || !bytes.Equal(disk, gotDisk) || !bytes.Equal(cache, gotCache) {
				t.Fatal("failed edit changed active or persisted configuration/index")
			}
		})
	}
}

func TestExclusionWatcherPrunesAndIgnoresEvents(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "@eaDir"), 0700); err != nil {
		t.Fatal(err)
	}
	var scans atomic.Int32
	s := watchingService(t, []config.Share{{Name: "Music", Path: root}}, 20*time.Millisecond, func(ctx context.Context, roots []config.Share) (*soulseek.ShareIndex, error) {
		scans.Add(1)
		return buildShareIndex(ctx, roots)
	})
	waitFor(t, func() bool { return s.Snapshot().ShareScan.State == "completed" && scans.Load() == 1 })
	for _, file := range []string{"@eaDir/metadata", "song.tmp"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(80 * time.Millisecond)
	if scans.Load() != 1 {
		t.Fatal("excluded paths caused a scan")
	}
	if err := os.Mkdir(filepath.Join(root, "lost+found"), 0700); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if scans.Load() != 1 {
		t.Fatal("excluded directory installed watches")
	}
	if err := os.WriteFile(filepath.Join(root, "song.flac"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return hasLocalFile(s, "Music", "song.flac", 1) })
	if hasLocalFile(s, "Music", "song.tmp", 1) {
		t.Fatal("watcher ignored policy")
	}
}
