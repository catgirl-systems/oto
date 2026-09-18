package ipc

import (
	"context"
	"net/http"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) communityCompletion(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunityCompletionRequest
	if !decodeCommunity(w, r, &req) {
		return
	}
	out, err := s.service.CompleteCommunity(r.Context(), req)
	communityResult(w, out, err)
}
func (c *Client) CompleteCommunity(ctx context.Context, req daemon.CommunityCompletionRequest) (daemon.CommunityCompletion, error) {
	var out daemon.CommunityCompletion
	err := c.Do(ctx, http.MethodPost, "/v1/community/completion", req, &out)
	return out, err
}
