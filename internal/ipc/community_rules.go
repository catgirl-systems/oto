package ipc

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) communityRules(w http.ResponseWriter, r *http.Request) {
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
	out, err := s.service.CommunityRules(r.Context(), daemon.CommunityRulesRequest{CommunityIdentity: identity, Cursor: q.Get("cursor"), Limit: limit})
	communityResult(w, out, err)
}

func (s *Server) communityRuleSet(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunityRuleRequest
	if !decodeCommunity(w, r, &req) {
		return
	}
	if r.Method != http.MethodDelete && req.Remove {
		communityError(w, errors.New("community: use DELETE to remove a privacy rule"))
		return
	}
	req.Remove = r.Method == http.MethodDelete
	out, err := s.service.SetCommunityRule(r.Context(), req)
	communityResult(w, out, err)
}

func (c *Client) CommunityRules(ctx context.Context, req daemon.CommunityRulesRequest) (daemon.CommunityRulesPage, error) {
	q := url.Values{"account": {req.Account}, "daemon": {req.Daemon}, "session": {strconv.FormatUint(req.Session, 10)}, "cursor": {req.Cursor}, "limit": {strconv.Itoa(req.Limit)}}
	var out daemon.CommunityRulesPage
	err := c.Do(ctx, http.MethodGet, "/v1/community/rules?"+q.Encode(), nil, &out)
	return out, err
}

func (c *Client) SetCommunityRule(ctx context.Context, req daemon.CommunityRuleRequest) (daemon.CommunityRulesPage, error) {
	method := http.MethodPut
	if req.Remove {
		method = http.MethodDelete
	}
	var out daemon.CommunityRulesPage
	err := c.Do(ctx, method, "/v1/community/rules", req, &out)
	return out, err
}
