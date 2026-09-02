package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	c := config.Default()
	c.Soulseek.Username, c.Soulseek.Password = "u", "p"
	c.DownloadDir = t.TempDir()
	return c
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

func TestStartConnectionFailureDoesNotDeadlock(t *testing.T) {
	server, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverAddr := server.Addr().String()
	_ = server.Close()
	peer, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	peerAddr := peer.Addr().String()
	_ = peer.Close()
	cfg := testConfig(t)
	cfg.Soulseek.Server, cfg.Soulseek.ListenAddr = serverAddr, peerAddr
	service, err := New(cfg, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- service.Start(ctx) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected connection failure")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start deadlocked while recording connection error")
	}
	if service.Snapshot().Status != StatusReconnecting {
		t.Fatalf("status: %s", service.Snapshot().Status)
	}
	_ = service.Close()
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
