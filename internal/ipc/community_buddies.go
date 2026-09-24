package ipc

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
)

var (
	epCommunityBuddies     = endpoint[daemon.CommunityBuddiesRequest, daemon.CommunityBuddiesPage]{http.MethodGet, "/v1/community/buddies"}
	epSetCommunityBuddy    = endpoint[daemon.CommunityBuddyRequest, daemon.CommunityBuddyResult]{http.MethodPut, "/v1/community/buddies"}
	epRemoveCommunityBuddy = endpoint[daemon.CommunityBuddyRequest, daemon.CommunityBuddyResult]{http.MethodDelete, "/v1/community/buddies"}
)

func (s *Server) registerCommunityBuddyRoutes() {
	route(s, scopeAuthed, epCommunityBuddies.op("list-buddies", "List buddies"), func(ctx context.Context, input *struct {
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
	route(s, scopeAuthed, epSetCommunityBuddy.op("set-buddy", "Add or update a buddy"), func(ctx context.Context, input *struct {
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
	route(s, scopeAuthed, epRemoveCommunityBuddy.op("remove-buddy", "Remove a buddy"), func(ctx context.Context, input *struct {
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
	q := communityRoomValues(req.CommunityIdentity)
	q.Set("username", req.Username)
	q.Set("query", req.Query)
	q.Set("sort", req.Sort)
	q.Set("cursor", req.Cursor)
	q.Set("limit", strconv.Itoa(req.Limit))
	return get(ctx, c, epCommunityBuddies, q)
}

func (c *Client) SetCommunityBuddy(ctx context.Context, req daemon.CommunityBuddyRequest) (daemon.CommunityBuddyResult, error) {
	return call(ctx, c, setRemoveEndpoint(epSetCommunityBuddy, epRemoveCommunityBuddy, req.Remove), req)
}
