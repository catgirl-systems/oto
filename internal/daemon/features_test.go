package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestDefaultFilterValidationAndPersistence(t *testing.T) {
	cfg := testConfig(t)
	cfg.Search.DefaultFilter = "in:["
	if _, err := New(cfg, filepath.Join(t.TempDir(), "journal")); !errors.Is(err, ErrInvalidFilter) {
		t.Fatalf("New validation: %v", err)
	}
	cfg.Search.DefaultFilter = "type:audio"
	s, err := New(cfg, filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	s.SetConfigPath(path)
	if err := s.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	invalid := cfg
	invalid.Search.DefaultFilter = "duration:no"
	if err := s.UpdateConfig(invalid); !errors.Is(err, ErrInvalidFilter) {
		t.Fatalf("update validation: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || s.Config().Search.DefaultFilter != cfg.Search.DefaultFilter {
		t.Fatal("invalid update saved")
	}
	loaded, err := config.Load(path)
	if err != nil || loaded.Search.DefaultFilter != "type:audio" {
		t.Fatalf("roundtrip: %v", err)
	}
	s.searches["cached"] = Search{ID: "cached", Results: []SearchResult{{Path: "song.mp3", Extension: "mp3"}, {Path: "note.txt", Extension: "txt"}}}
	page, err := s.SearchPage("cached", 0, "")
	if err != nil || len(page.Results) != 2 {
		t.Fatalf("empty explicit API filter inherited default: %+v %v", page, err)
	}
	if _, err := s.PutWishlist("wish", ""); err != nil {
		t.Fatal(err)
	}
	if s.Wishlist()[0].Filter != "" {
		t.Fatal("wishlist inherited default")
	}
}

func TestScanProgressCountsAndBusyStartup(t *testing.T) {
	cfg := testConfig(t)
	cfg.Soulseek.ConnectOnStartup = false
	a, b := t.TempDir(), t.TempDir()
	cfg.Shares = []config.Share{{Name: "A", Path: a}, {Name: "B", Path: b}}
	for i := 0; i < 30; i++ {
		if err := os.WriteFile(filepath.Join(a, fmt.Sprint(i)), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(a, ".hidden"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a, filepath.Join(b, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "last"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.shareRescanDelay = 0
	entered, release := make(chan struct{}), make(chan struct{})
	var savedCtx context.Context
	s.shareIndexBuilder = func(ctx context.Context, shares []config.Share) (*soulseek.ShareIndex, error) {
		savedCtx = ctx
		first, err := buildShareIndex(ctx, shares[:1])
		if err != nil {
			return nil, err
		}
		second, err := buildShareIndex(ctx, shares[1:])
		if err != nil {
			return nil, err
		}
		index, err := soulseek.RestoreShareIndex(append(first.Roots(), second.Roots()...), append(first.Files(), second.Files()...))
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return index, err
	}
	done := make(chan error, 1)
	go func() { done <- s.Start(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("startup scan not reached")
	}
	snapCh := make(chan Snapshot, 1)
	go func() { snapCh <- s.Snapshot() }()
	var snap Snapshot
	select {
	case snap = <-snapCh:
	case <-time.After(time.Second):
		t.Fatal("startup held state lock")
	}
	if snap.ShareScan.State != "scanning" || snap.ShareScan.Root != "B" || snap.ShareScan.Files != 30 || snap.ShareScan.Directories != 2 || snap.ShareIndexRevision != 0 {
		t.Fatalf("throttled root update lost counts: %+v", snap.ShareScan)
	}
	if err := s.Rescan(); !errors.Is(err, ErrScanBusy) {
		t.Fatalf("manual busy: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	snap = s.Snapshot()
	if snap.ShareScan.State != "completed" || snap.ShareScan.Files != 31 || snap.ShareScan.Directories != 2 || snap.ShareIndexRevision != 1 {
		t.Fatalf("final scan: %+v", snap)
	}
	before := *snap.ShareScan
	// A callback retained by a finished builder cannot modify the published status.
	if _, err := buildShareIndex(context.WithoutCancel(savedCtx), cfg.Shares); err != nil {
		t.Fatal(err)
	}
	if after := s.Snapshot().ShareScan; *after != before {
		t.Fatalf("stale progress modified status: %+v", after)
	}
}

func TestScanFailureMutationAndShutdown(t *testing.T) {
	for _, operation := range []string{"manual", "add", "config"} {
		t.Run(operation, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Soulseek.ConnectOnStartup = false
			cfg.Shares = []config.Share{{Name: "Music", Path: t.TempDir()}}
			s, err := New(cfg, filepath.Join(t.TempDir(), "journal"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			s.SetConfigPath(filepath.Join(t.TempDir(), "config.json"))
			s.shareRescanDelay = 0
			if err := s.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			old := s.Snapshot()
			entered := make(chan struct{})
			s.shareIndexBuilder = func(ctx context.Context, _ []config.Share) (*soulseek.ShareIndex, error) {
				close(entered)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			done := make(chan error, 1)
			go func() {
				switch operation {
				case "manual":
					done <- s.Rescan()
				case "add":
					done <- s.AddShare(config.Share{Name: "Other", Path: t.TempDir()})
				case "config":
					next := cfg
					next.DownloadSlots++
					done <- s.UpdateConfig(next)
				}
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("scan not started")
			}
			closed := make(chan struct{})
			go func() { _ = s.Close(); close(closed) }()
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("Close did not cancel/wait for scan")
			}
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel: %v", err)
			}
			after := s.Snapshot()
			if after.ShareScan.State != "cancelled" || after.ShareIndexRevision != old.ShareIndexRevision || len(after.Shares) != len(old.Shares) || after.Config.DownloadSlots != cfg.DownloadSlots {
				t.Fatalf("cancel changed index/config: %+v", after)
			}
		})
	}
	s := downloadService(t)
	s.SetConfigPath(filepath.Join(t.TempDir(), "config"))
	old := s.Snapshot()
	if err := s.AddShare(config.Share{Name: "Missing", Path: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("missing share accepted")
	}
	if snap := s.Snapshot(); len(snap.Shares) != 0 || snap.ShareScan.State != "failed" || snap.ShareIndexRevision != old.ShareIndexRevision {
		t.Fatalf("failed candidate published: %+v", snap)
	}
}

func TestCompletionNotifications(t *testing.T) {
	for _, tc := range []struct {
		files, folders     bool
		messages, sequence int
	}{{false, false, 0, 0}, {true, false, 2, 2}, {false, true, 1, 1}, {true, true, 3, 2}} {
		t.Run(fmt.Sprintf("%v-%v", tc.files, tc.folders), func(t *testing.T) {
			s := downloadService(t)
			s.cfg.Downloads.FileNotifications, s.cfg.Downloads.FolderNotifications = tc.files, tc.folders
			delivered := make(chan string, 8)
			s.desktopNotify = func(ctx context.Context, title, body string) error {
				data, err := os.ReadFile(s.journalPath)
				if err != nil || !strings.Contains(string(data), "completed") {
					t.Error("notification preceded journal save")
				}
				delivered <- title + ":" + body
				return errors.New("desktop unavailable") // Must not fail or retry the transfer.
			}
			ds, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "Album/a.flac", Size: 4}, {Filename: "Album/b.flac", Size: 4}}}})
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			for _, d := range ds {
				part := putPartial(t, d.ID, "data")
				s.updateDownload(d.ID, "running", 4, nil)
				wg.Add(1)
				go func(d Download) { defer wg.Done(); s.completeDownload(d.ID, d.DownloadDir, part) }(d)
			}
			wg.Wait()
			s.wg.Wait()
			snap := s.Snapshot()
			if len(delivered) != tc.messages || int(snap.DownloadNotification.Sequence) != tc.sequence {
				t.Fatalf("delivery count %d signal %+v", len(delivered), snap.DownloadNotification)
			}
			for _, d := range s.Downloads() {
				if d.State != "completed" {
					t.Fatalf("notifier failure changed download: %+v", d)
				}
			}
			for _, d := range ds {
				s.completeDownload(d.ID, d.DownloadDir, incompletePath(d.ID))
			}
			if s.Snapshot().DownloadNotification.Sequence != snap.DownloadNotification.Sequence {
				t.Fatal("duplicate completion notified")
			}
			restored, err := New(s.cfg, s.journalPath)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			if restored.Snapshot().DownloadNotification.Sequence != 0 || restored.downloadNotification.SessionID == s.downloadNotification.SessionID {
				t.Fatal("restore replayed notifications")
			}
			// A later file is a new completion, not suppressed forever by folder name.
			if tc.folders {
				more, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "Album/c.flac", Size: 4}}}})
				if err != nil {
					t.Fatal(err)
				}
				d := more[0]
				s.updateDownload(d.ID, "running", 4, nil)
				s.completeDownload(d.ID, d.DownloadDir, putPartial(t, d.ID, "data"))
				s.wg.Wait()
				if s.Snapshot().DownloadNotification.Sequence != snap.DownloadNotification.Sequence+1 {
					t.Fatal("later folder completion lost")
				}
			}
		})
	}
}

func TestNotificationFailureBoundaries(t *testing.T) {
	for _, mode := range []string{"move", "save", "root", "paused"} {
		t.Run(mode, func(t *testing.T) {
			s := downloadService(t)
			s.cfg.Downloads.FolderNotifications = true
			s.desktopNotify = func(context.Context, string, string) error { t.Error("unexpected notification"); return nil }
			ds, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "Album/a", Size: 4}}}})
			if err != nil {
				t.Fatal(err)
			}
			d := ds[0]
			s.updateDownload(d.ID, "running", 4, nil)
			if mode == "root" {
				s.journal.Downloads[0].Destination = "file"
			}
			if mode == "paused" {
				s.journal.Downloads[0].State = "paused"
			}
			part := putPartial(t, d.ID, "data")
			if mode == "move" {
				part = filepath.Join(t.TempDir(), "absent")
			}
			if mode == "save" {
				s.journalPath = t.TempDir()
			}
			s.completeDownload(d.ID, d.DownloadDir, part)
			s.wg.Wait()
			if s.Snapshot().DownloadNotification.Sequence != 0 {
				t.Fatal("failure advanced notification signal")
			}
		})
	}
}

func TestDesktopNotificationArguments(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "arguments")
	t.Setenv("PATH", dir)
	t.Setenv("OTO_TEST_NOTIFICATION_ARGS", out)
	if err := notifyDesktop(context.Background(), "title", "body"); err == nil {
		t.Fatal("missing notify-send should fail")
	}
	if err := os.WriteFile(filepath.Join(dir, "notify-send"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$OTO_TEST_NOTIFICATION_ARGS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := notifyDesktop(context.Background(), "File downloaded", `<b>literal $HOME</b>`); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(out)
	if string(args) != "--\nFile downloaded\n&lt;b&gt;literal $HOME&lt;/b&gt;\n" {
		t.Fatalf("unsafe args: %s", args)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := notifyDesktop(ctx, "title", "body"); err == nil {
		t.Fatal("cancel ignored")
	}
}
