package ipc

import (
	"context"
	"net/http"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) commands(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommandRequest
	if err := decode(w, r, &req); err != nil {
		communityError(w, err)
		return
	}
	out, err := s.service.RunCommand(r.Context(), req)
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (c *Client) RunCommand(ctx context.Context, req daemon.CommandRequest) (daemon.CommandResult, error) {
	var out daemon.CommandResult
	err := c.Do(ctx, http.MethodPost, "/v1/commands", req, &out)
	return out, err
}
