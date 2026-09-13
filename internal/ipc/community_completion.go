package ipc

import (
	"context"
	"net/http"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) communityCompletion(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunityCompletionRequest
	if err := decode(w, r, &req); err != nil {
		communityError(w, err)
		return
	}
	out, err := s.service.CompleteCommunity(r.Context(), req)
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (c *Client) CompleteCommunity(ctx context.Context, req daemon.CommunityCompletionRequest) (daemon.CommunityCompletion, error) {
	var out daemon.CommunityCompletion
	err := c.Do(ctx, http.MethodPost, "/v1/community/completion", req, &out)
	return out, err
}
