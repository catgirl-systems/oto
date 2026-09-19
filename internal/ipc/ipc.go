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
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/danielgtaylor/huma/v2"
)

const (
	MaxBodySize int64 = 1 << 20
	// Browse replies are bounded pages, including folder ancestry and metadata.
	MaxBrowseBodySize int64 = 2 << 20
)

type Server struct {
	service        *daemon.Service
	path           string
	http           *http.Server
	listener       net.Listener
	listInterfaces func() ([]net.Interface, error)
	socketMux      *http.ServeMux
	tcpMux         *http.ServeMux
	socketAPI      huma.API
	tcpAPI         huma.API
}

func NewServer(service *daemon.Service, path string) *Server {
	s := &Server{service: service, path: path, listInterfaces: net.Interfaces}
	s.socketMux = http.NewServeMux()
	s.tcpMux = http.NewServeMux()
	s.newAPIs()
	s.registerRoutes()
	return s
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
	if s.listener == nil {
		return nil // Listen failed; this server never owned the socket.
	}
	if s.http != nil {
		_ = s.http.Shutdown(context.Background())
	}
	_ = s.listener.Close()
	return os.Remove(s.path)
}

// handler returns the Unix-socket server: every registered route.
func (s *Server) handler() http.Handler {
	return s.socketMux
}

// tcpHandler returns the TCP API server: public and authed routes only.
func (s *Server) tcpHandler() http.Handler {
	return s.tcpMux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

var errUnauthorized = errors.New("ipc: missing or invalid bearer token")

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
	return c.doWith(ctx, method, path, body, out, responseLimit, c.http)
}

func (c *Client) doWith(ctx context.Context, method, path string, body any, out any, responseLimit int64, client *http.Client) error {
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
	resp, e := client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var x map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&x)
		if x["error"] != "" {
			return &HTTPError{StatusCode: resp.StatusCode, Message: x["error"]}
		}
		return &HTTPError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("ipc: HTTP %s", resp.Status)}
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
func (c *Client) Search(ctx context.Context, q, filter string, users ...string) (daemon.SearchPage, error) {
	var page daemon.SearchPage
	err := c.Do(ctx, "POST", "/v1/search", map[string]any{"query": q, "filter": filter, "usernames": users}, &page)
	return page, err
}
func (c *Client) SearchScoped(ctx context.Context, req daemon.ScopedSearchRequest) (daemon.SearchPage, error) {
	var page daemon.SearchPage
	err := c.Do(ctx, http.MethodPost, "/v1/search", req, &page)
	return page, err
}
func (c *Client) SearchPage(ctx context.Context, id string, cursor int, filter string) (daemon.SearchPage, error) {
	var page daemon.SearchPage
	path := fmt.Sprintf("/v1/searches?id=%s&cursor=%d&filter=%s", url.QueryEscape(id), cursor, url.QueryEscape(filter))
	err := c.Do(ctx, "GET", path, nil, &page)
	return page, err
}

func (c *Client) Wishlist(ctx context.Context) ([]daemon.WishlistItem, error) {
	var items []daemon.WishlistItem
	err := c.Do(ctx, http.MethodGet, "/v1/wishlist", nil, &items)
	return items, err
}

func (c *Client) PutWishlist(ctx context.Context, query, filter string) (daemon.WishlistItem, error) {
	var item daemon.WishlistItem
	err := c.Do(ctx, http.MethodPut, "/v1/wishlist", map[string]string{"query": query, "filter": filter}, &item)
	return item, err
}

func (c *Client) RemoveWishlist(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/v1/wishlist/"+url.PathEscape(id), nil, nil)
}

func (c *Client) RunWishlist(ctx context.Context, id string) (daemon.SearchPage, error) {
	var page daemon.SearchPage
	err := c.Do(ctx, http.MethodPost, "/v1/wishlist/"+url.PathEscape(id)+"/run", nil, &page)
	return page, err
}

func (c *Client) OpenWishlist(ctx context.Context, id string) (daemon.SearchPage, error) {
	var page daemon.SearchPage
	err := c.Do(ctx, http.MethodPost, "/v1/wishlist/"+url.PathEscape(id)+"/open", nil, &page)
	return page, err
}
func (c *Client) Browse(ctx context.Context, username string) (daemon.BrowsePage, error) {
	return c.OpenBrowse(ctx, username, "", "")
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
	path := "/v1/folder-downloads"
	if req.Destination != "" {
		// Older daemons must reject rather than silently discard the destination.
		path += "/as"
	}
	err := c.Do(ctx, "POST", path, req, &response)
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

func (c *Client) UploadAction(ctx context.Context, req daemon.UploadActionRequest) (daemon.UploadActionResult, error) {
	var result daemon.UploadActionResult
	err := c.Do(ctx, "POST", "/v1/uploads/actions", req, &result)
	return result, err
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
	// The daemon may need longer than the ordinary interactive request budget.
	client := *c.http
	client.Timeout = 0
	err := c.doWith(ctx, "POST", "/v1/shares/rescan", nil, &x, MaxBodySize, &client)
	return x, err
}

func (c *Client) NetworkInterfaces(ctx context.Context) ([]string, error) {
	var names []string
	err := c.Do(ctx, "GET", "/v1/network/interfaces", nil, &names)
	return names, err
}

// ListAPIAuthRequests returns pending pairing requests for the Settings UI.
func (c *Client) ListAPIAuthRequests(ctx context.Context) ([]daemon.APIAuthRequest, error) {
	var requests []daemon.APIAuthRequest
	err := c.Do(ctx, http.MethodGet, "/v1/auth/requests", nil, &requests)
	return requests, err
}

func (c *Client) ApproveAPIAuthRequest(ctx context.Context, id string, expiresInDays int) error {
	return c.Do(ctx, http.MethodPost, "/v1/auth/requests/"+url.PathEscape(id)+"/approve", map[string]int{"expires_in_days": expiresInDays}, nil)
}

func (c *Client) RejectAPIAuthRequest(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodPost, "/v1/auth/requests/"+url.PathEscape(id)+"/reject", nil, nil)
}

func (c *Client) ListAPIApps(ctx context.Context) ([]daemon.APIApp, error) {
	var apps []daemon.APIApp
	err := c.Do(ctx, http.MethodGet, "/v1/apps", nil, &apps)
	return apps, err
}

func (c *Client) RevokeAPIApp(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/v1/apps/"+url.PathEscape(id), nil, nil)
}
func (c *Client) UpdateConfig(ctx context.Context, cfg config.Config) (config.SafeConfig, error) {
	var x config.SafeConfig
	client := *c.http
	client.Timeout = 0 // Settings may stage a complete share scan, like Rescan.
	err := c.doWith(ctx, "PUT", "/v1/config", cfg, &x, MaxBodySize, &client)
	return x, err
}
