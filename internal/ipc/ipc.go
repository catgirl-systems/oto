package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

const (
	Version           = "v1"
	MaxBodySize int64 = 1 << 20
)

type Server struct {
	service  *daemon.Service
	path     string
	http     *http.Server
	listener net.Listener
}

func NewServer(service *daemon.Service, paths ...string) *Server {
	path := config.SocketPath()
	if len(paths) > 0 && paths[0] != "" {
		path = paths[0]
	}
	return &Server{service: service, path: path}
}
func New(service *daemon.Service, paths ...string) *Server { return NewServer(service, paths...) }
func (s *Server) Path() string                             { return s.path }

// Listen takes ownership of the Unix socket. An existing socket is removed only
// after a dial proves it is stale; an active daemon is never displaced.
func (s *Server) Listen() (net.Listener, error) {
	if s.service == nil {
		return nil, errors.New("ipc: nil service")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return nil, err
	}
	_ = os.Chmod(filepath.Dir(s.path), 0700)
	if _, err := os.Lstat(s.path); err == nil {
		c, dialErr := net.DialTimeout("unix", s.path, 100*time.Millisecond)
		if dialErr == nil {
			_ = c.Close()
			return nil, errors.New("ipc: socket already in use")
		}
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", s.path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(s.path, 0600); err != nil {
		_ = ln.Close()
		_ = os.Remove(s.path)
		return nil, err
	}
	s.listener = ln
	return ln, nil
}
func (s *Server) Handler() http.Handler { return s.handler() }
func (s *Server) Serve(ctx context.Context) error {
	ln, err := s.Listen()
	if err != nil {
		return err
	}
	s.http = &http.Server{Handler: s.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	go func() { <-ctx.Done(); _ = s.http.Shutdown(context.Background()); _ = ln.Close(); _ = os.Remove(s.path) }()
	err = s.http.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func (s *Server) Start(ctx context.Context) error { return s.Serve(ctx) }
func (s *Server) Close() error {
	if s.http != nil {
		_ = s.http.Shutdown(context.Background())
	}
	if s.listener != nil {
		_ = s.listener.Close()
	}
	return os.Remove(s.path)
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/state", s.state)
	mux.HandleFunc("/v1/searches", s.searches)
	mux.HandleFunc("/v1/search", s.search)
	mux.HandleFunc("/v1/browse", s.browse)
	mux.HandleFunc("/v1/downloads", s.downloads)
	mux.HandleFunc("/v1/transfers", s.transfers)
	mux.HandleFunc("/v1/transfers/", s.transfers)
	mux.HandleFunc("/v1/shares", s.shares)
	mux.HandleFunc("/v1/shares/", s.shares)
	mux.HandleFunc("/v1/config", s.updateConfig)
	mux.HandleFunc("/v1/events", s.events)
	return mux
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
func method(w http.ResponseWriter, r *http.Request, allowed ...string) bool {
	for _, x := range allowed {
		if r.Method == x {
			return true
		}
	}
	writeErr(w, http.StatusMethodNotAllowed, errors.New("ipc: method not allowed"))
	return false
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodySize)
	d := json.NewDecoder(r.Body)
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("ipc: trailing request data")
	}
	return nil
}
func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "GET") {
		return
	}
	writeJSON(w, 200, s.service.Snapshot())
}
func (s *Server) searches(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "GET") {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, 200, s.service.Searches())
		return
	}
	cursor, err := strconv.Atoi(r.URL.Query().Get("cursor"))
	if err != nil && r.URL.Query().Get("cursor") != "" {
		writeErr(w, 400, errors.New("ipc: invalid search cursor"))
		return
	}
	page, err := s.service.SearchPage(id, cursor)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, 200, page)
}
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "POST") {
		return
	}
	var req struct {
		Query string `json:"query"`
	}
	if err := decode(w, r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	out, err := s.service.Search(r.Context(), req.Query)
	if err != nil {
		writeErr(w, 503, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) browse(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "GET") {
		return
	}
	out, err := s.service.Browse(r.Context(), r.URL.Query().Get("user"), r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) downloads(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		writeJSON(w, 200, s.service.Downloads())
	case "POST":
		var req []daemon.DownloadRequest
		if err := decode(w, r, &req); err != nil {
			writeErr(w, 400, err)
			return
		}
		out, err := s.service.QueueDownloads(req)
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, errors.New("ipc: method not allowed"))
	}
}
func (s *Server) transfers(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/transfers" {
		id := strings.TrimPrefix(r.URL.Path, "/v1/transfers/")
		if !method(w, r, "POST") {
			return
		}
		var req struct {
			Action string `json:"action"`
		}
		if err := decode(w, r, &req); err != nil {
			writeErr(w, 400, err)
			return
		}
		if err := s.service.TransferAction(id, req.Action); err != nil {
			writeErr(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if !method(w, r, "GET") {
		return
	}
	writeJSON(w, 200, s.service.Transfers())
}
func (s *Server) shares(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/shares")
	if path == "/rescan" {
		if !method(w, r, "POST") {
			return
		}
		if err := s.service.Rescan(); err != nil {
			writeErr(w, 400, err)
			return
		}
		writeJSON(w, 200, s.service.Shares())
		return
	}
	if path != "" && path != "/" {
		if !method(w, r, "DELETE") {
			return
		}
		if err := s.service.RemoveShare(strings.TrimPrefix(path, "/")); err != nil {
			writeErr(w, 404, err)
			return
		}
		writeJSON(w, 200, s.service.Shares())
		return
	}
	switch r.Method {
	case "GET":
		writeJSON(w, 200, s.service.Shares())
	case "POST":
		var sh config.Share
		if err := decode(w, r, &sh); err != nil {
			writeErr(w, 400, err)
			return
		}
		if err := s.service.AddShare(sh); err != nil {
			writeErr(w, 400, err)
			return
		}
		writeJSON(w, 200, s.service.Shares())
	default:
		writeErr(w, 405, errors.New("ipc: method not allowed"))
	}
}
func (s *Server) updateConfig(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "PUT", "PATCH") {
		return
	}
	var c config.Config
	if err := decode(w, r, &c); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.service.UpdateConfig(c); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, s.service.Config())
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "GET") {
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, errors.New("ipc: streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-s.service.Events():
			if !ok {
				return
			}
			b, _ := json.Marshal(ev)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, b)
			fl.Flush()
		}
	}
}

// Client is the small Unix-socket JSON client used by the TUI.
type Client struct {
	path string
	http *http.Client
}

func NewClient(paths ...string) *Client {
	p := config.SocketPath()
	if len(paths) > 0 && paths[0] != "" {
		p = paths[0]
	}
	return &Client{path: p, http: &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", p)
	}}}}
}
func (c *Client) Do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return e
		}
		if int64(len(b)) > MaxBodySize {
			return errors.New("ipc: request too large")
		}
		rd = strings.NewReader(string(b))
	}
	req, e := http.NewRequestWithContext(ctx, method, "http://oto.local"+path, rd)
	if e != nil {
		return e
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, e := c.http.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var x map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&x)
		if x["error"] != "" {
			return errors.New(x["error"])
		}
		return fmt.Errorf("ipc: HTTP %s", resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, MaxBodySize)).Decode(out)
}
func (c *Client) Status(ctx context.Context) (daemon.Snapshot, error) {
	var x daemon.Snapshot
	err := c.Do(ctx, "GET", "/v1/state", nil, &x)
	return x, err
}
func (c *Client) Search(ctx context.Context, q string) (daemon.SearchPage, error) {
	var page daemon.SearchPage
	err := c.Do(ctx, "POST", "/v1/search", map[string]string{"query": q}, &page)
	return page, err
}
func (c *Client) SearchPage(ctx context.Context, id string, cursor int) (daemon.SearchPage, error) {
	var page daemon.SearchPage
	path := fmt.Sprintf("/v1/searches?id=%s&cursor=%d", urlQuery(id), cursor)
	err := c.Do(ctx, "GET", path, nil, &page)
	return page, err
}
func (c *Client) Browse(ctx context.Context, username string) ([]soulseek.ShareEntry, error) {
	var entries []soulseek.ShareEntry
	err := c.Do(ctx, "GET", "/v1/browse?user="+urlQuery(username), nil, &entries)
	return entries, err
}
func (c *Client) QueueDownloads(ctx context.Context, r []daemon.DownloadRequest) ([]daemon.Download, error) {
	var x []daemon.Download
	err := c.Do(ctx, "POST", "/v1/downloads", r, &x)
	return x, err
}
func (c *Client) Downloads(ctx context.Context) ([]daemon.Download, error) {
	var x []daemon.Download
	err := c.Do(ctx, "GET", "/v1/downloads", nil, &x)
	return x, err
}
func (c *Client) Transfers(ctx context.Context) ([]daemon.Transfer, error) {
	var x []daemon.Transfer
	err := c.Do(ctx, "GET", "/v1/transfers", nil, &x)
	return x, err
}
func (c *Client) TransferAction(ctx context.Context, id, action string) error {
	return c.Do(ctx, "POST", "/v1/transfers/"+urlQuery(id), map[string]string{"action": action}, nil)
}
func (c *Client) Shares(ctx context.Context) ([]config.Share, error) {
	var x []config.Share
	err := c.Do(ctx, "GET", "/v1/shares", nil, &x)
	return x, err
}
func (c *Client) AddShare(ctx context.Context, sh config.Share) ([]config.Share, error) {
	var x []config.Share
	err := c.Do(ctx, "POST", "/v1/shares", sh, &x)
	return x, err
}
func (c *Client) Rescan(ctx context.Context) ([]config.Share, error) {
	var x []config.Share
	err := c.Do(ctx, "POST", "/v1/shares/rescan", nil, &x)
	return x, err
}
func (c *Client) UpdateConfig(ctx context.Context, cfg config.Config) (config.SafeConfig, error) {
	var x config.SafeConfig
	err := c.Do(ctx, "PUT", "/v1/config", cfg, &x)
	return x, err
}
func (c *Client) Events(ctx context.Context) (io.ReadCloser, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", "http://oto.local/v1/events", nil)
	if e != nil {
		return nil, e
	}
	resp, e := c.http.Do(req)
	if e != nil {
		return nil, e
	}
	if resp.StatusCode != 200 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("ipc: HTTP %s", resp.Status)
	}
	return resp.Body, nil
}
func urlQuery(x string) string { return url.QueryEscape(x) }

// Events returns decoded SSE events until EOF. The callback is intentionally
// synchronous so callers can choose their own cancellation and backpressure.
func (c *Client) StreamEvents(ctx context.Context, fn func(daemon.Event)) error {
	r, e := c.Events(ctx)
	if e != nil {
		return e
	}
	defer r.Close()
	sc := bufio.NewScanner(r)
	var data string
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
			var ev daemon.Event
			if json.Unmarshal([]byte(data), &ev) == nil && fn != nil {
				fn(ev)
			}
		}
	}
	return sc.Err()
}
