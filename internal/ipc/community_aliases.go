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

func (s *Server) registerCommunityAliasRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "list-aliases", Method: http.MethodGet, Path: "/v1/community/aliases",
		Summary: "List chat command aliases", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Account string `query:"account" doc:"Community account"`
		Daemon  string `query:"daemon" doc:"Daemon identity"`
		Session string `query:"session" doc:"Session number"`
		Cursor  string `query:"cursor"`
		Limit   int    `query:"limit"`
	}) (*struct {
		Body daemon.CommunityAliasesPage
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityAliases(ctx, daemon.CommunityAliasesRequest{CommunityIdentity: identity, Cursor: input.Cursor, Limit: input.Limit})
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "set-alias", Method: http.MethodPut, Path: "/v1/community/aliases",
		Summary: "Add or update an alias", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityAliasRequest
	}) (*struct {
		Body daemon.CommunityAlias
	}, error) {
		if input.Body.Remove {
			return nil, communityErr(errors.New("community: use DELETE to remove an alias"))
		}
		out, err := s.service.SetCommunityAlias(ctx, input.Body)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "remove-alias", Method: http.MethodDelete, Path: "/v1/community/aliases",
		Summary: "Remove an alias", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityAliasRequest
	}) (*struct {
		Body daemon.CommunityAlias
	}, error) {
		input.Body.Remove = true
		out, err := s.service.SetCommunityAlias(ctx, input.Body)
		return communityBody(out, err)
	})
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
