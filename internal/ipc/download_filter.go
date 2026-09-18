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
	if !decodeBody(w, r, &req) {
		return
	}
	result, err := s.service.ForceDownloads(req.IDs)
	badRequestResult(w, result, err)
}
func (c *Client) ForceDownloads(ctx context.Context, ids []string) (daemon.UploadActionResult, error) {
	var result daemon.UploadActionResult
	err := c.Do(ctx, "POST", "/v1/downloads/force", map[string]any{"ids": ids}, &result)
	return result, err
}
