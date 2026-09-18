package ipc

import (
	"context"
	"net/http"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) accountPrivileges(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id, err := communityRoomIdentityQuery(q)
	if err != nil {
		communityError(w, err)
		return
	}
	refresh := false
	if q.Has("refresh") {
		refresh, err = strconv.ParseBool(q.Get("refresh"))
		if err != nil {
			communityError(w, err)
			return
		}
	}
	out, err := s.service.AccountPrivileges(r.Context(), daemon.AccountPrivilegesRequest{CommunityIdentity: id, Refresh: refresh})
	communityResult(w, out, err)
}
func (s *Server) accountPrivilegeGift(w http.ResponseWriter, r *http.Request) {
	var req daemon.AccountPrivilegeGiftRequest
	if !decodeCommunity(w, r, &req) {
		return
	}
	out, err := s.service.GiftAccountPrivileges(r.Context(), req)
	communityResult(w, out, err)
}
func (c *Client) AccountPrivileges(ctx context.Context, req daemon.AccountPrivilegesRequest) (daemon.AccountPrivileges, error) {
	q := communityRoomValues(req.CommunityIdentity)
	q.Set("refresh", strconv.FormatBool(req.Refresh))
	var out daemon.AccountPrivileges
	err := c.Do(ctx, http.MethodGet, "/v1/account/privileges?"+q.Encode(), nil, &out)
	return out, err
}
func (c *Client) GiftAccountPrivileges(ctx context.Context, req daemon.AccountPrivilegeGiftRequest) (daemon.AccountPrivilegeGiftResult, error) {
	var out daemon.AccountPrivilegeGiftResult
	err := c.Do(ctx, http.MethodPost, "/v1/account/privileges/gift", req, &out)
	return out, err
}
