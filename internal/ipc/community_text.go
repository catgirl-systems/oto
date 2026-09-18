package ipc

import (
	"context"
	"net/http"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) communityText(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunityTextSettings
	var err error
	if r.Method == http.MethodGet {
		req.CommunityIdentity, err = communityRoomIdentityQuery(r.URL.Query())
	} else {
		err = decode(w, r, &req)
	}
	if err != nil {
		communityError(w, err)
		return
	}
	var out daemon.CommunityTextSettings
	if r.Method == http.MethodGet {
		out, err = s.service.CommunityTextSettings(r.Context(), req.CommunityIdentity)
	} else {
		out, err = s.service.SetCommunityTextSettings(r.Context(), req)
	}
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (c *Client) CommunityTextSettings(ctx context.Context, id daemon.CommunityIdentity) (daemon.CommunityTextSettings, error) {
	q := communityRoomValues(id)
	var out daemon.CommunityTextSettings
	err := c.Do(ctx, http.MethodGet, "/v1/community/text-tools?"+q.Encode(), nil, &out)
	return out, err
}
func (c *Client) SetCommunityTextSettings(ctx context.Context, req daemon.CommunityTextSettings) (daemon.CommunityTextSettings, error) {
	var out daemon.CommunityTextSettings
	err := c.Do(ctx, http.MethodPut, "/v1/community/text-tools", req, &out)
	return out, err
}
