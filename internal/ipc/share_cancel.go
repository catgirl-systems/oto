package ipc

import (
	"context"
	"errors"
	"net/http"

	"github.com/catgirl-systems/oto/internal/daemon"
)

type scanCancelRequest struct {
	ID uint64 `json:"id"`
}

func (s *Server) cancelShareScan(w http.ResponseWriter, r *http.Request) {
	var req scanCancelRequest
	if err := decode(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.service.CancelShareScan(req.ID); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, daemon.ErrScanConflict) {
			status = http.StatusConflict
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, http.StatusAccepted, req)
}

func (c *Client) CancelShareScan(ctx context.Context, id uint64) error {
	return c.Do(ctx, http.MethodPost, "/v1/shares/rescan/cancel", scanCancelRequest{ID: id}, nil)
}
