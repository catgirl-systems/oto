package daemon

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

func TestCommunityPrivateOutboxIdempotency(t *testing.T) {
	s := downloadService(t)
	ctx := context.Background()
	identity := s.community.identity
	req := CommunitySendRequest{CommunityIdentity: identity, RequestID: "frontend-one-1", Username: "Alice", Text: "hello\r\n世界"}
	first, err := s.SendCommunityPrivate(ctx, req)
	failIfFmt(t, err != nil || first.State != "queued" || first.Duplicate, "offline queue: %+v %v", first, err)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.SendCommunityPrivate(ctx, req)
			if err != nil || got.MessageID != first.MessageID || !got.Duplicate {
				t.Error("duplicate submission", got, err)
			}
		}()
	}
	wg.Wait()
	bad := req
	bad.Text += "!"
	if _, err := s.SendCommunityPrivate(ctx, bad); err == nil {
		t.Fatal("request ID reused for different content")
	}
	page, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: first.ConversationID})
	failIfFmt(t, err != nil || len(page.Messages) != 1 || page.Messages[0].Text != "hello 世界", "normalization/idempotency: %+v %v", page, err)
	for _, text := range []string{"", " \n\t", "\xff", "control\x1b", "\u202e", strings.Repeat("é", soulseek.MaxChatBytes/2+1)} {
		bad := req
		bad.RequestID, bad.Text = "invalid", text
		if _, err := s.SendCommunityPrivate(ctx, bad); err == nil {
			t.Fatal("accepted invalid outgoing text")
		}
	}
	cancel := CommunityMessageActionRequest{CommunityIdentity: identity, MessageID: first.MessageID, Action: "cancel", RequestID: "cancel-one"}
	must(t, s.CommunityMessageAction(ctx, cancel))
	retry := CommunityMessageActionRequest{CommunityIdentity: identity, MessageID: first.MessageID, Action: "retry", RequestID: "retry-one", Confirm: true}
	must(t, s.CommunityMessageAction(ctx, retry))
	// A retried old cancel must not cancel a subsequent explicitly requested retry.
	must(t, s.CommunityMessageAction(ctx, cancel))
	row, err := s.stateDB.Queries().GetCommunityMessage(ctx, db.GetCommunityMessageParams{Account: identity.Account, ID: first.MessageID})
	failIf(t, err != nil || row.State != "queued", "old action ran again", row.State, err)
	cancel.RequestID = "cancel-two"
	must(t, s.CommunityMessageAction(ctx, cancel))
	if err := s.CommunityConversationAction(ctx, CommunityConversationActionRequest{CommunityIdentity: identity, ConversationID: first.ConversationID,
		Action: "clear", ThroughID: first.MessageID, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	got, err := s.SendCommunityPrivate(ctx, req)
	failIf(t, err != nil || got.State != "cleared" || !got.Duplicate, "cleared submission resurrected", got, err)
}

func TestCommunityPrivateOutboxWriteLifecycle(t *testing.T) {
	for _, mode := range []string{"sent", "unknown", "cancelled-before-write"} {
		t.Run(mode, func(t *testing.T) {
			s := downloadService(t)
			ctx := context.Background()
			queued, err := s.SendCommunityPrivate(ctx, CommunitySendRequest{CommunityIdentity: s.community.identity,
				Username: "Alice", Text: "hello 世界", RequestID: "one"})
			must(t, err)
			client, peer, identity := communityTestConnection(t, s)
			get := func() db.CommunityMessage {
				t.Helper()
				row, err := s.stateDB.Queries().GetCommunityMessage(ctx, db.GetCommunityMessageParams{Account: identity.Account, ID: queued.MessageID})
				must(t, err)
				return row
			}
			message := get()
			cancel := CommunityMessageActionRequest{CommunityIdentity: identity, MessageID: message.ID, Action: "cancel", RequestID: "cancel-one"}
			if mode == "cancelled-before-write" {
				must(t, s.CommunityMessageAction(ctx, cancel))
				if err := s.sendCommunityQueued(ctx, client, identity, message); !errors.Is(err, ErrCommunityMessageState) {
					t.Fatal("cancelled message reached writer", err)
				}
				return
			}
			done := make(chan error, 1)
			go func() { done <- s.sendCommunityQueued(ctx, client, identity, message) }()
			var length [4]byte
			if _, err := io.ReadFull(peer, length[:]); err != nil {
				t.Fatal(err)
			}
			failIf(t, get().State != "sending", "network write preceded durable sending state")
			if err := s.CommunityMessageAction(ctx, cancel); !errors.Is(err, ErrCommunityMessageState) {
				t.Fatal("in-flight write cancelled locally", err)
			}
			if mode == "unknown" {
				_ = peer.Close()
				if err := <-done; err == nil {
					t.Fatal("interrupted write succeeded")
				}
				if row := get(); row.State != "unknown" || row.Error == "" {
					t.Fatal("ambiguous write was not retained", row.State)
				}
				if err := s.syncCommunityOutbox(ctx, client, identity); err != nil {
					t.Fatal("unknown should not be retried", err)
				}
				retry := CommunityMessageActionRequest{CommunityIdentity: identity, MessageID: message.ID, Action: "retry", RequestID: "retry"}
				if err := s.CommunityMessageAction(ctx, retry); err == nil {
					t.Fatal("unconfirmed unknown retry")
				}
				must(t, s.CommunityMessageAction(ctx, cancel))
				if err := s.CommunityMessageAction(ctx, retry); err == nil {
					t.Fatal("cancel bypassed unknown retry confirmation")
				}
				retry.Confirm = true
				must(t, s.CommunityMessageAction(ctx, retry))
				failIf(t, get().State != "queued", "explicit retry did not queue")
				return
			}
			body := make([]byte, binary.LittleEndian.Uint32(length[:]))
			if _, err := io.ReadFull(peer, body); err != nil {
				t.Fatal(err)
			}
			must(t, <-done)
			failIf(t, get().State != "sent" || binary.LittleEndian.Uint32(body) != soulseek.ServerPrivateMessage, "send not completed")
			d := soulseek.NewDecoder(body[4:])
			username := d.String()
			text := d.String()
			failIf(t, username != "Alice" || text != "hello 世界" || d.Done() != nil, "wrong private frame")
			if err := s.CommunityMessageAction(ctx, cancel); !errors.Is(err, ErrCommunityMessageState) {
				t.Fatal("completed socket write retracted")
			}
		})
	}
}

func TestCommunityPrivateOutboxRestart(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ctx := context.Background()
	cfg, path := testConfig(t), filepath.Join(t.TempDir(), "state.sqlite3")
	s, err := New(cfg, path)
	must(t, err)
	defer s.Close()
	first, err := s.SendCommunityPrivate(ctx, CommunitySendRequest{CommunityIdentity: s.community.identity, Username: "Alice", Text: "one", RequestID: "one"})
	must(t, err)
	second, err := s.SendCommunityPrivate(ctx, CommunitySendRequest{CommunityIdentity: s.community.identity, Username: "Alice", Text: "two", RequestID: "two"})
	must(t, err)
	if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		if _, err := q.SetCommunityMessageState(ctx, db.SetCommunityMessageStateParams{Account: first.Account, ID: first.MessageID, OldState: "queued", NewState: "sending"}); err != nil {
			return err
		}
		if err := q.EnsureCommunityAccount(ctx, "other-account"); err != nil {
			return err
		}
		conversation, err := q.EnsureCommunityConversation(ctx, db.EnsureCommunityConversationParams{Account: "other-account", Kind: "private", Target: "Alice"})
		if err != nil {
			return err
		}
		_, err = q.InsertCommunityMessage(ctx, db.InsertCommunityMessageParams{Account: "other-account", ConversationID: conversation.ID, Direction: "outgoing", Body: "other", State: "sending"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	oldIdentity := s.community.identity
	must(t, s.Close())
	s, err = New(cfg, path)
	must(t, err)
	defer s.Close()
	page, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: s.community.identity, ConversationID: first.ConversationID})
	failIfFmt(t, err != nil || len(page.Messages) != 2 || page.Messages[0].ID != second.MessageID || page.Messages[0].State != "queued" || page.Messages[1].State != "unknown", "restart recovery: %+v %v", page, err)
	var sending int
	if err := s.stateDB.SQL().QueryRowContext(ctx, "SELECT count(*) FROM community_messages WHERE state = 'sending'").Scan(&sending); err != nil || sending != 0 {
		t.Fatal("inactive account not recovered", sending, err)
	}
	if _, err := s.SendCommunityPrivate(ctx, CommunitySendRequest{CommunityIdentity: oldIdentity, Username: "Alice", Text: "stale", RequestID: "stale"}); !errors.Is(err, ErrCommunitySession) {
		t.Fatal("old daemon request accepted", err)
	}
	duplicate, err := s.SendCommunityPrivate(ctx, CommunitySendRequest{CommunityIdentity: s.community.identity, Username: "Alice", Text: "one", RequestID: "one"})
	failIf(t, err != nil || !duplicate.Duplicate || duplicate.State != "unknown" || duplicate.MessageID != first.MessageID, "restart submission replayed", duplicate, err)
}
