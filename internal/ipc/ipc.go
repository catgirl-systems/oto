package ipc

import (
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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

const (
	Version                 = "v1"
	MaxBodySize       int64 = 1 << 20
	MaxBrowseBodySize int64 = 128 << 20
)

type Server struct {
	service        *daemon.Service
	path           string
	http           *http.Server
	listener       net.Listener
	listInterfaces func() ([]net.Interface, error)
}

func NewServer(service *daemon.Service, path string) *Server {
	return &Server{service: service, path: path, listInterfaces: net.Interfaces}
}

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
	mux.HandleFunc("GET /v1/state", s.state)
	mux.HandleFunc("GET /v1/network/interfaces", s.networkInterfaces)
	mux.HandleFunc("POST /v1/network/port-check", s.checkListeningPort)
	mux.HandleFunc("PUT /v1/presence", s.presence)
	mux.HandleFunc("PUT /v1/account/password", s.accountPassword)
	mux.HandleFunc("GET /v1/searches", s.searches)
	mux.HandleFunc("POST /v1/search", s.search)
	mux.HandleFunc("GET /v1/browse", s.browse)
	mux.HandleFunc("GET /v1/browse/progress", s.browseProgress)
	mux.HandleFunc("GET /v1/browse/saved", s.savedBrowses)
	mux.HandleFunc("POST /v1/browse/save", s.saveBrowse)
	mux.HandleFunc("POST /v1/downloads", s.downloads)
	mux.HandleFunc("POST /v1/folder-downloads", s.folderDownloads)
	mux.HandleFunc("GET /v1/transfers", s.transfers)
	mux.HandleFunc("POST /v1/transfers/{id}", s.transfers)
	mux.HandleFunc("GET /v1/shares", s.shares)
	mux.HandleFunc("POST /v1/shares", s.shares)
	mux.HandleFunc("GET /v1/shares/browse", s.shares)
	mux.HandleFunc("POST /v1/shares/rescan", s.shares)
	mux.HandleFunc("DELETE /v1/shares/{name}", s.shares)
	mux.HandleFunc("PUT /v1/config", s.updateConfig)
	mux.HandleFunc("PATCH /v1/config", s.updateConfig)
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
func (s *Server) state(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, s.service.Snapshot())
}

func portCheckStatus(err error) int {
	if errors.Is(err, daemon.ErrNotStarted) || errors.Is(err, daemon.ErrListenPortUnavailable) {
		return http.StatusServiceUnavailable
	}
	return http.StatusBadGateway
}

func (s *Server) checkListeningPort(w http.ResponseWriter, r *http.Request) {
	result, err := s.service.CheckListeningPort(r.Context())
	if err != nil {
		writeErr(w, portCheckStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) networkInterfaces(w http.ResponseWriter, _ *http.Request) {
	interfaces, err := s.listInterfaces()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	names := make([]string, 0, len(interfaces))
	for _, networkInterface := range interfaces {
		if networkInterface.Name != "" {
			names = append(names, networkInterface.Name)
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	writeJSON(w, http.StatusOK, names)
}

func (s *Server) presence(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Presence daemon.Presence `json:"presence"`
	}
	if err := decode(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.service.SetPresence(req.Presence); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) accountPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decode(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Password) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("ipc: password cannot be empty"))
		return
	}
	result, err := s.service.ChangePassword(r.Context(), req.Password)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (s *Server) searches(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	cursor, err := strconv.Atoi(r.URL.Query().Get("cursor"))
	if err != nil && r.URL.Query().Get("cursor") != "" {
		writeErr(w, 400, errors.New("ipc: invalid search cursor"))
		return
	}
	page, err := s.service.SearchPage(id, cursor, r.URL.Query().Get("filter"))
	if err != nil {
		status := http.StatusNotFound
		if errors.Is(err, daemon.ErrInvalidFilter) {
			status = http.StatusBadRequest
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, 200, page)
}
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query  string `json:"query"`
		Filter string `json:"filter"`
	}
	if err := decode(w, r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	out, err := s.service.Search(r.Context(), req.Query, req.Filter)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, daemon.ErrInvalidFilter) {
			status = http.StatusBadRequest
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) browse(w http.ResponseWriter, r *http.Request) {
	out, err := s.service.BrowseComplete(r.Context(), r.URL.Query().Get("user"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) browseProgress(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.service.BrowseProgress(r.URL.Query().Get("user")))
}

func (s *Server) savedBrowses(w http.ResponseWriter, _ *http.Request) {
	out, err := s.service.SavedBrowses()
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) saveBrowse(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Revision uint64 `json:"revision"`
	}
	if err := decode(w, r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	out, err := s.service.SaveBrowse(r.URL.Query().Get("user"), req.Revision)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) downloads(w http.ResponseWriter, r *http.Request) {
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
}

func (s *Server) folderDownloads(w http.ResponseWriter, r *http.Request) {
	var req daemon.FolderDownloadRequest
	if err := decode(w, r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	out, err := s.service.QueueFolder(r.Context(), req)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]int{"queued": len(out)})
}
func (s *Server) transfers(w http.ResponseWriter, r *http.Request) {
	if id := r.PathValue("id"); id != "" {
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
	writeJSON(w, 200, s.service.Transfers())
}
func (s *Server) shares(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/shares/browse":
		out, err := s.service.BrowseLocal(r.URL.Query().Get("path"))
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		writeJSON(w, 200, out)
	case r.URL.Path == "/v1/shares/rescan":
		if err := s.service.Rescan(); err != nil {
			writeErr(w, 400, err)
			return
		}
		writeJSON(w, 200, s.service.Shares())
	case r.PathValue("name") != "":
		if err := s.service.RemoveShare(r.PathValue("name")); err != nil {
			writeErr(w, 404, err)
			return
		}
		writeJSON(w, 200, s.service.Shares())
	case r.Method == "GET":
		writeJSON(w, 200, s.service.Shares())
	case r.Method == "POST":
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
	}
}
func (s *Server) updateConfig(w http.ResponseWriter, r *http.Request) {
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

// Client is the small Unix-socket JSON client used by the TUI.
type Client struct {
	path string
	http *http.Client
}

func NewClient(path string) *Client {
	return &Client{path: path, http: &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", path)
	}}}}
}
func (c *Client) Do(ctx context.Context, method, path string, body any, out any) error {
	return c.do(ctx, method, path, body, out, MaxBodySize)
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any, responseLimit int64) error {
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
	return json.NewDecoder(io.LimitReader(resp.Body, responseLimit)).Decode(out)
}
func (c *Client) Status(ctx context.Context) (daemon.Snapshot, error) {
	var x daemon.Snapshot
	err := c.Do(ctx, "GET", "/v1/state", nil, &x)
	return x, err
}

func (c *Client) CheckListeningPort(ctx context.Context) (daemon.ListeningPortCheck, error) {
	var result daemon.ListeningPortCheck
	err := c.Do(ctx, http.MethodPost, "/v1/network/port-check", nil, &result)
	return result, err
}

func (c *Client) SetPresence(ctx context.Context, presence daemon.Presence) error {
	return c.Do(ctx, "PUT", "/v1/presence", map[string]daemon.Presence{"presence": presence}, nil)
}

func (c *Client) ChangePassword(ctx context.Context, password string) (daemon.PasswordChangeResult, error) {
	var result daemon.PasswordChangeResult
	err := c.Do(ctx, "PUT", "/v1/account/password", map[string]string{"password": password}, &result)
	return result, err
}
func (c *Client) Search(ctx context.Context, q, filter string) (daemon.SearchPage, error) {
	var page daemon.SearchPage
	err := c.Do(ctx, "POST", "/v1/search", map[string]string{"query": q, "filter": filter}, &page)
	return page, err
}
func (c *Client) SearchPage(ctx context.Context, id string, cursor int, filter string) (daemon.SearchPage, error) {
	var page daemon.SearchPage
	path := fmt.Sprintf("/v1/searches?id=%s&cursor=%d&filter=%s", url.QueryEscape(id), cursor, url.QueryEscape(filter))
	err := c.Do(ctx, "GET", path, nil, &page)
	return page, err
}
func (c *Client) Browse(ctx context.Context, username string) (daemon.BrowseResult, error) {
	var result daemon.BrowseResult
	err := c.do(ctx, "GET", "/v1/browse?user="+url.QueryEscape(username), nil, &result, MaxBrowseBodySize)
	return result, err
}

func (c *Client) BrowseProgress(ctx context.Context, username string) (*daemon.BrowseProgress, error) {
	var progress *daemon.BrowseProgress
	err := c.Do(ctx, "GET", "/v1/browse/progress?user="+url.QueryEscape(username), nil, &progress)
	return progress, err
}

func (c *Client) SavedBrowses(ctx context.Context) ([]daemon.SavedBrowse, error) {
	var saved []daemon.SavedBrowse
	err := c.Do(ctx, "GET", "/v1/browse/saved", nil, &saved)
	return saved, err
}

func (c *Client) SaveBrowse(ctx context.Context, username string, revision uint64) (daemon.SavedBrowse, error) {
	var saved daemon.SavedBrowse
	err := c.Do(ctx, "POST", "/v1/browse/save?user="+url.QueryEscape(username), map[string]uint64{"revision": revision}, &saved)
	return saved, err
}
func (c *Client) QueueDownloads(ctx context.Context, r []daemon.DownloadRequest) ([]daemon.Download, error) {
	var x []daemon.Download
	err := c.Do(ctx, "POST", "/v1/downloads", r, &x)
	return x, err
}

func (c *Client) QueueFolder(ctx context.Context, req daemon.FolderDownloadRequest) (int, error) {
	var response struct {
		Queued int `json:"queued"`
	}
	err := c.Do(ctx, "POST", "/v1/folder-downloads", req, &response)
	return response.Queued, err
}
func (c *Client) Transfers(ctx context.Context) ([]daemon.Transfer, error) {
	var x []daemon.Transfer
	err := c.Do(ctx, "GET", "/v1/transfers", nil, &x)
	return x, err
}
func (c *Client) TransferAction(ctx context.Context, id, action string) error {
	return c.Do(ctx, "POST", "/v1/transfers/"+url.QueryEscape(id), map[string]string{"action": action}, nil)
}
func (c *Client) Shares(ctx context.Context) ([]config.Share, error) {
	var x []config.Share
	err := c.Do(ctx, "GET", "/v1/shares", nil, &x)
	return x, err
}

func (c *Client) BrowseShares(ctx context.Context, path string) ([]soulseek.ShareEntry, error) {
	var entries []soulseek.ShareEntry
	err := c.Do(ctx, "GET", "/v1/shares/browse?path="+url.QueryEscape(path), nil, &entries)
	return entries, err
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

func (c *Client) NetworkInterfaces(ctx context.Context) ([]string, error) {
	var names []string
	err := c.Do(ctx, "GET", "/v1/network/interfaces", nil, &names)
	return names, err
}
func (c *Client) UpdateConfig(ctx context.Context, cfg config.Config) (config.SafeConfig, error) {
	var x config.SafeConfig
	err := c.Do(ctx, "PUT", "/v1/config", cfg, &x)
	return x, err
}
