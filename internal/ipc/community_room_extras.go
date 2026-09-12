package ipc

import (
	"context"
	"net/http"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) communityRoomRole(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunityRoomRoleRequest
	if err := decode(w, r, &req); err != nil {
		communityError(w, err)
		return
	}
	out, err := s.service.ChangeCommunityRoomRole(r.Context(), req)
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) communityRoomInvitations(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunityRoomInvitationsRequest
	if err := decode(w, r, &req); err != nil {
		communityError(w, err)
		return
	}
	if err := s.service.SetCommunityRoomInvitations(r.Context(), req); err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}
func (s *Server) communityRoomWallSet(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunityRoomWallRequest
	if err := decode(w, r, &req); err != nil {
		communityError(w, err)
		return
	}
	if err := s.service.SetCommunityRoomWall(r.Context(), req); err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}
func (s *Server) communityRoomWall(w http.ResponseWriter, r *http.Request) {
	page, err := communityRoomPageQuery(r)
	if err != nil {
		communityError(w, err)
		return
	}
	out, err := s.service.CommunityRoomWall(r.Context(), daemon.CommunityRoomMembersRequest{CommunityIdentity: page.CommunityIdentity, Room: page.Room, Cursor: page.Cursor, Query: page.Query, Limit: page.Limit})
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
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
