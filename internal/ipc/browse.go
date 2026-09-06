package ipc

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) browsePage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	revision, err := strconv.ParseUint(q.Get("revision"), 10, 64)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	cursor, err := strconv.Atoi(q.Get("cursor"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	out, err := s.service.BrowsePage(r.Context(), daemon.BrowsePageRequest{Username: q.Get("user"), Revision: revision, Folder: q.Get("folder"), Query: q.Get("query"), Cursor: cursor})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) browseDownload(w http.ResponseWriter, r *http.Request) {
	var req daemon.BrowseDownloadRequest
	if err := decode(w, r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if r.URL.Path == "/v1/browse/download-as" && req.Destination == "" {
		writeErr(w, 400, errors.New("download destination is required"))
		return
	}
	out, err := s.service.QueueBrowse(r.Context(), req)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, out)
}

func (c *Client) OpenBrowse(ctx context.Context, username, folder, query string) (daemon.BrowsePage, error) {
	q := url.Values{"user": {username}, "folder": {folder}, "query": {query}}
	var out daemon.BrowsePage
	client := *c.http
	client.Timeout = 2 * time.Minute // Allow slow shares without leaving a stalled browse unbounded.
	err := c.doWith(ctx, http.MethodGet, "/v1/browse?"+q.Encode(), nil, &out, MaxBrowseBodySize, &client)
	return out, err
}
func (c *Client) BrowsePage(ctx context.Context, req daemon.BrowsePageRequest) (daemon.BrowsePage, error) {
	q := url.Values{"user": {req.Username}, "revision": {strconv.FormatUint(req.Revision, 10)}, "folder": {req.Folder}, "query": {req.Query}, "cursor": {strconv.Itoa(req.Cursor)}}
	var out daemon.BrowsePage
	err := c.do(ctx, http.MethodGet, "/v1/browse/page?"+q.Encode(), nil, &out, MaxBrowseBodySize)
	return out, err
}
func (c *Client) QueueBrowse(ctx context.Context, req daemon.BrowseDownloadRequest) (daemon.BrowseDownloadResult, error) {
	var out daemon.BrowseDownloadResult
	client := *c.http
	client.Timeout = 2 * time.Minute
	path := "/v1/browse/download"
	if req.Destination != "" {
		// Old daemons must reject this action, not silently ignore the new field.
		path = "/v1/browse/download-as"
	}
	err := c.doWith(ctx, http.MethodPost, path, req, &out, MaxBodySize, &client)
	return out, err
}
