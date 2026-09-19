package ipc

import (
	"context"
	"net/http"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/danielgtaylor/huma/v2"
)

func (s *Server) registerCommunityRoomExtraRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "change-room-role", Method: http.MethodPost, Path: "/v1/community/rooms/roles",
		Summary: "Manage room roles", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityRoomRoleRequest
	}) (*struct {
		Body daemon.CommunityRoomRoleResult
	}, error) {
		out, err := s.service.ChangeCommunityRoomRole(ctx, input.Body)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "set-room-invitations", Method: http.MethodPost, Path: "/v1/community/rooms/invitations",
		Summary: "Manage room invitations", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityRoomInvitationsRequest
	}) (*emptyOutput, error) {
		if err := s.service.SetCommunityRoomInvitations(ctx, input.Body); err != nil {
			return nil, communityErr(err)
		}
		return &emptyOutput{}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-room-wall", Method: http.MethodGet, Path: "/v1/community/rooms/wall",
		Summary: "Room wall", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Account string `query:"account" doc:"Community account"`
		Daemon  string `query:"daemon" doc:"Daemon identity"`
		Session string `query:"session" doc:"Session number"`
		Room    string `query:"room"`
		Cursor  string `query:"cursor"`
		Query   string `query:"query"`
		Limit   int    `query:"limit"`
	}) (*struct {
		Body daemon.CommunityRoomWallPage
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityRoomWall(ctx, daemon.CommunityRoomMembersRequest{
			CommunityIdentity: identity, Room: input.Room, Cursor: input.Cursor, Query: input.Query, Limit: input.Limit,
		})
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "set-room-wall", Method: http.MethodPost, Path: "/v1/community/rooms/wall",
		Summary: "Post to a room wall", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityRoomWallRequest
	}) (*emptyOutput, error) {
		if err := s.service.SetCommunityRoomWall(ctx, input.Body); err != nil {
			return nil, communityErr(err)
		}
		return &emptyOutput{}, nil
	})
}

func (c *Client) ChangeCommunityRoomRole(ctx context.Context, req daemon.CommunityRoomRoleRequest) (daemon.CommunityRoomRoleResult, error) {
	var out daemon.CommunityRoomRoleResult
	err := c.Do(ctx, http.MethodPost, "/v1/community/rooms/roles", req, &out)
	return out, err
}

func (c *Client) SetCommunityRoomInvitations(ctx context.Context, req daemon.CommunityRoomInvitationsRequest) error {
	return c.Do(ctx, http.MethodPost, "/v1/community/rooms/invitations", req, nil)
}

func (c *Client) SetCommunityRoomWall(ctx context.Context, req daemon.CommunityRoomWallRequest) error {
	return c.Do(ctx, http.MethodPost, "/v1/community/rooms/wall", req, nil)
}

func (c *Client) CommunityRoomWall(ctx context.Context, req daemon.CommunityRoomMembersRequest) (daemon.CommunityRoomWallPage, error) {
	q := communityRoomValues(req.CommunityIdentity)
	q.Set("room", req.Room)
	q.Set("cursor", req.Cursor)
	q.Set("query", req.Query)
	q.Set("limit", strconv.Itoa(req.Limit))
	var out daemon.CommunityRoomWallPage
	err := c.Do(ctx, http.MethodGet, "/v1/community/rooms/wall?"+q.Encode(), nil, &out)
	return out, err
}
