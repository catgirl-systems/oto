package ipc

import (
	"context"
	"net/http"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) receivingSettings(w http.ResponseWriter, r *http.Request) {
	var out daemon.ReceivingSettings
	var err error
	if r.Method == http.MethodGet {
		id, e := communityRoomIdentityQuery(r.URL.Query())
		if e != nil {
			communityError(w, e)
			return
		}
		out, err = s.service.ReceivingSettings(r.Context(), id)
	} else {
		var req daemon.ReceivingSettingsRequest
		if e := decode(w, r, &req); e != nil {
			communityError(w, e)
			return
		}
		out, err = s.service.SetReceivingSettings(r.Context(), req)
	}
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (c *Client) ReceivingSettings(ctx context.Context, id daemon.CommunityIdentity) (daemon.ReceivingSettings, error) {
	q := communityRoomValues(id)
	var out daemon.ReceivingSettings
	err := c.Do(ctx, http.MethodGet, "/v1/downloads/receiving?"+q.Encode(), nil, &out)
	return out, err
}
func (c *Client) SetReceivingSettings(ctx context.Context, req daemon.ReceivingSettingsRequest) (daemon.ReceivingSettings, error) {
	var out daemon.ReceivingSettings
	err := c.Do(ctx, http.MethodPut, "/v1/downloads/receiving", req, &out)
	return out, err
}
