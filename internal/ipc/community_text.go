package ipc

import (
	"context"
	"net/http"

	"github.com/catgirl-systems/oto/internal/daemon"
)

var (
	epCommunityText    = endpoint[daemon.CommunityIdentity, daemon.CommunityTextSettings]{http.MethodGet, "/v1/community/text-tools"}
	epSetCommunityText = endpoint[daemon.CommunityTextSettings, daemon.CommunityTextSettings]{http.MethodPut, "/v1/community/text-tools"}
)

func (s *Server) registerCommunityTextRoutes() {
	route(s, scopeAuthed, epCommunityText.op("get-text-tools", "Chat text tools"), func(ctx context.Context, input *identityParams) (*struct {
		Body daemon.CommunityTextSettings
	}, error) {
		identity, err := input.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityTextSettings(ctx, identity)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, epSetCommunityText.op("set-text-tools", "Update chat text tools"), func(ctx context.Context, input *struct {
		Body daemon.CommunityTextSettings
	}) (*struct {
		Body daemon.CommunityTextSettings
	}, error) {
		out, err := s.service.SetCommunityTextSettings(ctx, input.Body)
		return communityBody(out, err)
	})
}

func (c *Client) CommunityTextSettings(ctx context.Context, id daemon.CommunityIdentity) (daemon.CommunityTextSettings, error) {
	return get(ctx, c, epCommunityText, communityRoomValues(id))
}

func (c *Client) SetCommunityTextSettings(ctx context.Context, req daemon.CommunityTextSettings) (daemon.CommunityTextSettings, error) {
	return call(ctx, c, epSetCommunityText, req)
}
