package ipc

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/danielgtaylor/huma/v2"
)

func (s *Server) registerCommunityBroadcastRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "list-broadcasts", Method: http.MethodGet, Path: "/v1/community/broadcasts",
		Summary: "List broadcasts", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Account   string `query:"account" doc:"Community account"`
		Daemon    string `query:"daemon" doc:"Daemon identity"`
		Session   string `query:"session" doc:"Session number"`
		RequestID string `query:"request_id"`
		Cursor    int    `query:"cursor"`
	}) (*struct {
		Body daemon.CommunityBroadcastPage
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityBroadcast(ctx, identity, input.RequestID, input.Cursor)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "preview-broadcast", Method: http.MethodPost, Path: "/v1/community/broadcasts",
		Summary: "Send a broadcast", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityBroadcastRequest
	}) (*struct {
		Body daemon.CommunityBroadcastPage
	}, error) {
		out, err := s.service.PreviewCommunityBroadcast(ctx, input.Body)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "broadcast-action", Method: http.MethodPost, Path: "/v1/community/broadcasts/action",
		Summary: "Act on a broadcast", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityBroadcastAction
	}) (*struct {
		Body daemon.CommunityBroadcastPage
	}, error) {
		out, err := s.service.ActCommunityBroadcast(ctx, input.Body)
		return communityBody(out, err)
	})
}

func (c *Client) PreviewCommunityBroadcast(ctx context.Context, req daemon.CommunityBroadcastRequest) (daemon.CommunityBroadcastPage, error) {
	var out daemon.CommunityBroadcastPage
	err := c.Do(ctx, http.MethodPost, "/v1/community/broadcasts", req, &out)
	return out, err
}

func (c *Client) CommunityBroadcast(ctx context.Context, id daemon.CommunityIdentity, requestID string, cursor int) (daemon.CommunityBroadcastPage, error) {
	q := url.Values{"account": {id.Account}, "daemon": {id.Daemon}, "session": {strconv.FormatUint(id.Session, 10)}, "request_id": {requestID}, "cursor": {strconv.Itoa(cursor)}}
	var out daemon.CommunityBroadcastPage
	err := c.Do(ctx, http.MethodGet, "/v1/community/broadcasts?"+q.Encode(), nil, &out)
	return out, err
}

func (c *Client) ActCommunityBroadcast(ctx context.Context, req daemon.CommunityBroadcastAction) (daemon.CommunityBroadcastPage, error) {
	var out daemon.CommunityBroadcastPage
	err := c.Do(ctx, http.MethodPost, "/v1/community/broadcasts/action", req, &out)
	return out, err
}
