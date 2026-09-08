package ipc

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) registerCommunityChats(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/community/conversations", s.communityConversations)
	mux.HandleFunc("POST /v1/community/conversations", s.communityOpenConversation)
	mux.HandleFunc("POST /v1/community/conversations/action", s.communityConversationAction)
	mux.HandleFunc("GET /v1/community/conversations/{id}/messages", s.communityMessages)
	mux.HandleFunc("GET /v1/community/conversations/{id}/export", s.communityExport)
	mux.HandleFunc("POST /v1/community/messages", s.communitySendPrivate)
	mux.HandleFunc("POST /v1/community/messages/action", s.communityMessageAction)
}

func communityQueryInt(q url.Values, name string) (int64, error) {
	if q.Get(name) == "" {
		return 0, nil
	}
	return strconv.ParseInt(q.Get(name), 10, 64)
}

func communityConversationQuery(r *http.Request) (daemon.CommunityConversationsRequest, error) {
	q := r.URL.Query()
	out := daemon.CommunityConversationsRequest{CommunityIdentity: daemon.CommunityIdentity{Account: q.Get("account"), Daemon: q.Get("daemon")}, Kind: q.Get("kind"), Query: q.Get("query")}
	var err error
	if out.Session, err = strconv.ParseUint(q.Get("session"), 10, 64); err != nil {
		return out, err
	}
	if out.Cursor, err = communityQueryInt(q, "cursor"); err != nil {
		return out, err
	}
	if q.Get("limit") != "" {
		if out.Limit, err = strconv.Atoi(q.Get("limit")); err != nil {
			return out, err
		}
	}
	if q.Get("include_closed") != "" {
		if out.IncludeClosed, err = strconv.ParseBool(q.Get("include_closed")); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (s *Server) communityConversations(w http.ResponseWriter, r *http.Request) {
	req, err := communityConversationQuery(r)
	if err != nil {
		communityError(w, err)
		return
	}
	out, err := s.service.CommunityConversations(r.Context(), req)
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) communityOpenConversation(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunityOpenConversationRequest
	if err := decode(w, r, &req); err != nil {
		communityError(w, err)
		return
	}
	out, err := s.service.OpenCommunityConversation(r.Context(), req)
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) communityConversationAction(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunityConversationActionRequest
	if err := decode(w, r, &req); err != nil {
		communityError(w, err)
		return
	}
	if err := s.service.CommunityConversationAction(r.Context(), req); err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

func (s *Server) communityMessages(w http.ResponseWriter, r *http.Request) {
	base, err := communityConversationQuery(r)
	if err != nil {
		communityError(w, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		communityError(w, err)
		return
	}
	newer, err := communityQueryInt(r.URL.Query(), "newer_than")
	if err != nil {
		communityError(w, err)
		return
	}
	out, err := s.service.CommunityMessages(r.Context(), daemon.CommunityMessagesRequest{CommunityIdentity: base.CommunityIdentity,
		ConversationID: id, Cursor: base.Cursor, Limit: base.Limit, Query: base.Query, NewerThan: newer})
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) communityExport(w http.ResponseWriter, r *http.Request) {
	base, err := communityConversationQuery(r)
	if err != nil {
		communityError(w, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		communityError(w, err)
		return
	}
	through, err := communityQueryInt(r.URL.Query(), "through_id")
	if err != nil {
		communityError(w, err)
		return
	}
	out, err := s.service.ExportCommunityHistory(r.Context(), daemon.CommunityExportRequest{CommunityIdentity: base.CommunityIdentity,
		ConversationID: id, Cursor: base.Cursor, Limit: base.Limit, Query: base.Query, ThroughID: through, Format: r.URL.Query().Get("format")})
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) communitySendPrivate(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunitySendRequest
	if err := decode(w, r, &req); err != nil {
		communityError(w, err)
		return
	}
	out, err := s.service.SendCommunityPrivate(r.Context(), req)
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) communityMessageAction(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunityMessageActionRequest
	if err := decode(w, r, &req); err != nil {
		communityError(w, err)
		return
	}
	if err := s.service.CommunityMessageAction(r.Context(), req); err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

func communityChatValues(identity daemon.CommunityIdentity, cursor int64, limit int, query string) url.Values {
	return url.Values{"account": {identity.Account}, "daemon": {identity.Daemon}, "session": {strconv.FormatUint(identity.Session, 10)},
		"cursor": {strconv.FormatInt(cursor, 10)}, "limit": {strconv.Itoa(limit)}, "query": {query}}
}

func (c *Client) CommunityConversations(ctx context.Context, req daemon.CommunityConversationsRequest) (daemon.CommunityConversationsPage, error) {
	q := communityChatValues(req.CommunityIdentity, req.Cursor, req.Limit, req.Query)
	q.Set("kind", req.Kind)
	q.Set("include_closed", strconv.FormatBool(req.IncludeClosed))
	var out daemon.CommunityConversationsPage
	err := c.Do(ctx, http.MethodGet, "/v1/community/conversations?"+q.Encode(), nil, &out)
	return out, err
}

func (c *Client) OpenCommunityConversation(ctx context.Context, req daemon.CommunityOpenConversationRequest) (daemon.CommunityConversation, error) {
	var out daemon.CommunityConversation
	err := c.Do(ctx, http.MethodPost, "/v1/community/conversations", req, &out)
	return out, err
}

func (c *Client) CommunityMessages(ctx context.Context, req daemon.CommunityMessagesRequest) (daemon.CommunityMessagesPage, error) {
	q := communityChatValues(req.CommunityIdentity, req.Cursor, req.Limit, req.Query)
	q.Set("newer_than", strconv.FormatInt(req.NewerThan, 10))
	var out daemon.CommunityMessagesPage
	err := c.Do(ctx, http.MethodGet, "/v1/community/conversations/"+strconv.FormatInt(req.ConversationID, 10)+"/messages?"+q.Encode(), nil, &out)
	return out, err
}

func (c *Client) ExportCommunityHistory(ctx context.Context, req daemon.CommunityExportRequest) (daemon.CommunityExportPage, error) {
	q := communityChatValues(req.CommunityIdentity, req.Cursor, req.Limit, req.Query)
	q.Set("through_id", strconv.FormatInt(req.ThroughID, 10))
	q.Set("format", req.Format)
	var out daemon.CommunityExportPage
	err := c.Do(ctx, http.MethodGet, "/v1/community/conversations/"+strconv.FormatInt(req.ConversationID, 10)+"/export?"+q.Encode(), nil, &out)
	return out, err
}

func (c *Client) SendCommunityPrivate(ctx context.Context, req daemon.CommunitySendRequest) (daemon.CommunitySendResult, error) {
	var out daemon.CommunitySendResult
	err := c.Do(ctx, http.MethodPost, "/v1/community/messages", req, &out)
	return out, err
}

func (c *Client) CommunityConversationAction(ctx context.Context, req daemon.CommunityConversationActionRequest) error {
	return c.Do(ctx, http.MethodPost, "/v1/community/conversations/action", req, nil)
}

func (c *Client) CommunityMessageAction(ctx context.Context, req daemon.CommunityMessageActionRequest) error {
	return c.Do(ctx, http.MethodPost, "/v1/community/messages/action", req, nil)
}
