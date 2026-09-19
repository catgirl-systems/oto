package ipc

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/danielgtaylor/huma/v2"
)

func (s *Server) registerCommunityChatRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "list-conversations", Method: http.MethodGet, Path: "/v1/community/conversations",
		Summary: "List private conversations", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Account       string `query:"account" doc:"Community account"`
		Daemon        string `query:"daemon" doc:"Daemon identity"`
		Session       string `query:"session" doc:"Session number"`
		Kind          string `query:"kind"`
		Query         string `query:"query"`
		Cursor        int64  `query:"cursor"`
		Limit         int    `query:"limit"`
		IncludeClosed bool   `query:"include_closed"`
	}) (*struct {
		Body daemon.CommunityConversationsPage
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityConversations(ctx, daemon.CommunityConversationsRequest{
			CommunityIdentity: identity, Kind: input.Kind, Query: input.Query, Cursor: input.Cursor, Limit: input.Limit, IncludeClosed: input.IncludeClosed,
		})
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "open-conversation", Method: http.MethodPost, Path: "/v1/community/conversations",
		Summary: "Open a private conversation", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityOpenConversationRequest
	}) (*struct {
		Body daemon.CommunityConversation
	}, error) {
		out, err := s.service.OpenCommunityConversation(ctx, input.Body)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "conversation-action", Method: http.MethodPost, Path: "/v1/community/conversations/action",
		Summary: "Act on a conversation", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityConversationActionRequest
	}) (*emptyOutput, error) {
		if err := s.service.CommunityConversationAction(ctx, input.Body); err != nil {
			return nil, communityErr(err)
		}
		return &emptyOutput{}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "list-conversation-messages", Method: http.MethodGet, Path: "/v1/community/conversations/{id}/messages",
		Summary: "Private conversation messages", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		ID        int64  `path:"id" doc:"Conversation ID"`
		Account   string `query:"account" doc:"Community account"`
		Daemon    string `query:"daemon" doc:"Daemon identity"`
		Session   string `query:"session" doc:"Session number"`
		Query     string `query:"query"`
		Cursor    int64  `query:"cursor"`
		Limit     int    `query:"limit"`
		NewerThan int64  `query:"newer_than"`
	}) (*struct {
		Body daemon.CommunityMessagesPage
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityMessages(ctx, daemon.CommunityMessagesRequest{
			CommunityIdentity: identity, ConversationID: input.ID, Cursor: input.Cursor, Limit: input.Limit, Query: input.Query, NewerThan: input.NewerThan,
		})
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "export-conversation", Method: http.MethodGet, Path: "/v1/community/conversations/{id}/export",
		Summary: "Export a conversation", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		ID        int64  `path:"id" doc:"Conversation ID"`
		Account   string `query:"account" doc:"Community account"`
		Daemon    string `query:"daemon" doc:"Daemon identity"`
		Session   string `query:"session" doc:"Session number"`
		Query     string `query:"query"`
		Cursor    int64  `query:"cursor"`
		Limit     int    `query:"limit"`
		ThroughID int64  `query:"through_id"`
		Format    string `query:"format"`
	}) (*struct {
		Body daemon.CommunityExportPage
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.ExportCommunityHistory(ctx, daemon.CommunityExportRequest{
			CommunityIdentity: identity, ConversationID: input.ID, Cursor: input.Cursor, Limit: input.Limit, Query: input.Query, ThroughID: input.ThroughID, Format: input.Format,
		})
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "send-private-message", Method: http.MethodPost, Path: "/v1/community/messages",
		Summary: "Send a private message", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunitySendRequest
	}) (*struct {
		Body daemon.CommunitySendResult
	}, error) {
		out, err := s.service.SendCommunityPrivate(ctx, input.Body)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "private-message-action", Method: http.MethodPost, Path: "/v1/community/messages/action",
		Summary: "Act on a private message", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityMessageActionRequest
	}) (*emptyOutput, error) {
		if err := s.service.CommunityMessageAction(ctx, input.Body); err != nil {
			return nil, communityErr(err)
		}
		return &emptyOutput{}, nil
	})
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
