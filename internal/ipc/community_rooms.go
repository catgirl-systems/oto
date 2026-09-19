package ipc

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/danielgtaylor/huma/v2"
)

func (s *Server) registerCommunityRoomRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "list-rooms", Method: http.MethodGet, Path: "/v1/community/rooms",
		Summary: "List chat rooms", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Account string `query:"account" doc:"Community account"`
		Daemon  string `query:"daemon" doc:"Daemon identity"`
		Session string `query:"session" doc:"Session number"`
		Room    string `query:"room"`
		Cursor  string `query:"cursor"`
		Query   string `query:"query"`
		Mode    string `query:"mode"`
		Limit   int    `query:"limit"`
	}) (*struct {
		Body daemon.CommunityRoomsPage
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityRooms(ctx, daemon.CommunityRoomsRequest{
			CommunityIdentity: identity, Room: input.Room, Cursor: input.Cursor, Query: input.Query, Mode: input.Mode, Limit: input.Limit,
		})
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "refresh-rooms", Method: http.MethodPost, Path: "/v1/community/rooms/refresh",
		Summary: "Refresh rooms", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityIdentity
	}) (*emptyOutput, error) {
		if err := s.service.RefreshCommunityRooms(ctx, input.Body); err != nil {
			return nil, communityErr(err)
		}
		return &emptyOutput{}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "room-action", Method: http.MethodPost, Path: "/v1/community/rooms/action",
		Summary: "Join, leave or act in a room", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityRoomActionRequest
	}) (*struct {
		Body daemon.CommunityRoomActionResult
	}, error) {
		out, err := s.service.CommunityRoomAction(ctx, input.Body)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "send-room-message", Method: http.MethodPost, Path: "/v1/community/rooms/messages",
		Summary: "Send a room message", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityRoomSendRequest
	}) (*struct {
		Body daemon.CommunitySendResult
	}, error) {
		out, err := s.service.SendCommunityRoom(ctx, input.Body)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "list-room-members", Method: http.MethodGet, Path: "/v1/community/rooms/members",
		Summary: "Room members", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Account string `query:"account" doc:"Community account"`
		Daemon  string `query:"daemon" doc:"Daemon identity"`
		Session string `query:"session" doc:"Session number"`
		Room    string `query:"room"`
		Cursor  string `query:"cursor"`
		Query   string `query:"query"`
		Limit   int    `query:"limit"`
		Private bool   `query:"private"`
	}) (*struct {
		Body daemon.CommunityRoomMembersPage
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityRoomMembers(ctx, daemon.CommunityRoomMembersRequest{
			CommunityIdentity: identity, Room: input.Room, Cursor: input.Cursor, Query: input.Query, Limit: input.Limit, Private: input.Private,
		})
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-room-feed", Method: http.MethodGet, Path: "/v1/community/rooms/feed",
		Summary: "Room message feed", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Account string `query:"account" doc:"Community account"`
		Daemon  string `query:"daemon" doc:"Daemon identity"`
		Session string `query:"session" doc:"Session number"`
		Cursor  int64  `query:"cursor"`
		Limit   int    `query:"limit"`
	}) (*struct {
		Body daemon.CommunityFeedPage
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityFeed(ctx, daemon.CommunityFeedRequest{CommunityIdentity: identity, Cursor: input.Cursor, Limit: input.Limit})
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "set-feed-subscription", Method: http.MethodPost, Path: "/v1/community/rooms/feed/subscription",
		Summary: "Manage feed subscriptions", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityFeedSubscription
	}) (*emptyOutput, error) {
		if err := s.service.SetCommunityFeed(ctx, input.Body); err != nil {
			return nil, communityErr(err)
		}
		return &emptyOutput{}, nil
	})
	s.registerCommunityRoomExtraRoutes()
}

func communityRoomValues(identity daemon.CommunityIdentity) url.Values {
	return url.Values{"account": {identity.Account}, "daemon": {identity.Daemon}, "session": {strconv.FormatUint(identity.Session, 10)}}
}

func (c *Client) CommunityRooms(ctx context.Context, req daemon.CommunityRoomsRequest) (daemon.CommunityRoomsPage, error) {
	q := communityRoomValues(req.CommunityIdentity)
	q.Set("room", req.Room)
	q.Set("cursor", req.Cursor)
	q.Set("query", req.Query)
	q.Set("mode", req.Mode)
	q.Set("limit", strconv.Itoa(req.Limit))
	var out daemon.CommunityRoomsPage
	err := c.Do(ctx, http.MethodGet, "/v1/community/rooms?"+q.Encode(), nil, &out)
	return out, err
}

func (c *Client) RefreshCommunityRooms(ctx context.Context, identity daemon.CommunityIdentity) error {
	return c.Do(ctx, http.MethodPost, "/v1/community/rooms/refresh", identity, nil)
}

func (c *Client) CommunityRoomAction(ctx context.Context, req daemon.CommunityRoomActionRequest) (daemon.CommunityRoomActionResult, error) {
	var out daemon.CommunityRoomActionResult
	err := c.Do(ctx, http.MethodPost, "/v1/community/rooms/action", req, &out)
	return out, err
}

func (c *Client) SendCommunityRoom(ctx context.Context, req daemon.CommunityRoomSendRequest) (daemon.CommunitySendResult, error) {
	var out daemon.CommunitySendResult
	err := c.Do(ctx, http.MethodPost, "/v1/community/rooms/messages", req, &out)
	return out, err
}

func (c *Client) CommunityRoomMembers(ctx context.Context, req daemon.CommunityRoomMembersRequest) (daemon.CommunityRoomMembersPage, error) {
	q := communityRoomValues(req.CommunityIdentity)
	q.Set("room", req.Room)
	q.Set("cursor", req.Cursor)
	q.Set("query", req.Query)
	q.Set("limit", strconv.Itoa(req.Limit))
	q.Set("private", strconv.FormatBool(req.Private))
	var out daemon.CommunityRoomMembersPage
	err := c.Do(ctx, http.MethodGet, "/v1/community/rooms/members?"+q.Encode(), nil, &out)
	return out, err
}

func (c *Client) CommunityFeed(ctx context.Context, req daemon.CommunityFeedRequest) (daemon.CommunityFeedPage, error) {
	q := communityRoomValues(req.CommunityIdentity)
	q.Set("cursor", strconv.FormatInt(req.Cursor, 10))
	q.Set("limit", strconv.Itoa(req.Limit))
	var out daemon.CommunityFeedPage
	err := c.Do(ctx, http.MethodGet, "/v1/community/rooms/feed?"+q.Encode(), nil, &out)
	return out, err
}

func (c *Client) SetCommunityFeed(ctx context.Context, req daemon.CommunityFeedSubscription) error {
	return c.Do(ctx, http.MethodPost, "/v1/community/rooms/feed/subscription", req, nil)
}
