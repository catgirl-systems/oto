package ipc

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) communityBroadcast(w http.ResponseWriter, r *http.Request) {
	var out daemon.CommunityBroadcastPage
	var err error
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		id, e := communityRoomIdentityQuery(q)
		if e != nil {
			communityError(w, e)
			return
		}
		cursor := 0
		if q.Get("cursor") != "" {
			cursor, e = strconv.Atoi(q.Get("cursor"))
			if e != nil {
				communityError(w, e)
				return
			}
		}
		out, err = s.service.CommunityBroadcast(r.Context(), id, q.Get("request_id"), cursor)
	} else {
		var req daemon.CommunityBroadcastRequest
		if e := decode(w, r, &req); e != nil {
			communityError(w, e)
			return
		}
		out, err = s.service.PreviewCommunityBroadcast(r.Context(), req)
	}
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
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

func (s *Server) communityBroadcastAction(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunityBroadcastAction
	if err := decode(w, r, &req); err != nil {
		communityError(w, err)
		return
	}
	out, err := s.service.ActCommunityBroadcast(r.Context(), req)
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (c *Client) ActCommunityBroadcast(ctx context.Context, req daemon.CommunityBroadcastAction) (daemon.CommunityBroadcastPage, error) {
	var out daemon.CommunityBroadcastPage
	err := c.Do(ctx, http.MethodPost, "/v1/community/broadcasts/action", req, &out)
	return out, err
}
