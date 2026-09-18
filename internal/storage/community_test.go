package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/catgirl-systems/oto/internal/storage/db"
)

func communityDatabase(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	database, err := OpenDaemon(path)
	must(t, err)
	t.Cleanup(func() { _ = database.Close() })
	for _, account := range []string{"server/Me", "server/me", "other/Me"} {
		must(t, database.Queries().EnsureCommunityAccount(context.Background(), account))
	}
	return database, path
}

func TestCommunityStorageAccountIdentityAndSettings(t *testing.T) {
	database, path := communityDatabase(t)
	ctx, q := context.Background(), database.Queries()
	for i, account := range []string{"server/Me", "server/me", "other/Me"} {
		for _, user := range []string{"Alice", "alice"} {
			must(t, q.PutCommunityBuddy(ctx, db.PutCommunityBuddyParams{Account: account, Username: user, Note: account + user, NotifyOnline: 1, Priority: 1, Trusted: 1}))
		}
		must(t, q.SetCommunityBuddyLastSeen(ctx, db.SetCommunityBuddyLastSeenParams{Account: account, Username: "Alice", SeenAt: 123}))
		must(t, q.SetCommunityBuddyLastSeen(ctx, db.SetCommunityBuddyLastSeenParams{Account: account, Username: "Alice", SeenAt: 100}))
		must(t, q.PutCommunityBuddy(ctx, db.PutCommunityBuddyParams{Account: account, Username: "Alice", Note: account + "Alice"}))
		must(t, q.PutCommunityRoom(ctx, db.PutCommunityRoomParams{Account: account, Room: "oto test", Autojoin: 1, PrivateRoom: 1, OwnWall: account}))
		must(t, q.PutCommunityInterest(ctx, db.PutCommunityInterestParams{Account: account, Item: "music", Opinion: 1}))
		must(t, q.PutCommunityAlias(ctx, db.PutCommunityAliasParams{Account: account, Name: "greet", Expansion: "/msg $1 hello $*"}))
		_, err := q.PutCommunityRule(ctx, db.PutCommunityRuleParams{Account: account, Action: "ban", Kind: "username", Value: "Alice", Message: account})
		must(t, err)
		settings := db.EditCommunityAccountParams{Account: account, Description: account, AcceptInvitations: 0, RetentionDays: int64(i + 1)}
		if n, err := q.EditCommunityAccount(ctx, settings); err != nil || n != 1 {
			t.Fatalf("edit settings: %d %v", n, err)
		}
		settings.Description = "stale edit"
		if n, err := q.EditCommunityAccount(ctx, settings); err != nil || n != 0 {
			t.Fatalf("revision conflict: %d %v", n, err)
		}
	}
	must(t, database.Close())
	reopened, err := OpenDaemon(path)
	must(t, err)
	defer reopened.Close()
	q = reopened.Queries()
	for i, account := range []string{"server/Me", "server/me", "other/Me"} {
		settings, err := q.GetCommunityAccount(ctx, account)
		failIfFmt(t, err != nil || settings.Description != account || settings.Revision != 1 || settings.RetentionDays != int64(i+1) || settings.AcceptInvitations != 0 || settings.PublicFeedLogging != 0, "settings round trip: %+v %v", settings, err)
		buddies, err := q.ListCommunityBuddies(ctx, db.ListCommunityBuddiesParams{Account: account, PageSize: 200})
		failIfFmt(t, err != nil || len(buddies) != 2 || buddies[0].Username != "Alice" || buddies[1].Username != "alice", "exact username isolation: %+v %v", buddies, err)
		failIfFmt(t, buddies[0].LastSeen == nil || *buddies[0].LastSeen != 123 || buddies[1].LastSeen != nil || buddies[0].Trusted != 0 || buddies[1].Trusted != 1, "notes, flags and observed last-seen: %+v", buddies)
		for _, buddy := range buddies {
			failIf(t, buddy.Note != account+buddy.Username, "buddy data crossed an account boundary")
		}
		room, err := q.GetCommunityRoom(ctx, db.GetCommunityRoomParams{Account: account, Room: "oto test"})
		failIfFmt(t, err != nil || room.OwnWall != account || room.Autojoin != 1 || room.PrivateRoom != 1, "room preference: %+v %v", room, err)
		interests, err := q.ListCommunityInterests(ctx, db.ListCommunityInterestsParams{Account: account, PageSize: 200})
		failIfFmt(t, err != nil || len(interests) != 1 || interests[0].Opinion != 1, "interests: %+v %v", interests, err)
		rules, err := q.ListCommunityRules(ctx, db.ListCommunityRulesParams{Account: account, PageSize: 200})
		failIfFmt(t, err != nil || len(rules) != 1 || rules[0].Message != account, "rules: %+v %v", rules, err)
		alias, err := q.GetCommunityAlias(ctx, db.GetCommunityAliasParams{Account: account, Name: "greet"})
		failIfFmt(t, err != nil || alias.Expansion != "/msg $1 hello $*", "alias: %+v %v", alias, err)
	}
}

func TestCommunityStorageMessagesReceiptsAndOutbox(t *testing.T) {
	database, _ := communityDatabase(t)
	ctx, q := context.Background(), database.Queries()
	const account = "server/Me"
	conversation, err := q.EnsureCommunityConversation(ctx, db.EnsureCommunityConversationParams{Account: account, Kind: "private", Target: "Alice"})
	must(t, err)
	var messages []db.CommunityMessage
	for i, state := range []string{"received", "held", "queued", "sending", "sent", "failed", "unknown", "cancelled"} {
		direction := "outgoing"
		if i < 2 {
			direction = "incoming"
		}
		remoteTime := int64(100 - i)
		message, err := q.InsertCommunityMessage(ctx, db.InsertCommunityMessageParams{Account: account, ConversationID: conversation.ID, Sender: "Alice", Direction: direction, Body: "hello 世界", CreatedAt: int64(i), ServerTime: &remoteTime, State: state, Mention: 1})
		must(t, err)
		messages = append(messages, message)
	}
	// The compound FK rejects mixing an account and another account's resource.
	if _, err := q.InsertCommunityMessage(ctx, db.InsertCommunityMessageParams{Account: "other/Me", ConversationID: conversation.ID, Sender: "Alice", Direction: "incoming", Body: "wrong account", State: "received"}); err == nil {
		t.Fatal("cross-account message accepted")
	}
	page, err := q.ListCommunityMessages(ctx, db.ListCommunityMessagesParams{Account: account, ConversationID: conversation.ID, SearchText: "世界", PageSize: 999})
	failIfFmt(t, err != nil || len(page) != 7 || page[0].ID != messages[7].ID || page[6].ID != messages[0].ID, "history order/filter/held visibility: %+v %v", page, err)
	unread, err := q.CommunityUnreadTotals(ctx, account)
	failIfFmt(t, err != nil || unread.Unread != 1 || unread.Mentions != 1, "held/outgoing content counted unread: %+v %v", unread, err)
	if n, err := q.MarkCommunityRead(ctx, db.MarkCommunityReadParams{Account: account, ConversationID: conversation.ID, ThroughID: messages[1].ID}); err != nil || n != 0 {
		t.Fatalf("hidden message marked read: %d %v", n, err)
	}
	if n, err := q.MarkCommunityRead(ctx, db.MarkCommunityReadParams{Account: account, ConversationID: conversation.ID, ThroughID: messages[0].ID}); err != nil || n != 1 {
		t.Fatalf("mark read: %d %v", n, err)
	}
	if n, err := q.SetCommunityMessageState(ctx, db.SetCommunityMessageStateParams{Account: account, ID: messages[2].ID, OldState: "queued", NewState: "sending"}); err != nil || n != 1 {
		t.Fatalf("outbox start: %d %v", n, err)
	}
	if n, err := q.SetCommunityMessageState(ctx, db.SetCommunityMessageStateParams{Account: account, ID: messages[2].ID, OldState: "queued", NewState: "sent"}); err != nil || n != 0 {
		t.Fatalf("stale outbox completion: %d %v", n, err)
	}
	if n, err := q.RecoverCommunityOutbox(ctx, account); err != nil || n != 2 {
		t.Fatalf("ambiguous write recovery: %d %v", n, err)
	}
	outbox, err := q.ListCommunityOutbox(ctx, db.ListCommunityOutboxParams{Account: account, PageSize: 200})
	failIfFmt(t, err != nil || len(outbox) != 0, "ambiguous sends automatically retried: %+v %v", outbox, err)
	fingerprint := sha256.Sum256([]byte("original content"))
	receipt := db.InsertCommunityReceiptParams{Account: account, Sender: "Alice", ServerID: 42, ServerTime: 1, Fingerprint: fingerprint[:], Disposition: "stored"}
	if n, err := q.InsertCommunityReceipt(ctx, receipt); err != nil || n != 1 {
		t.Fatalf("receipt: %d %v", n, err)
	}
	submission := db.InsertCommunitySubmissionParams{Account: account, RequestID: "request-1", Kind: "message", Fingerprint: fingerprint[:], Result: `{"state":"unknown"}`}
	if n, err := q.InsertCommunitySubmission(ctx, submission); err != nil || n != 1 {
		t.Fatalf("submission: %d %v", n, err)
	}
	if n, err := q.ClearCommunityHistoryThrough(ctx, db.ClearCommunityHistoryThroughParams{Account: account, ConversationID: conversation.ID, ThroughID: messages[7].ID}); err != nil || n != 4 {
		t.Fatalf("clear: %d %v", n, err)
	}
	if n, err := q.InsertCommunityReceipt(ctx, receipt); err != nil || n != 0 {
		t.Fatalf("cleared content lost replay protection: %d %v", n, err)
	}
	if n, err := q.InsertCommunitySubmission(ctx, submission); err != nil || n != 0 {
		t.Fatalf("cleared content lost idempotency: %d %v", n, err)
	}
	got, err := q.GetCommunitySubmission(ctx, db.GetCommunitySubmissionParams{Account: account, RequestID: submission.RequestID})
	failIfFmt(t, err != nil || !bytes.Equal(got.Fingerprint, fingerprint[:]) || got.Result != submission.Result, "submission result: %+v %v", got, err)
	if _, err := q.GetCommunityMessage(ctx, db.GetCommunityMessageParams{Account: account, ID: messages[0].ID}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("clear retained transcript content: %v", err)
	}
}

func TestCommunityStoragePagingAndConcurrentReadMarkers(t *testing.T) {
	database, path := communityDatabase(t)
	ctx, q := context.Background(), database.Queries()
	const account = "server/Me"
	conversation, err := q.EnsureCommunityConversation(ctx, db.EnsureCommunityConversationParams{Account: account, Kind: "private", Target: "Alice"})
	must(t, err)
	var ids []int64
	if err := database.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		for i := 0; i < 405; i++ {
			message, err := q.InsertCommunityMessage(ctx, db.InsertCommunityMessageParams{Account: account, ConversationID: conversation.ID, Sender: "Alice", Direction: "incoming", Body: "content", State: "received"})
			if err != nil {
				return err
			}
			ids = append(ids, message.ID)
			if err := q.PutCommunityBuddy(ctx, db.PutCommunityBuddyParams{Account: account, Username: fmt.Sprintf("buddy%03d", i)}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var gotIDs []int64
	var before int64
	for _, count := range []int{200, 200, 5, 0} {
		page, err := q.ListCommunityMessages(ctx, db.ListCommunityMessagesParams{Account: account, ConversationID: conversation.ID, BeforeID: before, PageSize: 99999})
		failIfFmt(t, err != nil || len(page) != count, "bounded history page: %d %v", len(page), err)
		for _, message := range page {
			gotIDs = append(gotIDs, message.ID)
			before = message.ID
		}
	}
	for i, id := range gotIDs {
		failIf(t, id != ids[len(ids)-i-1], "paged history skipped/duplicated a message")
	}
	var users []string
	var after string
	for _, count := range []int{200, 200, 5, 0} {
		page, err := q.ListCommunityBuddies(ctx, db.ListCommunityBuddiesParams{Account: account, AfterUsername: after, PageSize: 99999})
		failIfFmt(t, err != nil || len(page) != count, "bounded buddies page: %d %v", len(page), err)
		for _, buddy := range page {
			users = append(users, buddy.Username)
			after = buddy.Username
		}
	}
	failIf(t, len(users) != 405 || users[0] != "buddy000" || users[404] != "buddy404", "buddy paging truncated the audience")
	second, err := Open(path)
	must(t, err)
	defer second.Close()
	var wg sync.WaitGroup
	for i, connection := range []*DB{database, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				id := ids[len(ids)-1-i]
				if _, err := connection.Queries().MarkCommunityRead(ctx, db.MarkCommunityReadParams{Account: account, ConversationID: conversation.ID, ThroughID: id}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if n, err := q.MarkCommunityRead(ctx, db.MarkCommunityReadParams{Account: "other/Me", ConversationID: conversation.ID, ThroughID: ids[0]}); err != nil || n != 0 {
		t.Fatalf("cross-account marker update: %d %v", n, err)
	}
	got, err := q.GetCommunityConversation(ctx, db.GetCommunityConversationParams{Account: account, ID: conversation.ID})
	failIfFmt(t, err != nil || got.ReadThrough != ids[len(ids)-1], "read marker moved backward: %+v %v", got, err)
	beforeRows := legacyContents(t, database.SQL())
	rolledBack := errors.New("rollback")
	if err := database.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := db.New(tx).BumpCommunityRevision(ctx, account); err != nil {
			return err
		}
		fingerprint := sha256.Sum256([]byte("rollback receipt"))
		if _, err := db.New(tx).InsertCommunityReceipt(ctx, db.InsertCommunityReceiptParams{Account: account, Sender: "Alice", ServerID: 1, ServerTime: 1, Fingerprint: fingerprint[:], Disposition: "stored"}); err != nil {
			return err
		}
		if _, err := db.New(tx).InsertCommunityMessage(ctx, db.InsertCommunityMessageParams{Account: account, ConversationID: conversation.ID, Sender: "Alice", Direction: "incoming", Body: "rollback content", State: "received"}); err != nil {
			return err
		}
		return rolledBack
	}); !errors.Is(err, rolledBack) {
		t.Fatalf("rollback result: %v", err)
	}
	if afterRows := legacyContents(t, database.SQL()); !reflect.DeepEqual(beforeRows, afterRows) {
		t.Fatal("failed transaction changed Community state")
	}
}
