package ipc

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
)

var (
	epCommunityInterests      = endpoint[daemon.CommunityInterestsRequest, daemon.CommunityInterestsPage]{http.MethodGet, "/v1/community/interests"}
	epSetCommunityInterest    = endpoint[daemon.CommunityInterestRequest, daemon.CommunityInterestResult]{http.MethodPut, "/v1/community/interests"}
	epRemoveCommunityInterest = endpoint[daemon.CommunityInterestRequest, daemon.CommunityInterestResult]{http.MethodDelete, "/v1/community/interests"}
	epCommunitySelfProfile    = endpoint[daemon.CommunitySelfProfile, daemon.CommunitySelfProfile]{http.MethodGet, "/v1/community/profile/self"}
	epSetCommunitySelfProfile = endpoint[daemon.CommunitySelfProfile, daemon.CommunitySelfProfile]{http.MethodPut, "/v1/community/profile/self"}
)

func (s *Server) registerCommunityInterestRoutes() {
	route(s, scopeAuthed, epCommunityInterests.op("list-interests", "List interests"), func(ctx context.Context, input *struct {
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
	route(s, scopeAuthed, epSetCommunityInterest.op("set-interest", "Add an interest"), func(ctx context.Context, input *struct {
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
	route(s, scopeAuthed, epRemoveCommunityInterest.op("remove-interest", "Remove an interest"), func(ctx context.Context, input *struct {
		Body daemon.CommunityInterestRequest
	}) (*struct {
		Body daemon.CommunityInterestResult
	}, error) {
		input.Body.Remove = true
		out, err := s.service.SetCommunityInterest(ctx, input.Body)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, epCommunitySelfProfile.op("get-own-profile", "Own community profile"), func(ctx context.Context, input *identityParams) (*struct {
		Body daemon.CommunitySelfProfile
	}, error) {
		identity, err := input.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunitySelfProfile(ctx, identity)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, epSetCommunitySelfProfile.op("set-own-profile", "Update own profile"), func(ctx context.Context, input *struct {
		Body daemon.CommunitySelfProfile
	}) (*struct {
		Body daemon.CommunitySelfProfile
	}, error) {
		out, err := s.service.SetCommunitySelfProfile(ctx, input.Body)
		return communityBody(out, err)
	})
}

func (c *Client) CommunityInterests(ctx context.Context, req daemon.CommunityInterestsRequest) (daemon.CommunityInterestsPage, error) {
	q := communityRoomValues(req.CommunityIdentity)
	q.Set("cursor", req.Cursor)
	q.Set("query", req.Query)
	q.Set("limit", strconv.Itoa(req.Limit))
	return get(ctx, c, epCommunityInterests, q)
}

func (c *Client) SetCommunityInterest(ctx context.Context, req daemon.CommunityInterestRequest) (daemon.CommunityInterestResult, error) {
	return call(ctx, c, setRemoveEndpoint(epSetCommunityInterest, epRemoveCommunityInterest, req.Remove), req)
}

func (c *Client) CommunitySelfProfile(ctx context.Context, identity daemon.CommunityIdentity) (daemon.CommunitySelfProfile, error) {
	return get(ctx, c, epCommunitySelfProfile, communityRoomValues(identity))
}

func (c *Client) SetCommunitySelfProfile(ctx context.Context, req daemon.CommunitySelfProfile) (daemon.CommunitySelfProfile, error) {
	return call(ctx, c, epSetCommunitySelfProfile, req)
}
