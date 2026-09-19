package ipc

import (
	"context"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func (c *Client) ForceDownloads(ctx context.Context, ids []string) (daemon.UploadActionResult, error) {
	var result daemon.UploadActionResult
	err := c.Do(ctx, "POST", "/v1/downloads/force", map[string]any{"ids": ids}, &result)
	return result, err
}
