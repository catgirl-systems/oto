package ipc

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) communityBuddies(w http.ResponseWriter, r *http.Request) {
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
	out, err := s.service.CommunityBuddies(r.Context(), daemon.CommunityBuddiesRequest{CommunityIdentity: identity, Username: q.Get("username"), Query: q.Get("query"), Cursor: q.Get("cursor"), Sort: q.Get("sort"), Limit: limit})
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) communityBuddySet(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunityBuddyRequest
	if err := decode(w, r, &req); err != nil {
		communityError(w, err)
		return
	}
	if r.Method != http.MethodDelete && req.Remove {
		communityError(w, errors.New("community: use DELETE to remove a buddy"))
		return
	}
	req.Remove = r.Method == http.MethodDelete
	out, err := s.service.SetCommunityBuddy(r.Context(), req)
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (c *Client) CommunityBuddies(ctx context.Context, req daemon.CommunityBuddiesRequest) (daemon.CommunityBuddiesPage, error) {
	q := url.Values{"account": {req.Account}, "daemon": {req.Daemon}, "session": {strconv.FormatUint(req.Session, 10)}, "username": {req.Username}, "query": {req.Query}, "sort": {req.Sort}, "cursor": {req.Cursor}, "limit": {strconv.Itoa(req.Limit)}}
	var out daemon.CommunityBuddiesPage
	err := c.Do(ctx, http.MethodGet, "/v1/community/buddies?"+q.Encode(), nil, &out)
	return out, err
}
func (c *Client) SetCommunityBuddy(ctx context.Context, req daemon.CommunityBuddyRequest) (daemon.CommunityBuddyResult, error) {
	method := http.MethodPut
	if req.Remove {
		method = http.MethodDelete
	}
	var out daemon.CommunityBuddyResult
	err := c.Do(ctx, method, "/v1/community/buddies", req, &out)
	return out, err
}
