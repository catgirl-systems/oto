package ipc

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/danielgtaylor/huma/v2"
)

func (s *Server) registerCommunityRuleRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "list-privacy-rules", Method: http.MethodGet, Path: "/v1/community/rules",
		Summary: "List privacy rules", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Account string `query:"account" doc:"Community account"`
		Daemon  string `query:"daemon" doc:"Daemon identity"`
		Session string `query:"session" doc:"Session number"`
		Cursor  string `query:"cursor"`
		Limit   int    `query:"limit"`
	}) (*struct {
		Body daemon.CommunityRulesPage
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityRules(ctx, daemon.CommunityRulesRequest{CommunityIdentity: identity, Cursor: input.Cursor, Limit: input.Limit})
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "set-privacy-rule", Method: http.MethodPut, Path: "/v1/community/rules",
		Summary: "Add or update a privacy rule", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
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
	route(s, scopeAuthed, huma.Operation{
		OperationID: "remove-privacy-rule", Method: http.MethodDelete, Path: "/v1/community/rules",
		Summary: "Remove a privacy rule", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
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
