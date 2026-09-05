package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestCancelScanOwners(t *testing.T) {
	for _, kind := range []string{"startup", "manual", "settings"} {
		t.Run(kind, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Soulseek.ConnectOnStartup = false
			s, err := New(cfg, filepath.Join(t.TempDir(), "journal.json"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			s.SetConfigPath(filepath.Join(t.TempDir(), "config.json"))
			s.shareRescanDelay = 0
			if err := cfg.Save(s.configPath); err != nil {
				t.Fatal(err)
			}
			if kind != "startup" {
				if err := s.Rescan(); err != nil {
					t.Fatal(err)
				}
			}
			before := s.Snapshot()
			cache, _ := os.ReadFile(s.shareIndexPath)
			configuration, _ := os.ReadFile(s.configPath)
			entered, release := make(chan struct{}), make(chan struct{})
			s.shareIndexBuilder = func(ctx context.Context, _ []config.Share) (*soulseek.ShareIndex, error) {
				close(entered)
				<-ctx.Done()
				<-release                            // emulate a filesystem call that is not immediately interruptible
				return soulseek.NewShareIndex(), nil // even a builder ignoring cancellation cannot publish
			}
			done := make(chan error, 1)
			go func() {
				switch kind {
				case "startup":
					done <- s.Start(context.Background())
				case "settings":
					next := cfg
					next.DownloadSlots++
					done <- s.UpdateConfig(next)
				default:
					done <- s.Rescan()
				}
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("scan did not start")
			}
			id := s.Snapshot().ShareScan.ID
			if err := s.CancelShareScan(id + 1); !errors.Is(err, ErrScanConflict) {
				t.Fatalf("stale: %v", err)
			}
			if err := s.CancelShareScan(id); err != nil {
				t.Fatal(err)
			}
			if err := s.CancelShareScan(id); err != nil {
				t.Fatalf("repeat: %v", err)
			}
			if s.Snapshot().ShareScan.State != "cancelling" {
				t.Fatal("not cancelling")
			}
			close(release)
			select {
			case err := <-done:
				if kind == "startup" && err != nil || kind != "startup" && !errors.Is(err, ErrScanCancelled) {
					t.Fatalf("result: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation blocked on lifecycle lock")
			}
			after := s.Snapshot()
			if after.ShareScan.State != "cancelled" || after.ShareScan.Error != "" || before.ShareIndexRevision != after.ShareIndexRevision || s.cfg.DownloadSlots != cfg.DownloadSlots {
				t.Fatalf("cancelled scan changed state: %+v", after.ShareScan)
			}
			gotCache, _ := os.ReadFile(s.shareIndexPath)
			gotConfig, _ := os.ReadFile(s.configPath)
			if !bytes.Equal(cache, gotCache) || !bytes.Equal(configuration, gotConfig) {
				t.Fatal("cancel changed disk state")
			}
			s.shareIndexBuilder = buildShareIndex
			if err := s.Rescan(); err != nil {
				t.Fatalf("subsequent scan: %v", err)
			}
		})
	}
}

func TestScanPublicationCannotBeCancelled(t *testing.T) {
	s, err := New(testConfig(t), filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.runShareScan(context.Background(), nil, nil, 0, true, nil, func(*soulseek.ShareIndex) error { close(entered); <-release; return nil })
	}()
	<-entered
	if err := s.CancelShareScan(s.Snapshot().ShareScan.ID); !errors.Is(err, ErrScanConflict) {
		t.Fatalf("publication cancellation: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCancelledWatcherWaitsForNewChanges(t *testing.T) {
	root := t.TempDir()
	var scans atomic.Int32
	entered := make(chan struct{})
	s := watchingService(t, []config.Share{{Name: "Music", Path: root}}, 25*time.Millisecond, func(ctx context.Context, roots []config.Share) (*soulseek.ShareIndex, error) {
		if scans.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return buildShareIndex(ctx, roots)
	})
	<-entered
	if err := s.CancelShareScan(s.Snapshot().ShareScan.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return s.Snapshot().ShareScan.State == "cancelled" })
	time.Sleep(100 * time.Millisecond)
	if scans.Load() != 1 {
		t.Fatal("cancelled watcher immediately retried")
	}
	if err := os.WriteFile(filepath.Join(root, "new.flac"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return hasLocalFile(s, "Music", "new.flac", 1) })
}

func TestCancelPublicationArbitration(t *testing.T) {
	s := downloadService(t)
	for range 100 {
		entered, release := make(chan struct{}), make(chan struct{})
		var published atomic.Bool
		builder := func(context.Context, []config.Share) (*soulseek.ShareIndex, error) {
			close(entered)
			<-release
			return soulseek.NewShareIndex(), nil
		}
		done, cancelled := make(chan error, 1), make(chan error, 1)
		go func() {
			done <- s.runShareScan(context.Background(), nil, nil, 0, true, builder, func(*soulseek.ShareIndex) error { published.Store(true); return nil })
		}()
		<-entered
		id := s.Snapshot().ShareScan.ID
		go func() { <-release; cancelled <- s.CancelShareScan(id) }()
		close(release)
		scanErr, cancelErr := <-done, <-cancelled
		if cancelErr == nil {
			if published.Load() || !errors.Is(scanErr, ErrScanCancelled) {
				t.Fatalf("accepted cancellation published: %v", scanErr)
			}
		} else if !errors.Is(cancelErr, ErrScanConflict) || scanErr != nil || !published.Load() {
			t.Fatalf("publication arbitration: %v %v", cancelErr, scanErr)
		}
	}
}
