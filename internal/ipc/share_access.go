package ipc

import (
	"context"
	"net/http"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/danielgtaylor/huma/v2"
)

func (s *Server) registerShareAccessRoute() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "set-share-access", Method: http.MethodPut, Path: "/v1/shares/access",
		Summary: "Change share access rules", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.ShareAccessRequest
	}) (*struct {
		Body daemon.ShareAccessResult
	}, error) {
		out, err := s.service.SetShareAccess(ctx, input.Body)
		return communityBody(out, err)
	})
}

func (c *Client) SetShareAccess(ctx context.Context, req daemon.ShareAccessRequest) (daemon.ShareAccessResult, error) {
	var out daemon.ShareAccessResult
	err := c.Do(ctx, http.MethodPut, "/v1/shares/access", req, &out)
	return out, err
}
