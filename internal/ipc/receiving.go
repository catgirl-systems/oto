package ipc

import (
	"context"
	"net/http"

	"github.com/catgirl-systems/oto/internal/daemon"
)

var (
	epReceiving    = endpoint[daemon.CommunityIdentity, daemon.ReceivingSettings]{http.MethodGet, "/v1/downloads/receiving"}
	epSetReceiving = endpoint[daemon.ReceivingSettingsRequest, daemon.ReceivingSettings]{http.MethodPut, "/v1/downloads/receiving"}
)

func (s *Server) registerReceivingRoutes() {
	route(s, scopeAuthed, epReceiving.op("get-receiving-settings", "Received-files settings"), func(ctx context.Context, input *identityParams) (*struct {
		Body daemon.ReceivingSettings
	}, error) {
		identity, err := input.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.ReceivingSettings(ctx, identity)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, epSetReceiving.op("set-receiving-settings", "Update received-files settings"), func(ctx context.Context, input *struct {
		Body daemon.ReceivingSettingsRequest
	}) (*struct {
		Body daemon.ReceivingSettings
	}, error) {
		out, err := s.service.SetReceivingSettings(ctx, input.Body)
		return communityBody(out, err)
	})
}

func (c *Client) ReceivingSettings(ctx context.Context, id daemon.CommunityIdentity) (daemon.ReceivingSettings, error) {
	return get(ctx, c, epReceiving, communityRoomValues(id))
}

func (c *Client) SetReceivingSettings(ctx context.Context, req daemon.ReceivingSettingsRequest) (daemon.ReceivingSettings, error) {
	return call(ctx, c, epSetReceiving, req)
}
