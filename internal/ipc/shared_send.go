package ipc

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) sharedSend(w http.ResponseWriter, r *http.Request) {
	var out daemon.SharedSendPage
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
		out, err = s.service.SharedSend(r.Context(), id, q.Get("request_id"), cursor)
	} else {
		var req daemon.SharedSendRequest
		if e := decode(w, r, &req); e != nil {
			communityError(w, e)
			return
		}
		out, err = s.service.PreviewSharedSend(r.Context(), req)
	}
	communityResult(w, out, err)
}
func (s *Server) sharedSendAction(w http.ResponseWriter, r *http.Request) {
	var req daemon.SharedSendAction
	if !decodeCommunity(w, r, &req) {
		return
	}
	out, err := s.service.ActSharedSend(r.Context(), req)
	communityResult(w, out, err)
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
