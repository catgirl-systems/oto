package ipc

import (
	"context"
	"net/http"

	"github.com/catgirl-systems/oto/internal/daemon"
)

var (
	epCommunityAway    = endpoint[daemon.CommunityIdentity, daemon.CommunityAwaySettings]{http.MethodGet, "/v1/community/away"}
	epSetCommunityAway = endpoint[daemon.CommunityAwaySettingsRequest, daemon.CommunityAwaySettings]{http.MethodPut, "/v1/community/away"}
)

func (s *Server) registerCommunityAwayRoutes() {
	route(s, scopeAuthed, epCommunityAway.op("get-away-settings", "Automatic away settings"), func(ctx context.Context, input *identityParams) (*struct {
		Body daemon.CommunityAwaySettings
	}, error) {
		identity, err := input.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityAwaySettings(ctx, identity)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, epSetCommunityAway.op("set-away-settings", "Update automatic away settings"), func(ctx context.Context, input *struct {
		Body daemon.CommunityAwaySettingsRequest
	}) (*struct {
		Body daemon.CommunityAwaySettings
	}, error) {
		out, err := s.service.SetCommunityAwaySettings(ctx, input.Body)
		return communityBody(out, err)
	})
}

func (c *Client) CommunityAwaySettings(ctx context.Context, id daemon.CommunityIdentity) (daemon.CommunityAwaySettings, error) {
	return get(ctx, c, epCommunityAway, communityRoomValues(id))
}

func (c *Client) SetCommunityAwaySettings(ctx context.Context, req daemon.CommunityAwaySettingsRequest) (daemon.CommunityAwaySettings, error) {
	return call(ctx, c, epSetCommunityAway, req)
}
