package ipc

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/danielgtaylor/huma/v2"
)

func (s *Server) registerSharedSendRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "list-shared-send-offers", Method: http.MethodGet, Path: "/v1/shares/send",
		Summary: "List shared-send recipients", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Account   string `query:"account" doc:"Community account"`
		Daemon    string `query:"daemon" doc:"Daemon identity"`
		Session   string `query:"session" doc:"Session number"`
		RequestID string `query:"request_id"`
		Cursor    int    `query:"cursor"`
	}) (*struct {
		Body daemon.SharedSendPage
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.SharedSend(ctx, identity, input.RequestID, input.Cursor)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "preview-shared-send", Method: http.MethodPost, Path: "/v1/shares/send",
		Summary: "Offer shared files to a peer", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.SharedSendRequest
	}) (*struct {
		Body daemon.SharedSendPage
	}, error) {
		out, err := s.service.PreviewSharedSend(ctx, input.Body)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "preview-shared-send-legacy", Method: http.MethodPost, Path: "/v1/uploads/send",
		Summary: "Offer shared files to a peer", Errors: communityErrors,
		Description: "Legacy alias of POST /v1/shares/send.",
	}, func(ctx context.Context, input *struct {
		Body daemon.SharedSendRequest
	}) (*struct {
		Body daemon.SharedSendPage
	}, error) {
		out, err := s.service.PreviewSharedSend(ctx, input.Body)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "shared-send-action", Method: http.MethodPost, Path: "/v1/shares/send/action",
		Summary: "Act on a shared-send offer", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.SharedSendAction
	}) (*struct {
		Body daemon.SharedSendPage
	}, error) {
		out, err := s.service.ActSharedSend(ctx, input.Body)
		return communityBody(out, err)
	})
}

func (c *Client) PreviewSharedSend(ctx context.Context, req daemon.SharedSendRequest) (daemon.SharedSendPage, error) {
	var out daemon.SharedSendPage
	err := c.Do(ctx, http.MethodPost, "/v1/uploads/send", req, &out)
	return out, err
}

func (c *Client) SharedSend(ctx context.Context, id daemon.CommunityIdentity, requestID string, cursor int) (daemon.SharedSendPage, error) {
	q := url.Values{"account": {id.Account}, "daemon": {id.Daemon}, "session": {strconv.FormatUint(id.Session, 10)}, "request_id": {requestID}, "cursor": {strconv.Itoa(cursor)}}
	var out daemon.SharedSendPage
	err := c.Do(ctx, http.MethodGet, "/v1/shares/send?"+q.Encode(), nil, &out)
	return out, err
}

func (c *Client) ActSharedSend(ctx context.Context, req daemon.SharedSendAction) (daemon.SharedSendPage, error) {
	var out daemon.SharedSendPage
	err := c.Do(ctx, http.MethodPost, "/v1/shares/send/action", req, &out)
	return out, err
}
