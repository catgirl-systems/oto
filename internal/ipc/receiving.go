package ipc

import (
	"context"
	"net/http"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/danielgtaylor/huma/v2"
)

func (s *Server) registerReceivingRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-receiving-settings", Method: http.MethodGet, Path: "/v1/downloads/receiving",
		Summary: "Received-files settings", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Account string `query:"account" doc:"Community account"`
		Daemon  string `query:"daemon" doc:"Daemon identity"`
		Session string `query:"session" doc:"Session number"`
	}) (*struct {
		Body daemon.ReceivingSettings
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.ReceivingSettings(ctx, identity)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "set-receiving-settings", Method: http.MethodPut, Path: "/v1/downloads/receiving",
		Summary: "Update received-files settings", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.ReceivingSettingsRequest
	}) (*struct {
		Body daemon.ReceivingSettings
	}, error) {
		out, err := s.service.SetReceivingSettings(ctx, input.Body)
		return communityBody(out, err)
	})
}

func (c *Client) ReceivingSettings(ctx context.Context, id daemon.CommunityIdentity) (daemon.ReceivingSettings, error) {
	q := communityRoomValues(id)
	var out daemon.ReceivingSettings
	err := c.Do(ctx, http.MethodGet, "/v1/downloads/receiving?"+q.Encode(), nil, &out)
	return out, err
}

func (c *Client) SetReceivingSettings(ctx context.Context, req daemon.ReceivingSettingsRequest) (daemon.ReceivingSettings, error) {
	var out daemon.ReceivingSettings
	err := c.Do(ctx, http.MethodPut, "/v1/downloads/receiving", req, &out)
	return out, err
}
