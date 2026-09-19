package ipc

import (
	"context"
	"net/http"
)

type scanCancelRequest struct {
	ID uint64 `json:"id"`
}

func (c *Client) CancelShareScan(ctx context.Context, id uint64) error {
	return c.Do(ctx, http.MethodPost, "/v1/shares/rescan/cancel", scanCancelRequest{ID: id}, nil)
}
