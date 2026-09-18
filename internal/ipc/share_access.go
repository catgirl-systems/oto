package ipc

import (
	"context"
	"net/http"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) shareAccess(w http.ResponseWriter, r *http.Request) {
	var req daemon.ShareAccessRequest
	if err := decode(w, r, &req); err != nil {
		communityError(w, err)
		return
	}
	out, err := s.service.SetShareAccess(r.Context(), req)
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (c *Client) SetShareAccess(ctx context.Context, req daemon.ShareAccessRequest) (daemon.ShareAccessResult, error) {
	var out daemon.ShareAccessResult
	err := c.Do(ctx, http.MethodPut, "/v1/shares/access", req, &out)
	return out, err
}
