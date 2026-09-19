package ipc

import (
	"context"
	"strconv"

	"github.com/catgirl-systems/oto/internal/diagnostics"
)

// Logs fetches recent diagnostic records from the daemon's log directory.
func (c *Client) Logs(ctx context.Context, limit int) ([]diagnostics.Record, error) {
	var out []diagnostics.Record
	err := c.do(ctx, "GET", "/v1/logs?limit="+strconv.Itoa(min(max(limit, 1), 2000)), nil, &out, 20<<20)
	return out, err
}
