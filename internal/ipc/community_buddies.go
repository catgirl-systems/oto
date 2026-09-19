package ipc

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/danielgtaylor/huma/v2"
)

func (s *Server) registerCommunityBuddyRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "list-buddies", Method: http.MethodGet, Path: "/v1/community/buddies",
		Summary: "List buddies", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Account  string `query:"account" doc:"Community account"`
		Daemon   string `query:"daemon" doc:"Daemon identity"`
		Session  string `query:"session" doc:"Session number"`
		Username string `query:"username"`
		Query    string `query:"query"`
		Sort     string `query:"sort"`
		Cursor   string `query:"cursor"`
		Limit    int    `query:"limit"`
	}) (*struct {
		Body daemon.CommunityBuddiesPage
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityBuddies(ctx, daemon.CommunityBuddiesRequest{
			CommunityIdentity: identity, Username: input.Username, Query: input.Query, Cursor: input.Cursor, Sort: input.Sort, Limit: input.Limit,
		})
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "set-buddy", Method: http.MethodPut, Path: "/v1/community/buddies",
		Summary: "Add or update a buddy", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityBuddyRequest
	}) (*struct {
		Body daemon.CommunityBuddyResult
	}, error) {
		if input.Body.Remove {
			return nil, communityErr(errors.New("community: use DELETE to remove a buddy"))
		}
		out, err := s.service.SetCommunityBuddy(ctx, input.Body)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "remove-buddy", Method: http.MethodDelete, Path: "/v1/community/buddies",
		Summary: "Remove a buddy", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityBuddyRequest
	}) (*struct {
		Body daemon.CommunityBuddyResult
	}, error) {
		input.Body.Remove = true
		out, err := s.service.SetCommunityBuddy(ctx, input.Body)
		return communityBody(out, err)
	})
}

func (c *Client) CommunityBuddies(ctx context.Context, req daemon.CommunityBuddiesRequest) (daemon.CommunityBuddiesPage, error) {
	q := url.Values{"account": {req.Account}, "daemon": {req.Daemon}, "session": {strconv.FormatUint(req.Session, 10)}, "username": {req.Username}, "query": {req.Query}, "sort": {req.Sort}, "cursor": {req.Cursor}, "limit": {strconv.Itoa(req.Limit)}}
	var out daemon.CommunityBuddiesPage
	err := c.Do(ctx, http.MethodGet, "/v1/community/buddies?"+q.Encode(), nil, &out)
	return out, err
}

func (c *Client) SetCommunityBuddy(ctx context.Context, req daemon.CommunityBuddyRequest) (daemon.CommunityBuddyResult, error) {
	method := http.MethodPut
	if req.Remove {
		method = http.MethodDelete
	}
	var out daemon.CommunityBuddyResult
	err := c.Do(ctx, method, "/v1/community/buddies", req, &out)
	return out, err
}
