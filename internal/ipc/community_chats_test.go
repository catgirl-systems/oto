package ipc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

func TestCommunityPrivateIPCWorkflows(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password, cfg.DownloadDir = "u", "p", t.TempDir()
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	client, _ := communityIPC(t, cfg, path)
	second := *client
	ctx := context.Background()
	summary, err := client.CommunitySummary(ctx)
	must(t, err)
	identity := summary.CommunityIdentity
	conversation, err := client.OpenCommunityConversation(ctx, daemon.CommunityOpenConversationRequest{CommunityIdentity: identity, Username: "猫/?#"})
	failIf(t, err != nil || conversation.Target != "猫/?#", "identity changed", conversation, err)
	req := daemon.CommunitySendRequest{CommunityIdentity: identity, Username: conversation.Target, Text: "q/? private text\n世界", RequestID: "client-1"}
	first, err := client.SendCommunityPrivate(ctx, req)
	failIf(t, err != nil || first.ConversationID != conversation.ID || first.State != "queued", "offline send", first, err)
	repeated, err := second.SendCommunityPrivate(ctx, req)
	failIf(t, err != nil || !repeated.Duplicate || repeated.MessageID != first.MessageID, "two-client duplicate", repeated, err)
	store, err := storage.Open(path)
	must(t, err)
	defer store.Close()
	for i := range 2 {
		_, err := store.Queries().InsertCommunityMessage(ctx, db.InsertCommunityMessageParams{Account: identity.Account, ConversationID: conversation.ID,
			Direction: "incoming", Sender: conversation.Target, Body: fmt.Sprint("incoming ", i), State: "received", CreatedAt: 1700000000000, Mention: int64(i)})
		must(t, err)
	}
	summary, err = client.CommunitySummary(ctx)
	failIf(t, err != nil || summary.Unread != 2 || summary.Mentions != 1, "unread summary", summary, err)
	list, err := second.CommunityConversations(ctx, daemon.CommunityConversationsRequest{CommunityIdentity: identity, Kind: "private", Query: "猫"})
	failIf(t, err != nil || len(list.Conversations) != 1 || list.Conversations[0].Unread != 2, "conversation list", list, err)
	page, err := client.CommunityMessages(ctx, daemon.CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: conversation.ID})
	failIf(t, err != nil || len(page.Messages) != 3 || page.Conversation.ReadThrough != 0 || page.Conversation.Unread != 2 || page.Conversation.Mentions != 1, "history read changed or misreported conversation state", page, err)
	through := page.Conversation.LatestID
	for _, format := range []string{"json", "text"} {
		export, err := client.ExportCommunityHistory(ctx, daemon.CommunityExportRequest{CommunityIdentity: identity, ConversationID: conversation.ID, Format: format, Limit: 1})
		failIf(t, err != nil || export.ThroughID != through || export.NextCursor != first.MessageID, "export first page", export, err)
		failIf(t, format == "json" && (len(export.Messages) != 1 || export.Messages[0].Text != strings.ReplaceAll(req.Text, "\n", " ")), "JSON export content")
		failIf(t, format == "text" && (!strings.Contains(export.Text, "(queued)") || !strings.Contains(export.Text, "q/? private text 世界\n")), "text export content")
		last, err := second.ExportCommunityHistory(ctx, daemon.CommunityExportRequest{CommunityIdentity: identity, ConversationID: conversation.ID, Format: format, Cursor: export.NextCursor, ThroughID: export.ThroughID})
		failIf(t, err != nil || last.NextCursor != 0, "export next page", last, err)
	}
	summary, _ = second.CommunitySummary(ctx)
	failIf(t, summary.Unread != 2, "export marked read")
	read := daemon.CommunityConversationActionRequest{CommunityIdentity: identity, ConversationID: conversation.ID, Action: "read", ThroughID: through}
	must(t, client.CommunityConversationAction(ctx, read))
	read.ThroughID = first.MessageID
	must(t, second.CommunityConversationAction(ctx, read))
	summary, _ = second.CommunitySummary(ctx)
	failIf(t, summary.Unread != 0, "second frontend regressed read marker")
	req.RequestID, req.Text = "client-2", "later arrival"
	later, err := second.SendCommunityPrivate(ctx, req)
	must(t, err)
	export, err := client.ExportCommunityHistory(ctx, daemon.CommunityExportRequest{CommunityIdentity: identity, ConversationID: conversation.ID, Format: "json", ThroughID: through})
	failIf(t, err != nil || len(export.Messages) != 3 || export.Messages[2].ID >= later.MessageID, "export upper bound moved", export, err)
	cancel := daemon.CommunityMessageActionRequest{CommunityIdentity: identity, MessageID: first.MessageID, Action: "cancel", RequestID: "cancel"}
	must(t, client.CommunityMessageAction(ctx, cancel))
	clear := daemon.CommunityConversationActionRequest{CommunityIdentity: identity, ConversationID: conversation.ID, Action: "clear", ThroughID: through, Confirm: true}
	must(t, client.CommunityConversationAction(ctx, clear))
	must(t, second.CommunityConversationAction(ctx, clear))
	page, err = client.CommunityMessages(ctx, daemon.CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: conversation.ID})
	failIf(t, err != nil || len(page.Messages) != 1 || page.Messages[0].ID != later.MessageID, "clear lost later/unresolved content", page, err)
	must(t, client.CommunityConversationAction(ctx, daemon.CommunityConversationActionRequest{CommunityIdentity: identity, ConversationID: conversation.ID, Action: "close"}))
	list, err = second.CommunityConversations(ctx, daemon.CommunityConversationsRequest{CommunityIdentity: identity})
	failIf(t, err != nil || len(list.Conversations) != 0, "close did not hide conversation", list, err)
	list, err = second.CommunityConversations(ctx, daemon.CommunityConversationsRequest{CommunityIdentity: identity, IncludeClosed: true})
	failIf(t, err != nil || len(list.Conversations) != 1 || !list.Conversations[0].Closed, "close deleted conversation", list, err)
}

func TestCommunityPrivateIPCValidationAndBudgets(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password, cfg.DownloadDir = "u", "p", t.TempDir()
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	client, _ := communityIPC(t, cfg, path)
	ctx := context.Background()
	summary, err := client.CommunitySummary(ctx)
	must(t, err)
	identity := summary.CommunityIdentity
	first, err := client.SendCommunityPrivate(ctx, daemon.CommunitySendRequest{CommunityIdentity: identity, Username: "Alice", RequestID: "one", Text: strings.Repeat("<", soulseek.MaxChatBytes)})
	must(t, err)
	if _, err := client.SendCommunityPrivate(ctx, daemon.CommunitySendRequest{CommunityIdentity: identity, Username: "Alice", RequestID: "two", Text: strings.Repeat("<", soulseek.MaxChatBytes)}); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"json", "text"} {
		exported, err := client.ExportCommunityHistory(ctx, daemon.CommunityExportRequest{CommunityIdentity: identity, ConversationID: first.ConversationID, Format: format})
		failIf(t, err != nil || exported.NextCursor == 0, "large export not paged", format, err)
		encoded, _ := json.Marshal(exported)
		failIf(t, int64(len(encoded)) > MaxBodySize, "export exceeded IPC budget")
	}
	q := communityChatValues(identity, 0, 0, "")
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/v1/community/conversations?session=no", 400},
		{"/v1/community/conversations?" + q.Encode() + "&include_closed=no", 400},
		{"/v1/community/conversations/invalid/messages?" + q.Encode(), 400},
		{"/v1/community/conversations/1/messages?" + q.Encode() + "&newer_than=no", 400},
		{"/v1/community/conversations/1/export?" + q.Encode() + "&through_id=no&format=json", 400},
		{"/v1/community/conversations/1/export?" + q.Encode() + "&format=other", 400},
		{"/v1/community/conversations/999999/messages?" + q.Encode(), 404},
	} {
		response, err := client.http.Do(mustRequest(http.MethodGet, "http://oto.local"+tc.path, nil))
		must(t, err)
		_ = response.Body.Close()
		failIfFmt(t, response.StatusCode != tc.status, "%s: %d, want %d", tc.path, response.StatusCode, tc.status)
	}
	stale := identity
	stale.Session++
	for _, req := range []daemon.CommunitySendRequest{
		{CommunityIdentity: stale, Username: "Alice", RequestID: "stale", Text: "text"},
		{CommunityIdentity: identity, Username: "Alice", Text: "missing id"},
		{CommunityIdentity: identity, Username: "bad\nname", RequestID: "bad-user", Text: "text"},
		{CommunityIdentity: identity, Username: "Alice", RequestID: "oversize", Text: strings.Repeat("x", soulseek.MaxChatBytes+1)},
	} {
		if _, err := client.SendCommunityPrivate(ctx, req); err == nil {
			t.Fatal("accepted invalid send")
		}
	}
	store, err := storage.Open(path)
	must(t, err)
	defer store.Close()
	if err := store.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := db.New(tx).SetCommunityMessageState(ctx, db.SetCommunityMessageStateParams{Account: identity.Account, ID: first.MessageID, OldState: "queued", NewState: "unknown"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	retry := daemon.CommunityMessageActionRequest{CommunityIdentity: identity, MessageID: first.MessageID, Action: "retry", RequestID: "retry"}
	if err := client.CommunityMessageAction(ctx, retry); err == nil {
		t.Fatal("unconfirmed retry")
	}
	retry.Confirm = true
	must(t, client.CommunityMessageAction(ctx, retry))
	if err := client.CommunityMessageAction(ctx, retry); err != nil {
		t.Fatal("idempotent action", err)
	}
}
