package ipc

import (
	"context"
	"net/http"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/danielgtaylor/huma/v2"
)

func (s *Server) registerAccountPrivilegeRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-account-privileges", Method: http.MethodGet, Path: "/v1/account/privileges",
		Summary: "Account privileges", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Account string `query:"account" doc:"Community account"`
		Daemon  string `query:"daemon" doc:"Daemon identity"`
		Session string `query:"session" doc:"Session number"`
		Refresh bool   `query:"refresh"`
	}) (*struct {
		Body daemon.AccountPrivileges
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.AccountPrivileges(ctx, daemon.AccountPrivilegesRequest{CommunityIdentity: identity, Refresh: input.Refresh})
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "gift-privileges", Method: http.MethodPost, Path: "/v1/account/privileges/gift",
		Summary: "Gift privileges", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.AccountPrivilegeGiftRequest
	}) (*struct {
		Body daemon.AccountPrivilegeGiftResult
	}, error) {
		out, err := s.service.GiftAccountPrivileges(ctx, input.Body)
		return communityBody(out, err)
	})
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
