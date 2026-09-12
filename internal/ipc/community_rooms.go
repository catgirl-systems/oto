package ipc

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) registerCommunityRooms(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/community/rooms", s.communityRooms)
	mux.HandleFunc("POST /v1/community/rooms/refresh", s.communityRoomsRefresh)
	mux.HandleFunc("POST /v1/community/rooms/action", s.communityRoomAction)
	mux.HandleFunc("POST /v1/community/rooms/messages", s.communityRoomSend)
	mux.HandleFunc("GET /v1/community/rooms/members", s.communityRoomMembers)
	mux.HandleFunc("GET /v1/community/rooms/feed", s.communityFeed)
	mux.HandleFunc("POST /v1/community/rooms/feed/subscription", s.communityFeedSubscription)
}

func communityRoomIdentityQuery(q url.Values) (daemon.CommunityIdentity, error) {
	session, err := strconv.ParseUint(q.Get("session"), 10, 64)
	if err != nil {
		return daemon.CommunityIdentity{}, err
	}
	return daemon.CommunityIdentity{Account: q.Get("account"), Daemon: q.Get("daemon"), Session: session}, nil
}

func communityRoomPageQuery(r *http.Request) (daemon.CommunityRoomsRequest, error) {
	q := r.URL.Query()
	identity, err := communityRoomIdentityQuery(q)
	if err != nil {
		return daemon.CommunityRoomsRequest{}, err
	}
	limit := 0
	if q.Get("limit") != "" {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil {
			return daemon.CommunityRoomsRequest{}, err
		}
	}
	return daemon.CommunityRoomsRequest{CommunityIdentity: identity, Room: q.Get("room"), Cursor: q.Get("cursor"), Query: q.Get("query"), Mode: q.Get("mode"), Limit: limit}, nil
}

func (s *Server) communityRooms(w http.ResponseWriter, r *http.Request) {
	req, err := communityRoomPageQuery(r)
	if err != nil {
		communityError(w, err)
		return
	}
	out, err := s.service.CommunityRooms(r.Context(), req)
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) communityRoomsRefresh(w http.ResponseWriter, r *http.Request) {
	var identity daemon.CommunityIdentity
	if err := decode(w, r, &identity); err != nil {
		communityError(w, err)
		return
	}
	if err := s.service.RefreshCommunityRooms(r.Context(), identity); err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

func (s *Server) communityRoomAction(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunityRoomActionRequest
	if err := decode(w, r, &req); err != nil {
		communityError(w, err)
		return
	}
	out, err := s.service.CommunityRoomAction(r.Context(), req)
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) communityRoomSend(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunityRoomSendRequest
	if err := decode(w, r, &req); err != nil {
		communityError(w, err)
		return
	}
	out, err := s.service.SendCommunityRoom(r.Context(), req)
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) communityRoomMembers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	identity, err := communityRoomIdentityQuery(q)
	if err != nil {
		communityError(w, err)
		return
	}
	limit := 0
	if q.Get("limit") != "" {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil {
			communityError(w, err)
			return
		}
	}
	out, err := s.service.CommunityRoomMembers(r.Context(), daemon.CommunityRoomMembersRequest{CommunityIdentity: identity, Room: q.Get("room"), Cursor: q.Get("cursor"), Query: q.Get("query"), Limit: limit})
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) communityFeed(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	identity, err := communityRoomIdentityQuery(q)
	if err != nil {
		communityError(w, err)
		return
	}
	cursor, err := communityQueryInt(q, "cursor")
	if err != nil {
		communityError(w, err)
		return
	}
	limit := 0
	if q.Get("limit") != "" {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil {
			communityError(w, err)
			return
		}
	}
	out, err := s.service.CommunityFeed(r.Context(), daemon.CommunityFeedRequest{CommunityIdentity: identity, Cursor: cursor, Limit: limit})
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) communityFeedSubscription(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunityFeedSubscription
	if err := decode(w, r, &req); err != nil {
		communityError(w, err)
		return
	}
	if err := s.service.SetCommunityFeed(r.Context(), req); err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
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
