package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
	if snap.Config.Soulseek.Username != "u" || snap.Presence != daemon.PresenceOffline {
		t.Fatalf("snapshot: %+v", snap)
		if progress, err := cl.BrowseProgress(context.Background(), "peer"); err != nil || progress != nil {
			t.Fatalf("idle browse progress: %+v %v", progress, err)
		}
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("socket mode: %v %v", st, err)
	}
	if err := cl.SetPresence(context.Background(), daemon.PresenceOffline); err != nil {
		t.Fatalf("offline presence route: %v", err)
	}
	if err := cl.SetPresence(context.Background(), daemon.Presence("busy")); err == nil {
		t.Fatal("invalid presence accepted")
	}
	if _, err := cl.CheckListeningPort(context.Background()); err == nil || !strings.Contains(err.Error(), daemon.ErrNotStarted.Error()) {
		t.Fatalf("offline port check error: %v", err)
	}
	portResp, err := cl.http.Do(mustRequest(http.MethodPost, "http://oto.local/v1/network/port-check", nil))
	if err != nil {
		t.Fatal(err)
	}
	if portResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("offline port check status %d", portResp.StatusCode)
	}
	portResp.Body.Close()
	methodResp, err := cl.http.Do(mustRequest(http.MethodGet, "http://oto.local/v1/network/port-check", nil))
	if err != nil {
		t.Fatal(err)
	}
	if methodResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("port check method status %d", methodResp.StatusCode)
	}
	methodResp.Body.Close()
	passwordResp, err := cl.http.Do(mustRequest("PUT", "http://oto.local/v1/account/password", strings.NewReader(`{"password":"   "}`)))
	if err != nil {
		t.Fatal(err)
	}
	if passwordResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty password status %d", passwordResp.StatusCode)
	}
	passwordResp.Body.Close()
	result, err := cl.ChangePassword(context.Background(), "new-secret")
	if err == nil || result.Changed || strings.Contains(err.Error(), "new-secret") {
		t.Fatalf("disconnected password route leaked or accepted the password: %+v %v", result, err)
	}
	malformedResp, requestErr := cl.http.Do(mustRequest("PUT", "http://oto.local/v1/presence", strings.NewReader(`{`)))
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	if malformedResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed presence status %d", malformedResp.StatusCode)
	}
	malformedResp.Body.Close()
	wish, err := cl.PutWishlist(context.Background(), "rare album", "type:audio")
	if err != nil || wish.ID == "" || wish.Filter != "type:audio" {
		t.Fatalf("put wishlist: %+v %v", wish, err)
	}
	wishes, err := cl.Wishlist(context.Background())
	if err != nil || len(wishes) != 1 || wishes[0].ID != wish.ID {
		t.Fatalf("list wishlist: %+v %v", wishes, err)
	}
	if _, err := cl.PutWishlist(context.Background(), "bad", "unknown:value"); err == nil {
		t.Fatal("invalid wishlist filter accepted")
	}
	if _, err := cl.OpenWishlist(context.Background(), wish.ID); err == nil || !strings.Contains(err.Error(), "no cached results") {
		t.Fatalf("open empty wishlist: %v", err)
	}
	if _, err := cl.RunWishlist(context.Background(), wish.ID); err == nil || !strings.Contains(err.Error(), daemon.ErrNotStarted.Error()) {
		t.Fatalf("offline wishlist run: %v", err)
	}
	if err := cl.RemoveWishlist(context.Background(), wish.ID); err != nil {
		t.Fatalf("remove wishlist: %v", err)
	}
	if err := cl.RemoveWishlist(context.Background(), wish.ID); err == nil {
		t.Fatal("unknown wishlist item removed")
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
	cachePath := filepath.Join(filepath.Dir(journalPath), "usershares", "cGVlcg.json")
	if err := config.SaveJSON(cachePath, map[string]any{
		"version": 1, "username": "peer", "saved_at": time.Now().UTC(),
		"entries": []map[string]any{{"Name": `Music\cached.flac`, "Size": 42}},
	}); err != nil {
		t.Fatal(err)
	}
	savedBrowses, err := cl.SavedBrowses(context.Background())
	if err != nil || len(savedBrowses) != 1 || savedBrowses[0].Username != "peer" {
		t.Fatalf("saved browse list: %+v %v", savedBrowses, err)
	}
	browse, err := cl.Browse(context.Background(), "peer")
	if err != nil || !browse.Cached || browse.Revision == 0 || len(browse.Entries) != 1 || browse.Entries[0].Name != `Music\cached.flac` {
		t.Fatalf("cached browse route: %+v %v", browse, err)
	}
	if _, err := cl.SaveBrowse(context.Background(), "peer", browse.Revision); err != nil {
		t.Fatalf("save browse route: %v", err)
	}
	if _, err := cl.SaveBrowse(context.Background(), "peer", browse.Revision+1); err == nil {
		t.Fatal("save browse route accepted stale revision")
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
	resp, err = cl.http.Do(mustRequest("GET", "http://oto.local/v1/folder-downloads", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("folder download method status %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp, err = cl.http.Do(mustRequest("POST", "http://oto.local/v1/folder-downloads", strings.NewReader(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid folder download status %d", resp.StatusCode)
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

func TestPortCheckClientAndStatusMapping(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/network/port-check" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"port":61000,"open":true}`)), Header: make(http.Header)}, nil
	})}}
	result, err := client.CheckListeningPort(context.Background())
	if err != nil || result.Port != 61000 || !result.Open {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got := portCheckStatus(daemon.ErrNotStarted); got != http.StatusServiceUnavailable {
		t.Fatalf("offline status = %d", got)
	}
	if got := portCheckStatus(fmt.Errorf("%w: upstream", daemon.ErrPortCheckFailed)); got != http.StatusBadGateway {
		t.Fatalf("upstream status = %d", got)
	}
}

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

func TestBrowseProgressClient(t *testing.T) {
	calls := 0
	client := &Client{http: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/browse/progress" || request.URL.Query().Get("user") != "peer name" {
			t.Fatalf("progress request: %s", request.URL.String())
		}
		calls++
		body := `{"username":"peer name","received":25,"total":100}`
		if calls == 2 {
			body = `null`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
	progress, err := client.BrowseProgress(context.Background(), "peer name")
	if err != nil || progress == nil || progress.Received != 25 || progress.Total != 100 {
		t.Fatalf("active progress: %+v %v", progress, err)
	}
	progress, err = client.BrowseProgress(context.Background(), "peer name")
	if err != nil || progress != nil {
		t.Fatalf("missing progress: %+v %v", progress, err)
	}
}

func TestFolderDownloadClient(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var req daemon.FolderDownloadRequest
		if request.Method != "POST" || request.URL.Path != "/v1/folder-downloads" {
			t.Fatalf("folder request: %s %s", request.Method, request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&req); err != nil || req.Username != "peer" || req.Folder != `Music\Album` || !req.Recursive || len(req.Subfolders) != 1 || len(req.Files) != 1 {
			t.Fatalf("folder request body: %+v %v", req, err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"queued":2}`)), Header: make(http.Header)}, nil
	})}}
	queued, err := client.QueueFolder(context.Background(), daemon.FolderDownloadRequest{Username: "peer", Folder: `Music\Album`, Subfolders: []string{`Music\Album\Disc`}, Files: []daemon.DownloadItem{{Filename: `Music\Album\song.flac`, Size: 5}}, Recursive: true})
	if err != nil || queued != 2 {
		t.Fatalf("folder response: %d %v", queued, err)
	}
}

func TestBrowseClientAcceptsLargeShareLists(t *testing.T) {
	name := strings.Repeat("x", int(MaxBodySize)+1024)
	payload := `{"entries":[{"Name":"` + name + `"}],"revision":1}`
	client := &Client{http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload)), Header: make(http.Header)}, nil
	})}}
	result, err := client.Browse(context.Background(), "peer")
	if err != nil || len(result.Entries) != 1 || result.Entries[0].Name != name {
		t.Fatalf("large browse response: entries=%d err=%v", len(result.Entries), err)
	}
}

func TestNetworkInterfacesSortedUniqueAndErrors(t *testing.T) {
	srv := NewServer(nil, "")
	srv.listInterfaces = func() ([]net.Interface, error) {
		return []net.Interface{{Name: "wg0"}, {Name: "eth0"}, {Name: "wg0"}, {}}, nil
	}
	client := &Client{http: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		srv.handler().ServeHTTP(recorder, request)
		return recorder.Result(), nil
	})}}
	names, err := client.NetworkInterfaces(context.Background())
	if err != nil || strings.Join(names, ",") != "eth0,wg0" {
		t.Fatalf("interfaces = %v, %v", names, err)
	}

	srv.listInterfaces = func() ([]net.Interface, error) { return nil, errors.New("lookup failed") }
	if _, err := client.NetworkInterfaces(context.Background()); err == nil || !strings.Contains(err.Error(), "lookup failed") {
		t.Fatalf("interface lookup error = %v", err)
	}
}

func TestDownloadPauseResumeAndHookConfigRoutes(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "u", "p"
	svc, err := daemon.New(cfg, filepath.Join(t.TempDir(), "downloads.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	configPath := filepath.Join(t.TempDir(), "config.json")
	svc.SetConfigPath(configPath)
	downloads, err := svc.QueueDownloads([]daemon.DownloadRequest{{Username: "peer", Files: []daemon.DownloadItem{{Filename: "Album/song", Size: 4}}}})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(svc, "").handler()
	for _, step := range []struct{ action, state string }{{"pause", "paused"}, {"resume", "queued"}, {"cancel", "cancelled"}} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/transfers/"+downloads[0].ID, strings.NewReader(`{"action":"`+step.action+`"}`))
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK || svc.Transfers()[0].State != step.state {
			t.Fatalf("%s: HTTP %d %s; transfers %+v", step.action, w.Code, w.Body, svc.Transfers())
		}
	}
	cfg.Downloads.AfterFileCommand = `process-file "$1"`
	cfg.Downloads.AfterFolderCommand = `process-folder "$1"`
	body, _ := json.Marshal(cfg)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/v1/config", strings.NewReader(string(body))))
	if w.Code != http.StatusOK || !reflect.DeepEqual(svc.Config().Downloads, cfg.Downloads) {
		t.Fatalf("hook config: HTTP %d %s", w.Code, w.Body)
	}
	loaded, err := config.Load(configPath)
	if err != nil || !reflect.DeepEqual(loaded.Downloads, cfg.Downloads) {
		t.Fatalf("hook config not saved: %+v %v", loaded.Downloads, err)
	}
}
