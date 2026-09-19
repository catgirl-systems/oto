package ipc

import (
	"context"
	"net/http"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/danielgtaylor/huma/v2"
)

func (s *Server) registerCommunityDiscoveryRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-discovery", Method: http.MethodGet, Path: "/v1/community/discovery",
		Summary: "Recommended users and rooms", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Account  string `query:"account" doc:"Community account"`
		Daemon   string `query:"daemon" doc:"Daemon identity"`
		Session  string `query:"session" doc:"Session number"`
		Kind     string `query:"kind"`
		Target   string `query:"target"`
		Frontend string `query:"frontend"`
		Cursor   string `query:"cursor"`
		Limit    int    `query:"limit"`
	}) (*struct {
		Body daemon.CommunityDiscoveryPage
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityDiscovery(ctx, daemon.CommunityDiscoveryRequest{
			CommunityIdentity: identity, Kind: input.Kind, Target: input.Target, Frontend: input.Frontend, Cursor: input.Cursor, Limit: input.Limit,
		})
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "refresh-discovery", Method: http.MethodPost, Path: "/v1/community/discovery",
		Summary: "Refresh recommendations", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityDiscoveryRequest
	}) (*struct {
		Body daemon.CommunityDiscoveryPage
	}, error) {
		out, err := s.service.StartCommunityDiscovery(ctx, input.Body)
		return communityBody(out, err)
	})
}

func (c *Client) CommunityDiscovery(ctx context.Context, req daemon.CommunityDiscoveryRequest) (daemon.CommunityDiscoveryPage, error) {
	q := communityRoomValues(req.CommunityIdentity)
	q.Set("kind", req.Kind)
	q.Set("target", req.Target)
	q.Set("frontend", req.Frontend)
	q.Set("cursor", req.Cursor)
	q.Set("limit", strconv.Itoa(req.Limit))
	var out daemon.CommunityDiscoveryPage
	err := c.Do(ctx, http.MethodGet, "/v1/community/discovery?"+q.Encode(), nil, &out)
	return out, err
}

func (c *Client) StartCommunityDiscovery(ctx context.Context, req daemon.CommunityDiscoveryRequest) (daemon.CommunityDiscoveryPage, error) {
	var out daemon.CommunityDiscoveryPage
	err := c.Do(ctx, http.MethodPost, "/v1/community/discovery", req, &out)
	return out, err
}
