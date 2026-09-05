package ipc

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/stats"
)

type PruneRequest struct {
	Cutoff time.Time `json:"cutoff"`
	Logs   bool      `json:"logs"`
	Daily  bool      `json:"daily"`
}

func statsQuery(f stats.Filter) string {
	q := url.Values{"account": {f.Account}, "peer": {f.Peer}, "direction": {f.Direction}, "session": {f.Session}, "cursor": {f.Cursor}}
	q.Set("sort", f.Sort)
	if f.Limit != 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	if f.Bins != 0 {
		q.Set("bins", strconv.Itoa(f.Bins))
	}
	if !f.From.IsZero() {
		q.Set("from", f.From.Format(time.RFC3339Nano))
	}
	if !f.To.IsZero() {
		q.Set("to", f.To.Format(time.RFC3339Nano))
	}
	if len(f.Kinds) > 0 {
		q.Set("outcome", strings.Join(f.Kinds, ","))
	}
	return "?" + q.Encode()
}
func parseStatsFilter(r *http.Request) (stats.Filter, error) {
	q := r.URL.Query()
	f := stats.Filter{Account: q.Get("account"), Peer: q.Get("peer"), Direction: q.Get("direction"), Session: q.Get("session"), Cursor: q.Get("cursor")}
	f.Sort = q.Get("sort")
	for name, p := range map[string]*int{"limit": &f.Limit, "bins": &f.Bins} {
		if text := q.Get(name); text != "" {
			n, err := strconv.Atoi(text)
			if err != nil {
				return f, err
			}
			*p = n
		}
	}
	for name, p := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if text := q.Get(name); text != "" {
			at, err := time.Parse(time.RFC3339Nano, text)
			if err != nil {
				at, err = time.Parse(time.DateOnly, text)
			}
			if err != nil {
				return f, err
			}
			*p = at
		}
	}
	if text := q.Get("outcome"); text != "" {
		if r.URL.Path != "/v1/transfer-log" {
			return f, errors.New("outcome filters apply only to the transfer log")
		}
		f.Kinds = strings.Split(text, ",")
	}
	return f, stats.ValidateFilter(f)
}
func (s *Server) registerStats(mux *http.ServeMux) {
	for _, path := range []string{"/v1/stats", "/v1/stats/series", "/v1/stats/peers", "/v1/transfer-log"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			f, err := parseStatsFilter(r)
			if err != nil {
				writeErr(w, 400, err)
				return
			}
			var result any
			switch r.URL.Path {
			case "/v1/stats":
				result, err = s.service.Statistics(f)
			case "/v1/stats/series":
				result, err = s.service.StatsSeries(f)
			case "/v1/stats/peers":
				result, err = s.service.StatsPeers(f)
			case "/v1/transfer-log":
				result, err = s.service.TransferLog(f)
			}
			if err != nil {
				status := 503
				if errors.Is(err, stats.ErrInvalidCursor) {
					status = 400
				}
				writeErr(w, status, err)
				return
			}
			writeJSON(w, 200, result)
		})
	}
	for _, path := range []string{"/v1/stats/prune/preview", "/v1/stats/prune"} {
		mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
			var req PruneRequest
			if err := decode(w, r, &req); err != nil {
				writeErr(w, 400, err)
				return
			}
			result, err := s.service.PruneStatistics(req.Cutoff, req.Logs, req.Daily, r.URL.Path == "/v1/stats/prune")
			if err != nil {
				writeErr(w, 400, err)
				return
			}
			writeJSON(w, 200, result)
		})
	}
}
func (c *Client) Statistics(ctx context.Context, f stats.Filter) (daemon.StatsOverview, error) {
	var out daemon.StatsOverview
	err := c.Do(ctx, "GET", "/v1/stats"+statsQuery(f), nil, &out)
	return out, err
}
func (c *Client) StatsSeries(ctx context.Context, f stats.Filter) ([]stats.Daily, error) {
	var out []stats.Daily
	err := c.Do(ctx, "GET", "/v1/stats/series"+statsQuery(f), nil, &out)
	return out, err
}
func (c *Client) StatsPeers(ctx context.Context, f stats.Filter) (stats.PeerPage, error) {
	var out stats.PeerPage
	err := c.Do(ctx, "GET", "/v1/stats/peers"+statsQuery(f), nil, &out)
	return out, err
}
func (c *Client) TransferLog(ctx context.Context, f stats.Filter) (stats.LogPage, error) {
	var out stats.LogPage
	err := c.Do(ctx, "GET", "/v1/transfer-log"+statsQuery(f), nil, &out)
	return out, err
}
func (c *Client) PruneStatistics(ctx context.Context, req PruneRequest, apply bool) (stats.PruneResult, error) {
	var out stats.PruneResult
	path := "/v1/stats/prune"
	if !apply {
		path += "/preview"
	}
	err := c.Do(ctx, "POST", path, req, &out)
	return out, err
}
