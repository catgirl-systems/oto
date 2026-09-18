package ipc

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) communityAliases(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	identity, err := communityRoomIdentityQuery(q)
	if err != nil {
		communityError(w, err)
		return
	}
	limit := 0
	if q.Get("limit") != "" {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil {
			communityError(w, err)
			return
		}
	}
	out, err := s.service.CommunityAliases(r.Context(), daemon.CommunityAliasesRequest{CommunityIdentity: identity, Cursor: q.Get("cursor"), Limit: limit})
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) communityAliasSet(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunityAliasRequest
	if err := decode(w, r, &req); err != nil {
		communityError(w, err)
		return
	}
	if r.Method != http.MethodDelete && req.Remove {
		communityError(w, errors.New("use DELETE to remove an alias"))
		return
	}
	req.Remove = r.Method == http.MethodDelete
	out, err := s.service.SetCommunityAlias(r.Context(), req)
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (c *Client) CommunityAliases(ctx context.Context, req daemon.CommunityAliasesRequest) (daemon.CommunityAliasesPage, error) {
	q := url.Values{"account": {req.Account}, "daemon": {req.Daemon}, "session": {strconv.FormatUint(req.Session, 10)}, "cursor": {req.Cursor}, "limit": {strconv.Itoa(req.Limit)}}
	var out daemon.CommunityAliasesPage
	err := c.Do(ctx, http.MethodGet, "/v1/community/aliases?"+q.Encode(), nil, &out)
	return out, err
}
func (c *Client) SetCommunityAlias(ctx context.Context, req daemon.CommunityAliasRequest) (daemon.CommunityAlias, error) {
	method := http.MethodPut
	if req.Remove {
		method = http.MethodDelete
	}
	var out daemon.CommunityAlias
	err := c.Do(ctx, method, "/v1/community/aliases", req, &out)
	return out, err
}
