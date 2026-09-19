package ipc

import (
	"context"
	"net/http"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/danielgtaylor/huma/v2"
)

func (s *Server) registerCommunityAwayRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-away-settings", Method: http.MethodGet, Path: "/v1/community/away",
		Summary: "Automatic away settings", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Account string `query:"account" doc:"Community account"`
		Daemon  string `query:"daemon" doc:"Daemon identity"`
		Session string `query:"session" doc:"Session number"`
	}) (*struct {
		Body daemon.CommunityAwaySettings
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityAwaySettings(ctx, identity)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "set-away-settings", Method: http.MethodPut, Path: "/v1/community/away",
		Summary: "Update automatic away settings", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityAwaySettingsRequest
	}) (*struct {
		Body daemon.CommunityAwaySettings
	}, error) {
		out, err := s.service.SetCommunityAwaySettings(ctx, input.Body)
		return communityBody(out, err)
	})
}

func (c *Client) CommunityAwaySettings(ctx context.Context, id daemon.CommunityIdentity) (daemon.CommunityAwaySettings, error) {
	q := communityRoomValues(id)
	var out daemon.CommunityAwaySettings
	err := c.Do(ctx, http.MethodGet, "/v1/community/away?"+q.Encode(), nil, &out)
	return out, err
}

func (c *Client) SetCommunityAwaySettings(ctx context.Context, req daemon.CommunityAwaySettingsRequest) (daemon.CommunityAwaySettings, error) {
	var out daemon.CommunityAwaySettings
	err := c.Do(ctx, http.MethodPut, "/v1/community/away", req, &out)
	return out, err
}
