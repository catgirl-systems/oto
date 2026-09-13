package ipc

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) communityAway(w http.ResponseWriter, r *http.Request) {
	var out daemon.CommunityAwaySettings
	var err error
	if r.Method == http.MethodGet {
		id, e := communityRoomIdentityQuery(r.URL.Query())
		if e != nil {
			communityError(w, e)
			return
		}
		out, err = s.service.CommunityAwaySettings(r.Context(), id)
	} else {
		var req daemon.CommunityAwaySettingsRequest
		if e := decode(w, r, &req); e != nil {
			communityError(w, e)
			return
		}
		out, err = s.service.SetCommunityAwaySettings(r.Context(), req)
	}
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (c *Client) CommunityAwaySettings(ctx context.Context, id daemon.CommunityIdentity) (daemon.CommunityAwaySettings, error) {
	q := url.Values{"account": {id.Account}, "daemon": {id.Daemon}, "session": {strconv.FormatUint(id.Session, 10)}}
	var out daemon.CommunityAwaySettings
	err := c.Do(ctx, http.MethodGet, "/v1/community/away?"+q.Encode(), nil, &out)
	return out, err
}
func (c *Client) SetCommunityAwaySettings(ctx context.Context, req daemon.CommunityAwaySettingsRequest) (daemon.CommunityAwaySettings, error) {
	var out daemon.CommunityAwaySettings
	err := c.Do(ctx, http.MethodPut, "/v1/community/away", req, &out)
	return out, err
}
