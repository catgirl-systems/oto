package ipc

import (
	"context"
	"net/http"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) shareAccess(w http.ResponseWriter, r *http.Request) {
	var req daemon.ShareAccessRequest
	if !decodeCommunity(w, r, &req) {
		return
	}
	out, err := s.service.SetShareAccess(r.Context(), req)
	communityResult(w, out, err)
}

func (c *Client) SetShareAccess(ctx context.Context, req daemon.ShareAccessRequest) (daemon.ShareAccessResult, error) {
	var out daemon.ShareAccessResult
	err := c.Do(ctx, http.MethodPut, "/v1/shares/access", req, &out)
	return out, err
}
