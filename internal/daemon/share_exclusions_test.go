package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
	must(t, os.WriteFile(filepath.Join(root, "song.tmp"), []byte("x"), 0600))
	must(t, s.AddShare(config.Share{Name: "Music", Path: root}))
	failIf(t, hasLocalFile(s, "Music", "song.tmp", 1), "default not applied")
	cfg := s.cfg
	client := soulseek.NewClient(soulseek.ClientConfig{Uploads: newUploadManager(cfg)})
	s.client = client
	cfg.Bandwidth = config.Bandwidth{ActiveProfile: "Both", Profiles: []config.BandwidthProfile{{Name: "Both", UploadSpeedLimitKiB: 7, DownloadSpeedLimitKiB: 11}}}
	cfg.ShareExclusions = []string{}
	// An exclusion-only update must not tear down a running session.
	s.cancel = func() { t.Error("exclusions reconnected Soulseek") }
	must(t, s.UpdateConfig(cfg))
	s.cancel = nil
	failIf(t, client.DownloadLimit() != 11*1024 || client.UploadPolicy().BytesPerSecond != 7*1024, "share-scan publication lost bandwidth update")
	failIf(t, !hasLocalFile(s, "Music", "song.tmp", 1) || s.Snapshot().Config.ShareExclusions == nil, "empty policy was not published")
	loaded, err := config.Load(s.configPath)
	failIfFmt(t, err != nil || loaded.ShareExclusions == nil || len(loaded.ShareExclusions) != 0, "empty policy not persisted: %+v %v", loaded.ShareExclusions, err)
	if _, err := s.loadShareIndexCache(cfg.Shares, nil); err == nil {
		t.Fatal("differently filtered cache accepted")
	}
	cache, err := s.loadShareIndexCache(cfg.Shares, []string{})
	must(t, err)
	index := s.shares
	disk, _ := os.ReadFile(s.configPath)
	for _, failure := range []string{"validation", "scan", "save", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			next := cfg
			next.Bandwidth = config.Bandwidth{ActiveProfile: "Other", Profiles: []config.BandwidthProfile{{Name: "Other", UploadSpeedLimitKiB: 99, DownloadSpeedLimitKiB: 99}}}
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
				must(t, s.CancelShareScan(s.Snapshot().ShareScan.ID))
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
			failIf(t, s.Config().Bandwidth.ActiveProfile != "Both" || client.DownloadLimit() != 11*1024 || client.UploadPolicy().BytesPerSecond != 7*1024, "failed staged scan changed bandwidth")
			gotCache, err := s.loadShareIndexCache(s.cfg.Shares, s.cfg.ShareExclusions)
			failIf(t, err != nil || s.shares != index || !slices.Equal(s.cfg.ShareExclusions, cfg.ShareExclusions) || !bytes.Equal(disk, gotDisk) || !reflect.DeepEqual(cache.Files(), gotCache.Files()), "failed edit changed active or persisted configuration/index")
		})
	}
}

func TestExclusionWatcherPrunesAndIgnoresEvents(t *testing.T) {
	root := t.TempDir()
	must(t, os.Mkdir(filepath.Join(root, "@eaDir"), 0700))
	var scans atomic.Int32
	s := watchingService(t, []config.Share{{Name: "Music", Path: root}}, 20*time.Millisecond, func(ctx context.Context, roots []config.Share) (*soulseek.ShareIndex, error) {
		scans.Add(1)
		return buildShareIndex(ctx, roots)
	})
	waitFor(t, func() bool { return s.Snapshot().ShareScan.State == "completed" && scans.Load() == 1 })
	for _, file := range []string{"@eaDir/metadata", "song.tmp"} {
		must(t, os.WriteFile(filepath.Join(root, file), []byte("x"), 0600))
	}
	time.Sleep(80 * time.Millisecond)
	failIf(t, scans.Load() != 1, "excluded paths caused a scan")
	must(t, os.Mkdir(filepath.Join(root, "lost+found"), 0700))
	time.Sleep(60 * time.Millisecond)
	failIf(t, scans.Load() != 1, "excluded directory installed watches")
	must(t, os.WriteFile(filepath.Join(root, "song.flac"), []byte("x"), 0600))
	waitFor(t, func() bool { return hasLocalFile(s, "Music", "song.flac", 1) })
	failIf(t, hasLocalFile(s, "Music", "song.tmp", 1), "watcher ignored policy")
}
