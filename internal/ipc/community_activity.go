package ipc

import (
	"context"
	"net/http"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) communityActivity(w http.ResponseWriter, r *http.Request) {
	var id daemon.CommunityIdentity
	if err := decode(w, r, &id); err != nil {
		communityError(w, err)
		return
	}
	out, err := s.service.CommunityActivity(r.Context(), id)
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (c *Client) CommunityActivity(ctx context.Context, id daemon.CommunityIdentity) (daemon.CommunityActivity, error) {
	var out daemon.CommunityActivity
	err := c.Do(ctx, http.MethodPost, "/v1/community/activity", id, &out)
	return out, err
}
