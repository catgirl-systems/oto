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
	"github.com/danielgtaylor/huma/v2"
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

// statsFilterParams carries the raw statistics query parameters; most are
// parsed leniently (empty means unset), which huma's typed params cannot
// express for optional numerics and dual-format dates.
type statsFilterParams struct {
	Account   string `query:"account" doc:"Stats account to filter by"`
	Peer      string `query:"peer" doc:"Peer to filter by"`
	Direction string `query:"direction" doc:"Transfer direction to filter by"`
	Session   string `query:"session" doc:"Session to filter by"`
	Cursor    string `query:"cursor" doc:"Pagination cursor"`
	Sort      string `query:"sort" doc:"Sort order"`
	Limit     string `query:"limit" doc:"Maximum rows to return"`
	Bins      string `query:"bins" doc:"Histogram bin count"`
	From      string `query:"from" doc:"Range start, RFC 3339 or date"`
	To        string `query:"to" doc:"Range end, RFC 3339 or date"`
	Outcome   string `query:"outcome" doc:"Transfer-log outcomes, comma separated"`
}

func (p statsFilterParams) filter(transferLog bool) (stats.Filter, error) {
	f := stats.Filter{Account: p.Account, Peer: p.Peer, Direction: p.Direction, Session: p.Session, Cursor: p.Cursor, Sort: p.Sort}
	for _, number := range []struct {
		value  string
		target *int
	}{{p.Limit, &f.Limit}, {p.Bins, &f.Bins}} {
		if number.value == "" {
			continue
		}
		n, err := strconv.Atoi(number.value)
		if err != nil {
			return f, err
		}
		*number.target = n
	}
	for _, bound := range []struct {
		value  string
		target *time.Time
	}{{p.From, &f.From}, {p.To, &f.To}} {
		if bound.value == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, bound.value)
		if err != nil {
			at, err = time.Parse(time.DateOnly, bound.value)
		}
		if err != nil {
			return f, err
		}
		*bound.target = at
	}
	if p.Outcome != "" {
		if !transferLog {
			return f, errors.New("outcome filters apply only to the transfer log")
		}
		f.Kinds = strings.Split(p.Outcome, ",")
	}
	return f, stats.ValidateFilter(f)
}

func statsServiceError(err error) huma.StatusError {
	status := 503
	if errors.Is(err, stats.ErrInvalidCursor) {
		status = 400
	}
	return errStatus(status, err)
}

func (s *Server) registerStatsRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-stats", Method: http.MethodGet, Path: "/v1/stats",
		Summary: "Transfer statistics overview", Errors: []int{400, 503},
	}, func(ctx context.Context, input *statsFilterParams) (*struct {
		Body daemon.StatsOverview
	}, error) {
		f, err := input.filter(false)
		if err != nil {
			return nil, errStatus(400, err)
		}
		overview, err := s.service.Statistics(f)
		if err != nil {
			return nil, statsServiceError(err)
		}
		return &struct {
			Body daemon.StatsOverview
		}{overview}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-stats-series", Method: http.MethodGet, Path: "/v1/stats/series",
		Summary: "Daily transfer statistics series", Errors: []int{400, 503},
	}, func(ctx context.Context, input *statsFilterParams) (*struct {
		Body []stats.Daily
	}, error) {
		f, err := input.filter(false)
		if err != nil {
			return nil, errStatus(400, err)
		}
		series, err := s.service.StatsSeries(f)
		if err != nil {
			return nil, statsServiceError(err)
		}
		return &struct {
			Body []stats.Daily
		}{series}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-stats-peers", Method: http.MethodGet, Path: "/v1/stats/peers",
		Summary: "Per-peer transfer statistics", Errors: []int{400, 503},
	}, func(ctx context.Context, input *statsFilterParams) (*struct {
		Body stats.PeerPage
	}, error) {
		f, err := input.filter(false)
		if err != nil {
			return nil, errStatus(400, err)
		}
		peers, err := s.service.StatsPeers(f)
		if err != nil {
			return nil, statsServiceError(err)
		}
		return &struct {
			Body stats.PeerPage
		}{peers}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-transfer-log", Method: http.MethodGet, Path: "/v1/transfer-log",
		Summary: "Transfer log entries", Errors: []int{400, 503},
	}, func(ctx context.Context, input *statsFilterParams) (*struct {
		Body stats.LogPage
	}, error) {
		f, err := input.filter(true)
		if err != nil {
			return nil, errStatus(400, err)
		}
		log, err := s.service.TransferLog(f)
		if err != nil {
			return nil, statsServiceError(err)
		}
		return &struct {
			Body stats.LogPage
		}{log}, nil
	})
	for _, prune := range []struct {
		id      string
		apply   bool
		summary string
	}{{"preview-stats-prune", false, "Preview statistics pruning"}, {"stats-prune", true, "Prune statistics history"}} {
		apply := prune.apply
		route(s, scopeAuthed, huma.Operation{
			OperationID: prune.id, Method: http.MethodPost, Path: "/v1/stats/prune" + map[bool]string{false: "/preview", true: ""}[apply],
			Summary: prune.summary, Errors: []int{400},
		}, func(ctx context.Context, input *struct {
			Body PruneRequest
		}) (*struct {
			Body stats.PruneResult
		}, error) {
			result, err := s.service.PruneStatistics(input.Body.Cutoff, input.Body.Logs, input.Body.Daily, apply)
			if err != nil {
				return nil, errStatus(400, err)
			}
			return &struct {
				Body stats.PruneResult
			}{result}, nil
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
