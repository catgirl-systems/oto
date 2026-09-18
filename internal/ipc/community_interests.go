package ipc

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) communityInterests(w http.ResponseWriter, r *http.Request) {
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
	out, err := s.service.CommunityInterests(r.Context(), daemon.CommunityInterestsRequest{CommunityIdentity: identity, Cursor: q.Get("cursor"), Query: q.Get("query"), Limit: limit})
	communityResult(w, out, err)
}
func (s *Server) communityInterestSet(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunityInterestRequest
	if !decodeCommunity(w, r, &req) {
		return
	}
	if r.Method != http.MethodDelete && req.Remove {
		communityError(w, errors.New("community: use DELETE to remove an interest"))
		return
	}
	req.Remove = r.Method == http.MethodDelete
	out, err := s.service.SetCommunityInterest(r.Context(), req)
	communityResult(w, out, err)
}
func (s *Server) communitySelfProfile(w http.ResponseWriter, r *http.Request) {
	identity, err := communityRoomIdentityQuery(r.URL.Query())
	if err != nil {
		communityError(w, err)
		return
	}
	out, err := s.service.CommunitySelfProfile(r.Context(), identity)
	communityResult(w, out, err)
}
func (s *Server) communitySelfProfileSet(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunitySelfProfile
	if !decodeCommunity(w, r, &req) {
		return
	}
	out, err := s.service.SetCommunitySelfProfile(r.Context(), req)
	communityResult(w, out, err)
}
func (c *Client) CommunityInterests(ctx context.Context, req daemon.CommunityInterestsRequest) (daemon.CommunityInterestsPage, error) {
	q := url.Values{"account": {req.Account}, "daemon": {req.Daemon}, "session": {strconv.FormatUint(req.Session, 10)}, "cursor": {req.Cursor}, "query": {req.Query}, "limit": {strconv.Itoa(req.Limit)}}
	var out daemon.CommunityInterestsPage
	err := c.Do(ctx, http.MethodGet, "/v1/community/interests?"+q.Encode(), nil, &out)
	return out, err
}
func (c *Client) SetCommunityInterest(ctx context.Context, req daemon.CommunityInterestRequest) (daemon.CommunityInterestResult, error) {
	method := http.MethodPut
	if req.Remove {
		method = http.MethodDelete
	}
	var out daemon.CommunityInterestResult
	err := c.Do(ctx, method, "/v1/community/interests", req, &out)
	return out, err
}
func (c *Client) CommunitySelfProfile(ctx context.Context, identity daemon.CommunityIdentity) (daemon.CommunitySelfProfile, error) {
	q := communityRoomValues(identity)
	var out daemon.CommunitySelfProfile
	err := c.Do(ctx, http.MethodGet, "/v1/community/profile/self?"+q.Encode(), nil, &out)
	return out, err
}
func (c *Client) SetCommunitySelfProfile(ctx context.Context, req daemon.CommunitySelfProfile) (daemon.CommunitySelfProfile, error) {
	var out daemon.CommunitySelfProfile
	err := c.Do(ctx, http.MethodPut, "/v1/community/profile/self", req, &out)
	return out, err
}
