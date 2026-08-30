package ipc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestStatusMethodsBodyAndSocketMode(t *testing.T) {
	c := config.Default()
	c.Soulseek.Username, c.Soulseek.Password = "u", "p"
	c.DownloadDir = t.TempDir()
	shareRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(shareRoot, "Album"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shareRoot, "Album", "song.flac"), []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	c.Shares = []config.Share{{Name: "Music", Path: shareRoot}}
	journalPath := filepath.Join(t.TempDir(), "journal.json")
	if err := config.SaveJSON(journalPath, daemon.Journal{Downloads: []daemon.Download{{ID: "d-1", State: "running"}}}); err != nil {
		t.Fatal(err)
	}
	svc, err := daemon.New(c, journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	path := filepath.Join(t.TempDir(), "run", "slsk.sock")
	srv := NewServer(svc, path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	var snap daemon.Snapshot
	cl := NewClient(path)
	for i := 0; i < 50; i++ {
		if err := cl.Do(context.Background(), "GET", "/v1/state", nil, &snap); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if snap.Config.Soulseek.Username != "u" {
		t.Fatalf("snapshot: %+v", snap)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("socket mode: %v %v", st, err)
	}
	if err := cl.TransferAction(context.Background(), "d-1", "cancel"); err != nil || svc.Downloads()[0].State != "cancelled" {
		t.Fatalf("cancel transfer route: %v %+v", err, svc.Downloads())
	}
	if _, err := cl.Rescan(context.Background()); err != nil {
		t.Fatalf("rescan shares route: %v", err)
	}
	shareEntries, err := cl.BrowseShares(context.Background(), "Music")
	if err != nil || len(shareEntries) != 1 || shareEntries[0].Name != "Album" || !shareEntries[0].Directory {
		t.Fatalf("browse share root: %+v %v", shareEntries, err)
	}
	shareEntries, err = cl.BrowseShares(context.Background(), "Music/Album")
	if err != nil || len(shareEntries) != 1 || shareEntries[0].Name != "song.flac" {
		t.Fatalf("browse nested share: %+v %v", shareEntries, err)
	}
	if _, err := cl.BrowseShares(context.Background(), "Music/../outside"); err == nil {
		t.Fatal("share traversal route accepted")
	}
	resp, err := cl.http.Do(mustRequest("POST", "http://oto.local/v1/search", strings.NewReader(`{"query":"song","filter":"wat:true"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid initial filter status %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp, err = cl.http.Do(mustRequest("GET", "http://oto.local/v1/searches?id=missing&filter=size:nope", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid page filter status %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp, err = cl.http.Do(mustRequest("GET", "http://oto.local/v1/searches?id=missing", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing search status %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp, err = cl.http.Do(mustRequest("POST", "http://oto.local/v1/state", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 405 {
		t.Fatalf("method status %d", resp.StatusCode)
	}
	resp.Body.Close()
	big := strings.Repeat("x", int(MaxBodySize)+1)
	resp, err = cl.http.Do(mustRequest("POST", "http://oto.local/v1/downloads", strings.NewReader(big)))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Fatalf("body status %d", resp.StatusCode)
	}
	resp.Body.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}
func mustRequest(method, path string, body io.Reader) *http.Request {
	r, _ := http.NewRequest(method, path, body)
	return r
}
func TestStaleSocketIsRemovedAfterFailedDial(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "slsk.sock")
	if err := os.WriteFile(p, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	c := config.Default()
	c.Soulseek.Username, c.Soulseek.Password = "u", "p"
	c.DownloadDir = t.TempDir()
	svc, _ := daemon.New(c, filepath.Join(d, "j"))
	defer svc.Close()
	srv := NewServer(svc, p)
	ln, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	os.Remove(p)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestSearchClientSendsFilters(t *testing.T) {
	calls := 0
	client := &Client{http: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["query"] != "song" || body["filter"] != "type:flac" {
				t.Fatalf("search request: %+v %v", body, err)
			}
		} else if request.URL.Query().Get("filter") != `in:"live session"` || request.URL.Query().Get("cursor") != "100" {
			t.Fatalf("page query: %s", request.URL.RawQuery)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"s","results":[],"total":0,"found_total":0}`)), Header: make(http.Header)}, nil
	})}}
	if _, err := client.Search(context.Background(), "song", "type:flac"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SearchPage(context.Background(), "s", 100, `in:"live session"`); err != nil {
		t.Fatal(err)
	}
}

func TestBrowseClientAcceptsLargeShareLists(t *testing.T) {
	name := strings.Repeat("x", int(MaxBodySize)+1024)
	payload := `[{"Name":"` + name + `"}]`
	client := &Client{http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload)), Header: make(http.Header)}, nil
	})}}
	entries, err := client.Browse(context.Background(), "peer")
	if err != nil || len(entries) != 1 || entries[0].Name != name {
		t.Fatalf("large browse response: entries=%d err=%v", len(entries), err)
	}
}
