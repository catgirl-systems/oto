package ipc

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/danielgtaylor/huma/v2"
)

// communityErrors lists the statuses communityError can produce.
var communityErrors = []int{400, 404, 409, 503}

// identityParams documents the shared community session query parameters and
// doubles as the huma input type for identity-only GETs; identity()
// centralizes their parsing. huma does not parse parameters from embedded
// structs, so inputs with extra fields spell these out instead of embedding.
// Session stays a string so missing or malformed values keep the historical 400.
type identityParams struct {
	Account string `query:"account" doc:"Community account"`
	Daemon  string `query:"daemon" doc:"Daemon identity"`
	Session string `query:"session" doc:"Session number"`
}

func (p identityParams) identity() (daemon.CommunityIdentity, error) {
	session, err := strconv.ParseUint(p.Session, 10, 64)
	if err != nil {
		return daemon.CommunityIdentity{}, err
	}
	return daemon.CommunityIdentity{Account: p.Account, Daemon: p.Daemon, Session: session}, nil
}

// pagedIdentityQuery is the shared input for community GET pages whose only
// parameters are the session identity and cursor pagination. huma does not
// parse parameters from embedded structs, so the fields are spelled out.
type pagedIdentityQuery struct {
	Account string `query:"account" doc:"Community account"`
	Daemon  string `query:"daemon" doc:"Daemon identity"`
	Session string `query:"session" doc:"Session number"`
	Cursor  string `query:"cursor"`
	Limit   int    `query:"limit"`
}

func (p pagedIdentityQuery) identity() (daemon.CommunityIdentity, error) {
	return identityParams{Account: p.Account, Daemon: p.Daemon, Session: p.Session}.identity()
}

// endpoint pairs the HTTP method and path so server registration and client
// calls share one source of truth per route.
type endpoint[Req, Resp any] struct{ Method, Path string }

// op fills a community huma.Operation carrying the endpoint's method and path.
func (e endpoint[Req, Resp]) op(operationID, summary string) huma.Operation {
	return huma.Operation{OperationID: operationID, Method: e.Method, Path: e.Path, Summary: summary, Errors: communityErrors}
}

func get[Req, Resp any](ctx context.Context, c *Client, e endpoint[Req, Resp], q url.Values) (Resp, error) {
	var out Resp
	err := c.Do(ctx, e.Method, e.Path+"?"+q.Encode(), nil, &out)
	return out, err
}

func call[Req, Resp any](ctx context.Context, c *Client, e endpoint[Req, Resp], req Req) (Resp, error) {
	var out Resp
	err := c.Do(ctx, e.Method, e.Path, req, &out)
	return out, err
}

// setRemoveEndpoint picks the PUT or DELETE side of a set/remove pair.
func setRemoveEndpoint[Req, Resp any](set, remove endpoint[Req, Resp], removing bool) endpoint[Req, Resp] {
	if removing {
		return remove
	}
	return set
}

// communityErr maps a community error onto a huma status error.
func communityErr(err error) huma.StatusError {
	if err == nil {
		return nil
	}
	return huma.NewError(communityStatus(err), err.Error())
}

func communityStatus(err error) int {
	status := http.StatusBadRequest
	if errors.Is(err, daemon.ErrCommunitySession) || errors.Is(err, daemon.ErrCommunityMessageState) {
		status = http.StatusConflict
	}
	if errors.Is(err, daemon.ErrClosed) {
		status = http.StatusServiceUnavailable
	}
	if errors.Is(err, sql.ErrNoRows) {
		status = http.StatusNotFound
	}
	return status
}

// communityBody wraps a service result for huma, mapping failures through
// communityErr.
func communityBody[T any](out T, err error) (*struct {
	Body T
}, error) {
	if err != nil {
		return nil, communityErr(err)
	}
	return &struct {
		Body T
	}{out}, nil
}

// emptyOutput preserves the historical 200 with an empty JSON object.
type emptyOutput struct {
	Body struct{}
}

func (s *Server) registerCommunityRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-community-summary", Method: http.MethodGet, Path: "/v1/community",
		Summary: "Community summary", Errors: communityErrors,
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body daemon.CommunitySummary
	}, error) {
		return communityBody(s.service.CommunitySummary(ctx))
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "search-community-users", Method: http.MethodGet, Path: "/v1/community/users",
		Summary: "Search community users", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Account  string `query:"account" doc:"Community account"`
		Daemon   string `query:"daemon" doc:"Daemon identity"`
		Session  string `query:"session" doc:"Session number"`
		Cursor   string `query:"cursor"`
		Query    string `query:"query"`
		Username string `query:"username" doc:"Exact username lookup"`
		Limit    int    `query:"limit"`
	}) (*struct {
		Body daemon.CommunityUsersPage
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityUsers(ctx, daemon.CommunityUsersRequest{
			CommunityIdentity: identity, Cursor: input.Cursor, Query: input.Query, Username: input.Username, Limit: input.Limit,
		})
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "watch-community-users", Method: http.MethodPut, Path: "/v1/community/watches",
		Summary: "Watch or unwatch a user", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityWatchRequest
	}) (*emptyOutput, error) {
		if err := s.service.WatchCommunityUsers(input.Body.CommunityIdentity, input.Body.Frontend, input.Body.Users); err != nil {
			return nil, communityErr(err)
		}
		return &emptyOutput{}, nil
	})
}

func (c *Client) CommunitySummary(ctx context.Context) (daemon.CommunitySummary, error) {
	var out daemon.CommunitySummary
	err := c.Do(ctx, http.MethodGet, "/v1/community", nil, &out)
	return out, err
}

func (c *Client) CommunityUsers(ctx context.Context, req daemon.CommunityUsersRequest) (daemon.CommunityUsersPage, error) {
	q := url.Values{"account": {req.Account}, "daemon": {req.Daemon}, "session": {strconv.FormatUint(req.Session, 10)}, "cursor": {req.Cursor}, "query": {req.Query}, "limit": {strconv.Itoa(req.Limit)}}
	q.Set("username", req.Username)
	var out daemon.CommunityUsersPage
	err := c.Do(ctx, http.MethodGet, "/v1/community/users?"+q.Encode(), nil, &out)
	return out, err
}

func (c *Client) WatchCommunityUsers(ctx context.Context, req daemon.CommunityWatchRequest) error {
	return c.Do(ctx, http.MethodPut, "/v1/community/watches", req, nil)
}
