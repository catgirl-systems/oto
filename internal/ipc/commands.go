package ipc

import (
	"context"
	"net/http"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) commands(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommandRequest
	if !decodeCommunity(w, r, &req) {
		return
	}
	out, err := s.service.RunCommand(r.Context(), req)
	communityResult(w, out, err)
}
func (c *Client) RunCommand(ctx context.Context, req daemon.CommandRequest) (daemon.CommandResult, error) {
	var out daemon.CommandResult
	err := c.Do(ctx, http.MethodPost, "/v1/commands", req, &out)
	return out, err
}
