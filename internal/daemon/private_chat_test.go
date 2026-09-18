package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
	"github.com/catgirl-systems/oto/internal/testutil"
)

func TestCommunityPrivateReceiveHistoryAndRead(t *testing.T) {
	s := downloadService(t)
	_, _, identity := communityTestConnection(t, s)
	ctx := context.Background()
	message := soulseek.PrivateMessage{ID: 42, Timestamp: 1700000000, Username: "Alice", Text: "hello 世界", New: true}
	apply := func(m soulseek.PrivateMessage) {
		t.Helper()
		must(t, s.communityUpdate(ctx, identity, m))
	}
	apply(message)
	message.New = false
	apply(message) // New/offline is deliberately not part of replay identity.
	list, err := s.CommunityConversations(ctx, CommunityConversationsRequest{CommunityIdentity: identity})
	failIfFmt(t, err != nil || len(list.Conversations) != 1 || list.Conversations[0].Unread != 1, "replayed conversation: %+v %v", list, err)
	conversation := list.Conversations[0].ID
	message.Timestamp--
	apply(message) // Earlier remote time still gets a later local ordering ID.
	message.Text += "!"
	apply(message) // Reused server ID/timestamp with different content is not a replay.
	message.Username = "alice"
	apply(message)
	list, err = s.CommunityConversations(ctx, CommunityConversationsRequest{CommunityIdentity: identity})
	failIfFmt(t, err != nil || len(list.Conversations) != 2 || list.Conversations[0].Unread != 3 || list.Conversations[1].Target != "alice", "identity/fingerprint: %+v %v", list, err)
	page, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: conversation, Limit: 2})
	failIfFmt(t, err != nil || len(page.Messages) != 2 || page.NextCursor == 0 || page.NewerCount != 3, "history page: %+v %v", page, err)
	older, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: conversation, Cursor: page.NextCursor, Limit: 2})
	failIfFmt(t, err != nil || len(older.Messages) != 1 || older.NextCursor != 0 || !older.Messages[0].ServerTime.After(*page.Messages[0].ServerTime), "local ordering: %+v %v", older, err)
	failIfFmt(t, page.Conversation != list.Conversations[0], "history metadata disagrees with list: %+v / %+v", page.Conversation, list.Conversations[0])
	opened, err := s.OpenCommunityConversation(ctx, CommunityOpenConversationRequest{CommunityIdentity: identity, Username: "Alice"})
	failIfFmt(t, err != nil || opened != list.Conversations[0], "open metadata disagrees with list: %+v / %+v: %v", opened, list.Conversations[0], err)
	through := page.Messages[0].ID
	var wg sync.WaitGroup
	for _, id := range []int64{through, older.Messages[0].ID, through} {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			if err := s.CommunityConversationAction(ctx, CommunityConversationActionRequest{CommunityIdentity: identity, ConversationID: conversation, Action: "read", ThroughID: id}); err != nil {
				t.Error(err)
			}
		}(id)
	}
	wg.Wait()
	page, err = s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: conversation, NewerThan: through})
	failIfFmt(t, err != nil || page.Conversation.ReadThrough != through || page.NewerCount != 0, "read marker regressed: %+v %v", page, err)
	if err := s.CommunityConversationAction(ctx, CommunityConversationActionRequest{CommunityIdentity: identity, ConversationID: conversation, Action: "read", ThroughID: through + 100}); !errors.Is(err, ErrCommunityMessageState) {
		t.Fatal("accepted fabricated read marker", err)
	}
	clear := CommunityConversationActionRequest{CommunityIdentity: identity, ConversationID: conversation, Action: "clear", ThroughID: through}
	if err := s.CommunityConversationAction(ctx, clear); err == nil {
		t.Fatal("unconfirmed clear")
	}
	clear.Confirm = true
	must(t, s.CommunityConversationAction(ctx, clear))
	message.Username = "Alice"
	apply(message) // Cleared content must not reappear.
	message.ID++
	apply(message)
	if err := s.CommunityConversationAction(ctx, clear); err != nil {
		t.Fatal(err)
	} // Retried clear cannot erase a later arrival.
	page, err = s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: conversation})
	failIfFmt(t, err != nil || len(page.Messages) != 1 || page.Messages[0].ID <= through, "clear resurrected/lost content: %+v %v", page, err)
	summary, err := s.CommunitySummary(ctx)
	failIfFmt(t, err != nil || summary.Unread != 2, "unread isolation: %+v %v", summary, err)
	must(t, s.CommunityConversationAction(ctx, CommunityConversationActionRequest{CommunityIdentity: identity, ConversationID: conversation, Action: "close"}))
	list, err = s.CommunityConversations(ctx, CommunityConversationsRequest{CommunityIdentity: identity, IncludeClosed: true})
	failIf(t, err != nil || !list.Conversations[0].Closed, "close did not retain history", err)
	if _, wanted := s.community.users["Alice"]; wanted {
		t.Fatal("closed conversation kept watch")
	}
	if _, err := s.OpenCommunityConversation(ctx, CommunityOpenConversationRequest{CommunityIdentity: identity, Username: "Alice"}); err != nil {
		t.Fatal(err)
	}
	page, err = s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: conversation, Query: "世界"})
	failIf(t, err != nil || len(page.Messages) != 1 || page.Conversation.Closed, "reopen/search lost history", err)
	stale := identity
	stale.Session++
	if _, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: stale, ConversationID: conversation}); !errors.Is(err, ErrCommunitySession) {
		t.Fatal("stale history request accepted", err)
	}
}

func TestCommunityPrivateRollbackAndDisplayText(t *testing.T) {
	s := downloadService(t)
	_, _, identity := communityTestConnection(t, s)
	ctx := context.Background()
	_, err := s.stateDB.SQL().ExecContext(ctx, `CREATE TRIGGER fail_private BEFORE INSERT ON community_messages BEGIN SELECT RAISE(ABORT, 'scripted storage failure'); END`)
	must(t, err)
	message := soulseek.PrivateMessage{ID: 1, Username: "Alice", Text: "caf\xe9\r\n\x1b[31m\x00\x07\x1b]52;c;data\x07"}
	if err := s.communityUpdate(ctx, identity, message); err == nil {
		t.Fatal("storage failure acknowledged")
	}
	var count int
	if err := s.stateDB.SQL().QueryRowContext(ctx, "SELECT count(*) FROM community_receipts").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed insert retained a receipt", count, err)
	}
	failIf(t, len(s.community.users) != 0, "failed transaction published a watch")
	if _, err := s.stateDB.SQL().ExecContext(ctx, "DROP TRIGGER fail_private"); err != nil {
		t.Fatal(err)
	}
	must(t, s.communityUpdate(ctx, identity, message))
	var body string
	must(t, s.stateDB.SQL().QueryRowContext(ctx, "SELECT body FROM community_messages").Scan(&body))
	failIfFmt(t, !strings.HasPrefix(body, "café\n") || strings.ContainsAny(body, "\x1b\x00\x07\r"), "unsafe/incompatible display text: %q", body)
	if got := communityDisplayText("👩‍💻 世界\u202e\tline\n"); got != "👩‍💻 世界�\tline\n" {
		t.Fatalf("Unicode sanitation: %q", got)
	}
}

func TestCommunityPrivateCommitBeforeAckRestart(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg, path := testConfig(t), filepath.Join(t.TempDir(), "state.sqlite3")
	var original CommunityIdentity
	for attempt := range 2 {
		s, err := New(cfg, path)
		must(t, err)
		left, right := net.Pipe()
		_ = right.SetDeadline(time.Now().Add(3 * time.Second))
		identity := s.community.identity
		identity.Session++
		if attempt == 0 {
			original = identity
		} else if original.Daemon == identity.Daemon {
			t.Fatal("restart reused identity")
		}
		interrupted := errors.New("script: restart after durable commit, before ACK")
		client := soulseek.NewClientOnConn(soulseek.ClientConfig{SocialUpdate: func(ctx context.Context, message soulseek.SocialMessage) error {
			if err := s.communityUpdate(ctx, identity, message); err != nil {
				return err
			}
			if attempt == 0 {
				return interrupted
			}
			return nil
		}}, left)
		s.client, s.community.identity, s.community.online = client, identity, true
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		run := make(chan error, 1)
		go func() { run <- client.Run(ctx) }()
		fixture := testutil.SocialFixture(t, "pm-offline")
		must(t, soulseek.WriteFrame(right, fixture.Code, fixture.Payload(t)))
		if attempt == 0 {
			if err := <-run; !errors.Is(err, interrupted) {
				t.Fatal(err)
			}
		} else {
			code, payload, err := soulseek.ReadFrame(right)
			failIfFmt(t, err != nil || code != soulseek.ServerPrivateAck || len(payload) != 4 || payload[0] != 42, "replay ACK: %d %x %v", code, payload, err)
			cancel()
			<-run
		}
		var count int
		if err := s.stateDB.SQL().QueryRowContext(context.Background(), "SELECT count(*) FROM community_messages").Scan(&count); err != nil || count != 1 {
			t.Fatal("restart duplicate/loss", count, err)
		}
		cancel()
		_ = right.Close()
		must(t, s.Close())
	}
}

func TestCommunityPrivatePageBounds(t *testing.T) {
	s := downloadService(t)
	ctx := context.Background()
	identity := s.community.identity
	conversation, err := s.OpenCommunityConversation(ctx, CommunityOpenConversationRequest{CommunityIdentity: identity, Username: "Alice"})
	must(t, err)
	if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		for i := range 205 {
			_, err := q.InsertCommunityMessage(ctx, db.InsertCommunityMessageParams{Account: identity.Account, ConversationID: conversation.ID,
				Sender: "Alice", Direction: "incoming", State: "received", Body: fmt.Sprint(i), CreatedAt: 1})
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	page, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: conversation.ID, Limit: 999})
	failIf(t, err != nil || len(page.Messages) != 200 || page.NextCursor == 0, "missing record limit", len(page.Messages), err)
	page, err = s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: conversation.ID, Cursor: page.NextCursor})
	failIf(t, err != nil || len(page.Messages) != 5 || page.NextCursor != 0, "lost paged rows", len(page.Messages), err)
	if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE community_messages SET body = ?", strings.Repeat("<", soulseek.MaxChatBytes))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	page, err = s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: conversation.ID})
	failIf(t, err != nil || len(page.Messages) != 1 || page.NextCursor == 0, "missing byte budget", len(page.Messages), err)
	encoded, err := json.Marshal(page)
	failIf(t, err != nil || len(encoded) > 1<<20, "response exceeds IPC budget", len(encoded), err)
	for _, req := range []CommunityMessagesRequest{
		{CommunityIdentity: identity, ConversationID: conversation.ID, Limit: -1},
		{CommunityIdentity: identity, ConversationID: conversation.ID, Cursor: -1},
		{CommunityIdentity: identity, ConversationID: conversation.ID, Query: strings.Repeat("x", 1025)},
		{CommunityIdentity: identity, ConversationID: conversation.ID, NewerThan: -1},
	} {
		if _, err := s.CommunityMessages(ctx, req); err == nil {
			t.Fatal("accepted invalid history page")
		}
	}
}
