package ipc

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) registerCommunity(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/community", s.communitySummary)
	mux.HandleFunc("GET /v1/community/users", s.communityUsers)
	mux.HandleFunc("PUT /v1/community/watches", s.communityWatches)
}

func communityError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, daemon.ErrCommunitySession) {
		status = http.StatusConflict
	}
	if errors.Is(err, daemon.ErrClosed) {
		status = http.StatusServiceUnavailable
	}
	writeErr(w, status, err)
}

func (s *Server) communitySummary(w http.ResponseWriter, r *http.Request) {
	out, err := s.service.CommunitySummary(r.Context())
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) communityUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	session, err := strconv.ParseUint(q.Get("session"), 10, 64)
	if err != nil {
		communityError(w, err)
		return
	}
	limit := 200
	if q.Get("limit") != "" {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil {
			communityError(w, err)
			return
		}
	}
	out, err := s.service.CommunityUsers(r.Context(), daemon.CommunityUsersRequest{
		CommunityIdentity: daemon.CommunityIdentity{Account: q.Get("account"), Daemon: q.Get("daemon"), Session: session},
		Cursor:            q.Get("cursor"), Query: q.Get("query"), Username: q.Get("username"), Limit: limit,
	})
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) communityWatches(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunityWatchRequest
	if err := decode(w, r, &req); err != nil {
		communityError(w, err)
		return
	}
	if err := s.service.WatchCommunityUsers(req.CommunityIdentity, req.Frontend, req.Users); err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
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
