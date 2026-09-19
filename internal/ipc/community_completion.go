package ipc

import (
	"context"
	"net/http"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/danielgtaylor/huma/v2"
)

func (s *Server) registerCommunityCompletionRoute() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "chat-completion", Method: http.MethodPost, Path: "/v1/community/completion",
		Summary: "Chat completion suggestions", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityCompletionRequest
	}) (*struct {
		Body daemon.CommunityCompletion
	}, error) {
		out, err := s.service.CompleteCommunity(ctx, input.Body)
		return communityBody(out, err)
	})
}

func (c *Client) CompleteCommunity(ctx context.Context, req daemon.CommunityCompletionRequest) (daemon.CommunityCompletion, error) {
	var out daemon.CommunityCompletion
	err := c.Do(ctx, http.MethodPost, "/v1/community/completion", req, &out)
	return out, err
}
