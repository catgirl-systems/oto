package ipc

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
)

var (
	epCommunityAliases     = endpoint[daemon.CommunityAliasesRequest, daemon.CommunityAliasesPage]{http.MethodGet, "/v1/community/aliases"}
	epSetCommunityAlias    = endpoint[daemon.CommunityAliasRequest, daemon.CommunityAlias]{http.MethodPut, "/v1/community/aliases"}
	epRemoveCommunityAlias = endpoint[daemon.CommunityAliasRequest, daemon.CommunityAlias]{http.MethodDelete, "/v1/community/aliases"}
)

func (s *Server) registerCommunityAliasRoutes() {
	route(s, scopeAuthed, epCommunityAliases.op("list-aliases", "List chat command aliases"), func(ctx context.Context, input *pagedIdentityQuery) (*struct {
		Body daemon.CommunityAliasesPage
	}, error) {
		identity, err := input.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityAliases(ctx, daemon.CommunityAliasesRequest{CommunityIdentity: identity, Cursor: input.Cursor, Limit: input.Limit})
		return communityBody(out, err)
	})
	route(s, scopeAuthed, epSetCommunityAlias.op("set-alias", "Add or update an alias"), func(ctx context.Context, input *struct {
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
	route(s, scopeAuthed, epRemoveCommunityAlias.op("remove-alias", "Remove an alias"), func(ctx context.Context, input *struct {
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
	q := communityRoomValues(req.CommunityIdentity)
	q.Set("cursor", req.Cursor)
	q.Set("limit", strconv.Itoa(req.Limit))
	return get(ctx, c, epCommunityAliases, q)
}

func (c *Client) SetCommunityAlias(ctx context.Context, req daemon.CommunityAliasRequest) (daemon.CommunityAlias, error) {
	return call(ctx, c, setRemoveEndpoint(epSetCommunityAlias, epRemoveCommunityAlias, req.Remove), req)
}
