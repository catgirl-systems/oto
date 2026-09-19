package ipc

import (
	"context"
	"net/http"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/danielgtaylor/huma/v2"
)

func (s *Server) registerCommunityTextRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-text-tools", Method: http.MethodGet, Path: "/v1/community/text-tools",
		Summary: "Chat text tools", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Account string `query:"account" doc:"Community account"`
		Daemon  string `query:"daemon" doc:"Daemon identity"`
		Session string `query:"session" doc:"Session number"`
	}) (*struct {
		Body daemon.CommunityTextSettings
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityTextSettings(ctx, identity)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "set-text-tools", Method: http.MethodPut, Path: "/v1/community/text-tools",
		Summary: "Update chat text tools", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityTextSettings
	}) (*struct {
		Body daemon.CommunityTextSettings
	}, error) {
		out, err := s.service.SetCommunityTextSettings(ctx, input.Body)
		return communityBody(out, err)
	})
}

func (c *Client) CommunityTextSettings(ctx context.Context, id daemon.CommunityIdentity) (daemon.CommunityTextSettings, error) {
	q := communityRoomValues(id)
	var out daemon.CommunityTextSettings
	err := c.Do(ctx, http.MethodGet, "/v1/community/text-tools?"+q.Encode(), nil, &out)
	return out, err
}

func (c *Client) SetCommunityTextSettings(ctx context.Context, req daemon.CommunityTextSettings) (daemon.CommunityTextSettings, error) {
	var out daemon.CommunityTextSettings
	err := c.Do(ctx, http.MethodPut, "/v1/community/text-tools", req, &out)
	return out, err
}
