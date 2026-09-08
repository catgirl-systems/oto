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
		if err := s.communityUpdate(ctx, identity, m); err != nil {
			t.Fatal(err)
		}
	}
	apply(message)
	message.New = false
	apply(message) // New/offline is deliberately not part of replay identity.
	list, err := s.CommunityConversations(ctx, CommunityConversationsRequest{CommunityIdentity: identity})
	if err != nil || len(list.Conversations) != 1 || list.Conversations[0].Unread != 1 {
		t.Fatalf("replayed conversation: %+v %v", list, err)
	}
	conversation := list.Conversations[0].ID
	message.Timestamp--
	apply(message) // Earlier remote time still gets a later local ordering ID.
	message.Text += "!"
	apply(message) // Reused server ID/timestamp with different content is not a replay.
	message.Username = "alice"
	apply(message)
	list, err = s.CommunityConversations(ctx, CommunityConversationsRequest{CommunityIdentity: identity})
	if err != nil || len(list.Conversations) != 2 || list.Conversations[0].Unread != 3 || list.Conversations[1].Target != "alice" {
		t.Fatalf("identity/fingerprint: %+v %v", list, err)
	}
	page, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: conversation, Limit: 2})
	if err != nil || len(page.Messages) != 2 || page.NextCursor == 0 || page.NewerCount != 3 {
		t.Fatalf("history page: %+v %v", page, err)
	}
	older, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: conversation, Cursor: page.NextCursor, Limit: 2})
	if err != nil || len(older.Messages) != 1 || older.NextCursor != 0 || !older.Messages[0].ServerTime.After(*page.Messages[0].ServerTime) {
		t.Fatalf("local ordering: %+v %v", older, err)
	}
	if page.Conversation != list.Conversations[0] {
		t.Fatalf("history metadata disagrees with list: %+v / %+v", page.Conversation, list.Conversations[0])
	}
	opened, err := s.OpenCommunityConversation(ctx, CommunityOpenConversationRequest{CommunityIdentity: identity, Username: "Alice"})
	if err != nil || opened != list.Conversations[0] {
		t.Fatalf("open metadata disagrees with list: %+v / %+v: %v", opened, list.Conversations[0], err)
	}
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
	if err != nil || page.Conversation.ReadThrough != through || page.NewerCount != 0 {
		t.Fatalf("read marker regressed: %+v %v", page, err)
	}
	if err := s.CommunityConversationAction(ctx, CommunityConversationActionRequest{CommunityIdentity: identity, ConversationID: conversation, Action: "read", ThroughID: through + 100}); !errors.Is(err, ErrCommunityMessageState) {
		t.Fatal("accepted fabricated read marker", err)
	}
	clear := CommunityConversationActionRequest{CommunityIdentity: identity, ConversationID: conversation, Action: "clear", ThroughID: through}
	if err := s.CommunityConversationAction(ctx, clear); err == nil {
		t.Fatal("unconfirmed clear")
	}
	clear.Confirm = true
	if err := s.CommunityConversationAction(ctx, clear); err != nil {
		t.Fatal(err)
	}
	message.Username = "Alice"
	apply(message) // Cleared content must not reappear.
	message.ID++
	apply(message)
	if err := s.CommunityConversationAction(ctx, clear); err != nil {
		t.Fatal(err)
	} // Retried clear cannot erase a later arrival.
	page, err = s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: conversation})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID <= through {
		t.Fatalf("clear resurrected/lost content: %+v %v", page, err)
	}
	summary, err := s.CommunitySummary(ctx)
	if err != nil || summary.Unread != 2 {
		t.Fatalf("unread isolation: %+v %v", summary, err)
	}
	if err := s.CommunityConversationAction(ctx, CommunityConversationActionRequest{CommunityIdentity: identity, ConversationID: conversation, Action: "close"}); err != nil {
		t.Fatal(err)
	}
	list, err = s.CommunityConversations(ctx, CommunityConversationsRequest{CommunityIdentity: identity, IncludeClosed: true})
	if err != nil || !list.Conversations[0].Closed {
		t.Fatal("close did not retain history", err)
	}
	if _, wanted := s.community.users["Alice"]; wanted {
		t.Fatal("closed conversation kept watch")
	}
	if _, err := s.OpenCommunityConversation(ctx, CommunityOpenConversationRequest{CommunityIdentity: identity, Username: "Alice"}); err != nil {
		t.Fatal(err)
	}
	page, err = s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: conversation, Query: "世界"})
	if err != nil || len(page.Messages) != 1 || page.Conversation.Closed {
		t.Fatal("reopen/search lost history", err)
	}
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
	if _, err := s.stateDB.SQL().ExecContext(ctx, `CREATE TRIGGER fail_private BEFORE INSERT ON community_messages BEGIN SELECT RAISE(ABORT, 'scripted storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	message := soulseek.PrivateMessage{ID: 1, Username: "Alice", Text: "caf\xe9\r\n\x1b[31m\x00\x07\x1b]52;c;data\x07"}
	if err := s.communityUpdate(ctx, identity, message); err == nil {
		t.Fatal("storage failure acknowledged")
	}
	var count int
	if err := s.stateDB.SQL().QueryRowContext(ctx, "SELECT count(*) FROM community_receipts").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed insert retained a receipt", count, err)
	}
	if len(s.community.users) != 0 {
		t.Fatal("failed transaction published a watch")
	}
	if _, err := s.stateDB.SQL().ExecContext(ctx, "DROP TRIGGER fail_private"); err != nil {
		t.Fatal(err)
	}
	if err := s.communityUpdate(ctx, identity, message); err != nil {
		t.Fatal(err)
	}
	var body string
	if err := s.stateDB.SQL().QueryRowContext(ctx, "SELECT body FROM community_messages").Scan(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body, "café\n") || strings.ContainsAny(body, "\x1b\x00\x07\r") {
		t.Fatalf("unsafe/incompatible display text: %q", body)
	}
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
		if err != nil {
			t.Fatal(err)
		}
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
		var fixture testutil.WireFixture
		for _, f := range testutil.SocialFixtures(t) {
			if f.Name == "pm-offline" {
				fixture = f
				break
			}
		}
		if err := soulseek.WriteFrame(right, fixture.Code, fixture.Payload(t)); err != nil {
			t.Fatal(err)
		}
		if attempt == 0 {
			if err := <-run; !errors.Is(err, interrupted) {
				t.Fatal(err)
			}
		} else {
			code, payload, err := soulseek.ReadFrame(right)
			if err != nil || code != soulseek.ServerPrivateAck || len(payload) != 4 || payload[0] != 42 {
				t.Fatalf("replay ACK: %d %x %v", code, payload, err)
			}
			cancel()
			<-run
		}
		var count int
		if err := s.stateDB.SQL().QueryRowContext(context.Background(), "SELECT count(*) FROM community_messages").Scan(&count); err != nil || count != 1 {
			t.Fatal("restart duplicate/loss", count, err)
		}
		cancel()
		_ = right.Close()
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCommunityPrivatePageBounds(t *testing.T) {
	s := downloadService(t)
	ctx := context.Background()
	identity := s.community.identity
	conversation, err := s.OpenCommunityConversation(ctx, CommunityOpenConversationRequest{CommunityIdentity: identity, Username: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil || len(page.Messages) != 200 || page.NextCursor == 0 {
		t.Fatal("missing record limit", len(page.Messages), err)
	}
	page, err = s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: conversation.ID, Cursor: page.NextCursor})
	if err != nil || len(page.Messages) != 5 || page.NextCursor != 0 {
		t.Fatal("lost paged rows", len(page.Messages), err)
	}
	if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE community_messages SET body = ?", strings.Repeat("<", soulseek.MaxChatBytes))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	page, err = s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: conversation.ID})
	if err != nil || len(page.Messages) != 1 || page.NextCursor == 0 {
		t.Fatal("missing byte budget", len(page.Messages), err)
	}
	encoded, err := json.Marshal(page)
	if err != nil || len(encoded) > 1<<20 {
		t.Fatal("response exceeds IPC budget", len(encoded), err)
	}
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
