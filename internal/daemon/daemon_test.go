package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	c := config.Default()
	c.Soulseek.Username, c.Soulseek.Password = "u", "p"
	c.Soulseek.NATPMPPortMapping, c.Soulseek.UPnPPortMapping = false, false
	c.Downloads.FolderNotifications = false // Unit tests must not contact the desktop.
	c.DownloadDir = t.TempDir()
	return c
}

func closedAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

func TestJournalRoundTripAndSafeResume(t *testing.T) {
	d := t.TempDir()
	c := testConfig(t)
	completed := filepath.Join(c.DownloadDir, "Music", "song.mp3")
	if err := os.MkdirAll(filepath.Dir(completed), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(completed, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	jp := filepath.Join(d, "downloads.json")
	s, err := New(c, jp)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "Music/song.mp3", Size: 10}}}})
	if err != nil || len(got) != 1 || got[0].Filename != `Music\song.mp3` || got[0].Offset != 0 || got[0].State != "queued" || got[0].Destination != "peer/Music/song.mp3" {
		t.Fatalf("queue: %+v %v", got, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(jp)
	if err != nil {
		t.Fatal(err)
	}
	var j Journal
	if err := json.Unmarshal(b, &j); err != nil || len(j.Downloads) != 1 {
		t.Fatalf("journal: %v %+v", err, j)
	}
	s2, err := New(c, jp)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.Downloads()) != 1 {
		t.Fatal("journal did not reload")
	}
}

func TestFolderDownloadItems(t *testing.T) {
	entries := []soulseek.ShareEntry{
		{Name: `Music\Album`, Directory: true},
		{Name: `Music\Album\cover.jpg`, Size: 3},
		{Name: `Music\Album\cover.jpg`, Size: 3},
		{Name: `Music\Album\Disc`, Directory: true},
		{Name: `Music\Album\Disc\song.flac`, Size: 5},
	}
	direct, err := folderDownloadItems(FolderDownloadRequest{Folder: `Music\Album`}, entries)
	if err != nil || len(direct) != 1 || direct[0].Filename != "Music/Album/cover.jpg" {
		t.Fatalf("direct folder items: %+v %v", direct, err)
	}
	recursive, err := folderDownloadItems(FolderDownloadRequest{Folder: `Music\Album`, Recursive: true}, entries)
	if err != nil || len(recursive) != 2 || recursive[1].Filename != "Music/Album/Disc/song.flac" {
		t.Fatalf("recursive folder items: %+v %v", recursive, err)
	}
	remaining := withoutExistingFolderDownloads(append([]DownloadItem(nil), recursive...), []Download{{Username: "peer", Filename: `Music\Album\cover.jpg`}}, "peer", "/downloads", "/downloads")
	if len(remaining) != 1 || remaining[0].Filename != "Music/Album/Disc/song.flac" {
		t.Fatalf("existing folder download was not skipped: %+v", remaining)
	}
	if _, err := folderDownloadItems(FolderDownloadRequest{Folder: `Music\Album`, Recursive: true}, append(entries, soulseek.ShareEntry{Name: `Music\Other\bad`})); err == nil {
		t.Fatal("out-of-subtree entry accepted")
	}
	if _, err := folderDownloadItems(FolderDownloadRequest{Folder: `Music\Album`, Recursive: true}, []soulseek.ShareEntry{{Name: `..\bad`}}); err == nil {
		t.Fatal("malformed entry accepted")
	}

	s, err := New(testConfig(t), filepath.Join(t.TempDir(), "downloads.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	queued, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: recursive}})
	if err != nil || len(queued) != 2 || queued[0].Destination != "peer/Music/Album/cover.jpg" || queued[1].Destination != "peer/Music/Album/Disc/song.flac" {
		t.Fatalf("folder destinations: %+v %v", queued, err)
	}
	customRoot := t.TempDir()
	custom, err := s.QueueFolder(context.Background(), FolderDownloadRequest{Username: "peer", DownloadDir: customRoot, Folder: `Music\Album`, Files: []DownloadItem{{Filename: `Music\Album\cover.jpg`, Size: 3}}})
	if err != nil || len(custom) != 1 || custom[0].DownloadDir != customRoot {
		t.Fatalf("custom folder destination: %+v %v", custom, err)
	}
}

func TestClearDownloadRemovesIncompleteFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, err := New(testConfig(t), filepath.Join(t.TempDir(), "downloads.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.journal.Downloads = []Download{{ID: "d-1", State: "cancelled"}}
	s.transfers["d-1"] = Transfer{ID: "d-1", Direction: "download", State: "cancelled"}
	part := incompletePath("d-1")
	if err := os.MkdirAll(filepath.Dir(part), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(part, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := s.TransferAction("d-1", "clear"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatalf("incomplete file still exists: %v", err)
	}
	if len(s.Downloads()) != 0 {
		t.Fatal("cleared download remains in journal")
	}
}

func TestCloseRequeuesPartialDownload(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := testConfig(t)
	cfg.Soulseek.ConnectOnStartup = false
	journalPath := filepath.Join(t.TempDir(), "downloads.json")
	service, err := New(cfg, journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	downloads, err := service.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "song.flac", Size: 20}}}})
	if err != nil {
		t.Fatal(err)
	}
	part := incompletePath(downloads[0].ID)
	if err := os.MkdirAll(filepath.Dir(part), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(part, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := service.SetPresence(PresenceOnline); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		download := service.Downloads()[0]
		return download.State == "running" && download.Offset == 7
	})
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(cfg, journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if download := restarted.Downloads()[0]; download.State != "queued" || download.Offset != 7 {
		t.Fatalf("download after shutdown: %+v", download)
	}
}

func TestStartConnectionFailureDoesNotDeadlock(t *testing.T) {
	cfg := testConfig(t)
	cfg.Soulseek.Server, cfg.Soulseek.ListenAddr = closedAddress(t), closedAddress(t)
	service, err := New(cfg, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return service.Snapshot().Status == StatusReconnecting })
	if service.Snapshot().Presence != PresenceOnline {
		t.Fatalf("presence: %s", service.Snapshot().Presence)
	}
	if err := service.SetPresence(PresenceOffline); err != nil {
		t.Fatal(err)
	}
	if snapshot := service.Snapshot(); snapshot.Status != StatusStopped || snapshot.Presence != PresenceOffline {
		t.Fatalf("offline snapshot: %+v", snapshot)
	}
	if err := service.SetPresence(PresenceOffline); err != nil {
		t.Fatalf("repeated offline: %v", err)
	}
}

func TestPermanentLoginErrorWaitsForManualRetry(t *testing.T) {
	server, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	attempts := make(chan struct{}, 4)
	go func() {
		for {
			conn, acceptErr := server.Accept()
			if acceptErr != nil {
				return
			}
			attempts <- struct{}{}
			_, _, _ = soulseek.ReadFrame(conn)
			frame, _ := soulseek.EncodeMessage(soulseek.LoginResponse{Message: "invalid credentials"})
			_, _ = conn.Write(frame)
			_ = conn.Close()
		}
	}()

	cfg := testConfig(t)
	cfg.Soulseek.Server, cfg.Soulseek.ListenAddr = server.Addr().String(), closedAddress(t)
	service, err := New(cfg, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return service.Snapshot().Status == StatusError })
	<-attempts
	select {
	case <-attempts:
		t.Fatal("permanent login error retried automatically")
	case <-time.After(1100 * time.Millisecond):
	}
	if err := service.SetPresence(PresenceOnline); err != nil {
		t.Fatal(err)
	}
	select {
	case <-attempts:
	case <-time.After(2 * time.Second):
		t.Fatal("manual retry did not reconnect")
	}
}

func TestStartupDisabledAndAwayRetry(t *testing.T) {
	cfg := testConfig(t)
	cfg.Soulseek.ConnectOnStartup = false
	cfg.Soulseek.Server, cfg.Soulseek.ListenAddr = closedAddress(t), closedAddress(t)
	service, err := New(cfg, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if snapshot := service.Snapshot(); snapshot.Status != StatusStopped || snapshot.Presence != PresenceOffline {
		t.Fatalf("startup-disabled snapshot: %+v", snapshot)
	}
	if err := service.SetPresence(PresenceAway); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return service.Snapshot().Status == StatusReconnecting })
	if service.Snapshot().Presence != PresenceAway {
		t.Fatalf("away was not retained: %+v", service.Snapshot())
	}
	if err := service.SetPresence(PresenceAway); err != nil {
		t.Fatalf("retry away: %v", err)
	}
	if err := service.SetPresence(PresenceOffline); err != nil {
		t.Fatal(err)
	}
	service.SetConfigPath(filepath.Join(t.TempDir(), "config.json"))
	next := cfg
	next.Soulseek.ConnectOnStartup = true
	if err := service.UpdateConfig(next); err != nil {
		t.Fatal(err)
	}
	next.Soulseek.Server = "example.com:2242"
	if err := service.UpdateConfig(next); err != nil {
		t.Fatal(err)
	}
	if snapshot := service.Snapshot(); snapshot.Status != StatusStopped || snapshot.Presence != PresenceOffline {
		t.Fatalf("offline config update connected: %+v", snapshot)
	}
}

func TestOfflineRequeuesAndResumesPartialDownload(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := testConfig(t)
	cfg.Soulseek.ConnectOnStartup = false
	cfg.Soulseek.Server, cfg.Soulseek.ListenAddr = closedAddress(t), closedAddress(t)
	service, err := New(cfg, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	downloads, err := service.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "song.flac", Size: 20}}}})
	if err != nil {
		t.Fatal(err)
	}
	part := incompletePath(downloads[0].ID)
	if err := os.MkdirAll(filepath.Dir(part), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(part, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := service.SetPresence(PresenceOnline); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return service.Downloads()[0].State == "running" })
	if err := service.SetPresence(PresenceOffline); err != nil {
		t.Fatal(err)
	}
	if download := service.Downloads()[0]; download.State != "queued" || download.Offset != 7 {
		t.Fatalf("paused download: %+v", download)
	}
	if data, err := os.ReadFile(part); err != nil || string(data) != "partial" {
		t.Fatalf("partial file: %q %v", data, err)
	}
	if err := service.SetPresence(PresenceOnline); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		download := service.Downloads()[0]
		return download.State == "running" && download.Offset == 7
	})
	if err := service.SetPresence(PresenceOffline); err != nil {
		t.Fatal(err)
	}
}

func TestContextWithEOF(t *testing.T) {
	reader, writer := io.Pipe()
	ctx, cancel := ContextWithEOF(context.Background(), reader)
	defer cancel()
	_ = writer.Close()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("EOF did not stop child context")
	}
}

func TestSearchPagesCachedResults(t *testing.T) {
	results := make([]SearchResult, 205)
	for i := range results {
		results[i] = SearchResult{Username: "peer", Path: fmt.Sprintf("song-%d", i)}
	}
	search := Search{ID: "search", Query: "song", Results: results}

	page, err := searchPage(search, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 100 || page.NextCursor != 100 || page.Total != 205 {
		t.Fatalf("first page: %+v", page)
	}
	page, err = searchPage(search, page.NextCursor, "")
	if len(page.Results) != 100 || page.Results[0].Path != "song-100" || page.NextCursor != 200 {
		t.Fatalf("second page: %+v", page)
	}
	page, err = searchPage(search, page.NextCursor, "")
	if len(page.Results) != 5 || page.Results[0].Path != "song-200" || page.NextCursor != 0 {
		t.Fatalf("last page: %+v", page)
	}
}

func TestSearchResultsSortPrivateLast(t *testing.T) {
	results := []SearchResult{
		{Username: "private", Public: false, SlotFree: true, Speed: 1000},
		{Username: "public-slow", Public: true, Queue: 5, Speed: 1},
		{Username: "public-fast", Public: true, SlotFree: true, Speed: 100},
	}
	sortSearchResults(results)
	if results[0].Username != "public-fast" || results[1].Username != "public-slow" || results[2].Username != "private" {
		t.Fatalf("search order: %+v", results)
	}
}

func TestUpdateConfigHotAppliesSearchAndDownload(t *testing.T) {
	cfg := testConfig(t)
	s, err := New(cfg, filepath.Join(t.TempDir(), "downloads.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	configPath := filepath.Join(t.TempDir(), "config.json")
	s.SetConfigPath(configPath)
	client := soulseek.NewClient(soulseek.ClientConfig{})
	s.mu.Lock()
	s.client = client
	s.mu.Unlock()

	builderCalled := false
	s.shareIndexBuilder = func(context.Context, []config.Share) (*soulseek.ShareIndex, error) {
		builderCalled = true
		return nil, fmt.Errorf("share builder called")
	}
	next := cfg
	next.DownloadDir = t.TempDir()
	next.Search.RememberFilters = false
	next.Search.SearchHistoryLimit = 0
	next.Search.WishlistIntervalMinutes = 30
	next.Soulseek.ConnectOnStartup = false
	next.Search.RespondToIncomingSearches = false
	next.Search.MinimumIncomingSearchLength = 7
	next.Search.MaximumIncomingSearchResults = 500
	if err := s.UpdateConfig(next); err != nil {
		t.Fatalf("hot update: %v", err)
	}
	select {
	case <-s.wishlistWake:
	default:
		t.Fatal("wishlist scheduler was not woken by interval change")
	}
	if builderCalled || s.client != client || s.cfg.DownloadDir != next.DownloadDir || s.cfg.Search != next.Search || s.cfg.Soulseek.ConnectOnStartup {
		t.Fatalf("hot update rebuilt or was not adopted: called=%v sameClient=%v cfg=%+v", builderCalled, s.client == client, s.cfg)
	}
	if got := client.IncomingSearchPolicy(); got != incomingSearchPolicy(next) {
		t.Fatalf("incoming search policy was not hot-applied: %+v", got)
	}
	loaded, err := config.Load(configPath)
	if err != nil || loaded.Search != next.Search || loaded.DownloadDir != next.DownloadDir || loaded.Soulseek.ConnectOnStartup {
		t.Fatalf("saved hot update: %+v %v", loaded, err)
	}

	connectionChange := next
	connectionChange.Soulseek.Server = "example.com:2242"
	if err := s.UpdateConfig(connectionChange); err == nil || !builderCalled {
		t.Fatalf("session update bypassed existing full path: called=%v err=%v", builderCalled, err)
	}
}

func TestUpdateConfigHotAppliesUploadsAfterSave(t *testing.T) {
	cfg := testConfig(t)
	s, err := New(cfg, filepath.Join(t.TempDir(), "downloads.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	configPath := filepath.Join(t.TempDir(), "config.json")
	s.SetConfigPath(configPath)

	client := soulseek.NewClient(soulseek.ClientConfig{Uploads: newUploadManager(cfg)})
	s.mu.Lock()
	s.client = client
	s.mu.Unlock()
	builderCalled := false
	s.shareIndexBuilder = func(context.Context, []config.Share) (*soulseek.ShareIndex, error) {
		builderCalled = true
		return nil, errors.New("share builder called")
	}

	next := cfg
	next.Uploads.Profiles = []config.UploadProfile{{Name: "Limited", SpeedLimitKiB: 64}}
	next.Uploads.ActiveProfile = "Limited"
	next.Uploads.LimitScope = config.UploadLimitPerTransfer
	next.Uploads.Scheduling = config.UploadSchedulingRoundRobin
	if err := s.UpdateConfig(next); err != nil {
		t.Fatal(err)
	}
	want := soulseek.UploadPolicy{Scheduling: soulseek.UploadScheduleRoundRobin, BytesPerSecond: 64 * 1024, PerTransfer: true}
	if builderCalled || s.client != client || client.UploadPolicy() != want {
		t.Fatalf("upload update rebuilt or reconnected: builder=%v sameClient=%v policy=%+v", builderCalled, s.client == client, client.UploadPolicy())
	}
	loaded, err := config.Load(configPath)
	if err != nil || !sameUploadConfig(loaded.Uploads, next.Uploads) {
		t.Fatalf("saved upload config: %+v %v", loaded.Uploads, err)
	}

	before := client.UploadPolicy()
	failed := next
	failed.Uploads.Profiles = append([]config.UploadProfile(nil), next.Uploads.Profiles...)
	failed.Uploads.Profiles[0].SpeedLimitKiB = 32
	s.SetConfigPath(t.TempDir())
	if err := s.UpdateConfig(failed); err == nil {
		t.Fatal("upload update unexpectedly saved to a directory")
	}
	if client.UploadPolicy() != before || !sameUploadConfig(s.cfg.Uploads, next.Uploads) {
		t.Fatalf("failed save altered runtime: policy=%+v config=%+v", client.UploadPolicy(), s.cfg.Uploads)
	}
}

func TestChangePasswordPersistenceAndOwnership(t *testing.T) {
	cfg := testConfig(t)
	service, err := New(cfg, filepath.Join(t.TempDir(), "downloads.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if _, err := service.ChangePassword(context.Background(), "new"); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("disconnected password change: %v", err)
	}
	t.Setenv("OTO_PASSWORD", "owned")
	if _, err := service.ChangePassword(context.Background(), "new"); err == nil || !strings.Contains(err.Error(), "OTO_PASSWORD") {
		t.Fatalf("environment-owned password change: %v", err)
	}
	t.Setenv("OTO_PASSWORD", "")
	_ = os.Unsetenv("OTO_PASSWORD")

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	client := soulseek.NewClientOnConn(soulseek.ClientConfig{Username: "u", Password: "p"}, clientConn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	serverDone := make(chan error, 1)
	go func() {
		command, _, err := soulseek.ReadFrame(serverConn)
		if err == nil && command != soulseek.ServerLogin {
			err = fmt.Errorf("login command: %d", command)
		}
		if err == nil {
			var response soulseek.Encoder
			response.Bool(true)
			_ = response.String("ok")
			response.U32(0)
			_ = response.String("hash")
			response.Bool(false)
			err = soulseek.WriteFrame(serverConn, soulseek.ServerLogin, response.Payload())
		}
		for range 4 {
			if err == nil {
				_, _, err = soulseek.ReadFrame(serverConn)
			}
		}
		for range 2 {
			if err != nil {
				break
			}
			command, payload, readErr := soulseek.ReadFrame(serverConn)
			err = readErr
			if err == nil && command != soulseek.ServerChangePassword {
				err = fmt.Errorf("password command: %d", command)
			}
			if err == nil {
				err = soulseek.WriteFrame(serverConn, soulseek.ServerChangePassword, payload)
			}
		}
		serverDone <- err
	}()
	if err := client.Login(ctx); err != nil {
		t.Fatal(err)
	}
	go client.Run(ctx)
	service.mu.Lock()
	service.client, service.status = client, StatusConnected
	service.mu.Unlock()

	configPath := filepath.Join(t.TempDir(), "config.json")
	service.SetConfigPath(configPath)
	result, err := service.ChangePassword(ctx, " new secret ")
	if err != nil || !result.Changed || !result.Saved || result.Warning != "" {
		t.Fatalf("password change result: %+v %v", result, err)
	}
	loaded, err := config.Load(configPath)
	if err != nil || loaded.Soulseek.Password != " new secret " || service.cfg.Soulseek.Password != " new secret " {
		t.Fatalf("persisted password: %q/%q %v", loaded.Soulseek.Password, service.cfg.Soulseek.Password, err)
	}
	if info, err := os.Stat(configPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("config permissions: %v %v", info, err)
	}

	service.SetConfigPath(t.TempDir())
	result, err = service.ChangePassword(ctx, "unsaved-secret")
	if err != nil || !result.Changed || result.Saved || !strings.Contains(result.Warning, "not saved") || strings.Contains(result.Warning, "unsaved-secret") || service.cfg.Soulseek.Password != "unsaved-secret" {
		t.Fatalf("partial password change: %+v %v", result, err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}
