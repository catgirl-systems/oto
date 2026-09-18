package ipc

import (
	"context"
	"net/http"
	"strconv"

	"github.com/catgirl-systems/oto/internal/diagnostics"
)

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	limit := 500
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		limit = min(max(n, 1), 2000)
	}
	records, err := s.service.LogRecords(limit)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, records)
}

// Logs fetches recent diagnostic records from the daemon's log directory.
func (c *Client) Logs(ctx context.Context, limit int) ([]diagnostics.Record, error) {
	var out []diagnostics.Record
	err := c.do(ctx, "GET", "/v1/logs?limit="+strconv.Itoa(min(max(limit, 1), 2000)), nil, &out, 20<<20)
	return out, err
}
