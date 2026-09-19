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

func (s *Server) registerCommunityInterestRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "list-interests", Method: http.MethodGet, Path: "/v1/community/interests",
		Summary: "List interests", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Account string `query:"account" doc:"Community account"`
		Daemon  string `query:"daemon" doc:"Daemon identity"`
		Session string `query:"session" doc:"Session number"`
		Cursor  string `query:"cursor"`
		Query   string `query:"query"`
		Limit   int    `query:"limit"`
	}) (*struct {
		Body daemon.CommunityInterestsPage
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityInterests(ctx, daemon.CommunityInterestsRequest{
			CommunityIdentity: identity, Cursor: input.Cursor, Query: input.Query, Limit: input.Limit,
		})
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "set-interest", Method: http.MethodPut, Path: "/v1/community/interests",
		Summary: "Add an interest", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityInterestRequest
	}) (*struct {
		Body daemon.CommunityInterestResult
	}, error) {
		if input.Body.Remove {
			return nil, communityErr(errors.New("community: use DELETE to remove an interest"))
		}
		out, err := s.service.SetCommunityInterest(ctx, input.Body)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "remove-interest", Method: http.MethodDelete, Path: "/v1/community/interests",
		Summary: "Remove an interest", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityInterestRequest
	}) (*struct {
		Body daemon.CommunityInterestResult
	}, error) {
		input.Body.Remove = true
		out, err := s.service.SetCommunityInterest(ctx, input.Body)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-own-profile", Method: http.MethodGet, Path: "/v1/community/profile/self",
		Summary: "Own community profile", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Account string `query:"account" doc:"Community account"`
		Daemon  string `query:"daemon" doc:"Daemon identity"`
		Session string `query:"session" doc:"Session number"`
	}) (*struct {
		Body daemon.CommunitySelfProfile
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunitySelfProfile(ctx, identity)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "set-own-profile", Method: http.MethodPut, Path: "/v1/community/profile/self",
		Summary: "Update own profile", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunitySelfProfile
	}) (*struct {
		Body daemon.CommunitySelfProfile
	}, error) {
		out, err := s.service.SetCommunitySelfProfile(ctx, input.Body)
		return communityBody(out, err)
	})
}

func (c *Client) CommunityInterests(ctx context.Context, req daemon.CommunityInterestsRequest) (daemon.CommunityInterestsPage, error) {
	q := url.Values{"account": {req.Account}, "daemon": {req.Daemon}, "session": {strconv.FormatUint(req.Session, 10)}, "cursor": {req.Cursor}, "query": {req.Query}, "limit": {strconv.Itoa(req.Limit)}}
	var out daemon.CommunityInterestsPage
	err := c.Do(ctx, http.MethodGet, "/v1/community/interests?"+q.Encode(), nil, &out)
	return out, err
}

func (c *Client) SetCommunityInterest(ctx context.Context, req daemon.CommunityInterestRequest) (daemon.CommunityInterestResult, error) {
	method := http.MethodPut
	if req.Remove {
		method = http.MethodDelete
	}
	var out daemon.CommunityInterestResult
	err := c.Do(ctx, method, "/v1/community/interests", req, &out)
	return out, err
}

func (c *Client) CommunitySelfProfile(ctx context.Context, identity daemon.CommunityIdentity) (daemon.CommunitySelfProfile, error) {
	q := communityRoomValues(identity)
	var out daemon.CommunitySelfProfile
	err := c.Do(ctx, http.MethodGet, "/v1/community/profile/self?"+q.Encode(), nil, &out)
	return out, err
}

func (c *Client) SetCommunitySelfProfile(ctx context.Context, req daemon.CommunitySelfProfile) (daemon.CommunitySelfProfile, error) {
	var out daemon.CommunitySelfProfile
	err := c.Do(ctx, http.MethodPut, "/v1/community/profile/self", req, &out)
	return out, err
}
