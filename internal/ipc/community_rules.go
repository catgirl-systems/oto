package ipc

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
)

var (
	epCommunityRules      = endpoint[daemon.CommunityRulesRequest, daemon.CommunityRulesPage]{http.MethodGet, "/v1/community/rules"}
	epSetCommunityRule    = endpoint[daemon.CommunityRuleRequest, daemon.CommunityRulesPage]{http.MethodPut, "/v1/community/rules"}
	epRemoveCommunityRule = endpoint[daemon.CommunityRuleRequest, daemon.CommunityRulesPage]{http.MethodDelete, "/v1/community/rules"}
)

func (s *Server) registerCommunityRuleRoutes() {
	route(s, scopeAuthed, epCommunityRules.op("list-privacy-rules", "List privacy rules"), func(ctx context.Context, input *pagedIdentityQuery) (*struct {
		Body daemon.CommunityRulesPage
	}, error) {
		identity, err := input.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityRules(ctx, daemon.CommunityRulesRequest{CommunityIdentity: identity, Cursor: input.Cursor, Limit: input.Limit})
		return communityBody(out, err)
	})
	route(s, scopeAuthed, epSetCommunityRule.op("set-privacy-rule", "Add or update a privacy rule"), func(ctx context.Context, input *struct {
		Body daemon.CommunityRuleRequest
	}) (*struct {
		Body daemon.CommunityRulesPage
	}, error) {
		if input.Body.Remove {
			return nil, communityErr(errors.New("community: use DELETE to remove a privacy rule"))
		}
		out, err := s.service.SetCommunityRule(ctx, input.Body)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, epRemoveCommunityRule.op("remove-privacy-rule", "Remove a privacy rule"), func(ctx context.Context, input *struct {
		Body daemon.CommunityRuleRequest
	}) (*struct {
		Body daemon.CommunityRulesPage
	}, error) {
		input.Body.Remove = true
		out, err := s.service.SetCommunityRule(ctx, input.Body)
		return communityBody(out, err)
	})
}

func (c *Client) CommunityRules(ctx context.Context, req daemon.CommunityRulesRequest) (daemon.CommunityRulesPage, error) {
	q := communityRoomValues(req.CommunityIdentity)
	q.Set("cursor", req.Cursor)
	q.Set("limit", strconv.Itoa(req.Limit))
	return get(ctx, c, epCommunityRules, q)
}

func (c *Client) SetCommunityRule(ctx context.Context, req daemon.CommunityRuleRequest) (daemon.CommunityRulesPage, error) {
	return call(ctx, c, setRemoveEndpoint(epSetCommunityRule, epRemoveCommunityRule, req.Remove), req)
}
