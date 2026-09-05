package ipc

import (
	"context"
	"github.com/catgirl-systems/oto/internal/daemon"
	"net/http"
)

func (s *Server) forceDownloads(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := decode(w, r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	result, err := s.service.ForceDownloads(req.IDs)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, result)
}
func (c *Client) ForceDownloads(ctx context.Context, ids []string) (daemon.UploadActionResult, error) {
	var result daemon.UploadActionResult
	err := c.Do(ctx, "POST", "/v1/downloads/force", map[string]any{"ids": ids}, &result)
	return result, err
}
