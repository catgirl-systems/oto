package ipc

import (
	"context"
	"net/http"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/danielgtaylor/huma/v2"
)

func (s *Server) registerCommandRoute() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "run-command", Method: http.MethodPost, Path: "/v1/commands",
		Summary: "Run a social command", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommandRequest
	}) (*struct {
		Body daemon.CommandResult
	}, error) {
		out, err := s.service.RunCommand(ctx, input.Body)
		return communityBody(out, err)
	})
}

func (c *Client) RunCommand(ctx context.Context, req daemon.CommandRequest) (daemon.CommandResult, error) {
	var out daemon.CommandResult
	err := c.Do(ctx, http.MethodPost, "/v1/commands", req, &out)
	return out, err
}
