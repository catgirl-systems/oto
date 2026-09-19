package ipc

import (
	"context"
	"net/http"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/danielgtaylor/huma/v2"
)

func (s *Server) registerCommunityActivityRoute() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "report-activity", Method: http.MethodPost, Path: "/v1/community/activity",
		Summary: "Report chat activity", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityIdentity
	}) (*struct {
		Body daemon.CommunityActivity
	}, error) {
		out, err := s.service.CommunityActivity(ctx, input.Body)
		return communityBody(out, err)
	})
}

func (c *Client) CommunityActivity(ctx context.Context, id daemon.CommunityIdentity) (daemon.CommunityActivity, error) {
	var out daemon.CommunityActivity
	err := c.Do(ctx, http.MethodPost, "/v1/community/activity", id, &out)
	return out, err
}
