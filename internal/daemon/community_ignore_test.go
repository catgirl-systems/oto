package daemon

import (
	"context"
	"database/sql"
	"net/netip"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

func TestCommunityIgnoredAndHeldPrivateMessages(t *testing.T) {
	s := downloadService(t)
	_, _, identity := communityTestConnection(t, s)
	ctx := context.Background()
	setRule := func(rule CommunityRule) {
		t.Helper()
		page, err := s.CommunityRules(ctx, CommunityRulesRequest{CommunityIdentity: identity})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetCommunityRule(ctx, CommunityRuleRequest{CommunityIdentity: identity, Rule: rule, Confirm: true, Revision: page.Revision}); err != nil {
			t.Fatal(err)
		}
	}
	setRule(CommunityRule{Action: "ignore", Kind: "username", Value: "ignored"})
	message := soulseek.PrivateMessage{ID: 42, Timestamp: 1700000000, Username: "ignored", Text: "private text", New: true}
	if err := s.communityUpdate(ctx, identity, message); err != nil {
		t.Fatal(err)
	}
	page, err := s.CommunityConversations(ctx, CommunityConversationsRequest{CommunityIdentity: identity})
	if err != nil || len(page.Conversations) != 0 {
		t.Fatal("ignored conversation displayed", page, err)
	}
	setRule(CommunityRule{Action: "ignore", Kind: "ip", Value: "192.0.2.0/24"})
	message.Username = "held"
	if err := s.communityUpdate(ctx, identity, message); err != nil {
		t.Fatal(err)
	}
	page, err = s.CommunityConversations(ctx, CommunityConversationsRequest{CommunityIdentity: identity})
	if err != nil || len(page.Conversations) != 0 {
		t.Fatal("held conversation displayed", page, err)
	}
	rows, err := s.stateDB.Queries().ListHeldCommunityMessages(ctx, db.ListHeldCommunityMessagesParams{Account: identity.Account, Sender: "held", PageSize: 200})
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	if err := s.releaseCommunityHeld(ctx, identity, "held", netip.MustParseAddr("192.0.2.1")); err != nil {
		t.Fatal(err)
	}
	if err := s.communityUpdate(ctx, identity, message); err != nil {
		t.Fatal(err)
	}
	rows, err = s.stateDB.Queries().ListHeldCommunityMessages(ctx, db.ListHeldCommunityMessagesParams{Account: identity.Account, Sender: "held", PageSize: 200})
	if err != nil || len(rows) != 0 {
		t.Fatal("discarded replay resurrected", rows, err)
	}
	message.Username = "allowed"
	if err := s.communityUpdate(ctx, identity, message); err != nil {
		t.Fatal(err)
	}
	rows, err = s.stateDB.Queries().ListHeldCommunityMessages(ctx, db.ListHeldCommunityMessagesParams{Account: identity.Account, Sender: "allowed", PageSize: 200})
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	heldID := rows[0].ID
	// Simulate a read marker advancing while content was withheld.
	if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE community_conversations SET read_through = ? WHERE id = ?", heldID, rows[0].ConversationID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.releaseCommunityHeld(ctx, identity, "allowed", netip.MustParseAddr("198.51.100.1")); err != nil {
		t.Fatal(err)
	}
	page, err = s.CommunityConversations(ctx, CommunityConversationsRequest{CommunityIdentity: identity})
	if err != nil || len(page.Conversations) != 1 || page.Conversations[0].Unread != 1 {
		t.Fatal("released read marker", page, err)
	}
	messages, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: rows[0].ConversationID})
	if err != nil || len(messages.Messages) != 1 || messages.Messages[0].ID <= heldID {
		t.Fatal(messages, err)
	}
}

func TestCommunityIgnoreRoomsFeedAndWalls(t *testing.T) {
	s := downloadService(t)
	client, peer, id := communityTestConnection(t, s)
	ctx := context.Background()
	if _, err := s.CommunityRoomAction(ctx, CommunityRoomActionRequest{CommunityIdentity: id, Room: "oto test", Action: "join", RequestID: "join"}); err != nil {
		t.Fatal(err)
	}
	syncTestRooms(t, s, client, peer, id, "room-join-public", "room-invitations-true-client")
	applyRoomFixture(t, s, id, "room-joined")
	rule, err := NormalizeCommunityRule(CommunityRule{Action: "ignore", Kind: "ip", Value: "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.community.rules = []CommunityRule{rule}
	s.community.feedWanted, s.community.feedWritten = true, true
	s.mu.Unlock()
	for _, feed := range []bool{false, true} {
		applyRoomEvent(t, s, id, soulseek.RoomMessage{Room: "oto test", Username: "Alice", Text: "hidden until address", PublicFeed: feed})
	}
	applyRoomEvent(t, s, id, soulseek.RoomWallSnapshot{Room: "oto test", Entries: []soulseek.RoomWallEntry{{Username: "Alice", Text: "wall"}}})
	wall, err := s.CommunityRoomWall(ctx, CommunityRoomMembersRequest{CommunityIdentity: id, Room: "oto test"})
	if err != nil || len(wall.Entries) != 0 {
		t.Fatal("unresolved wall displayed", wall, err)
	}
	feed, err := s.CommunityFeed(ctx, CommunityFeedRequest{CommunityIdentity: id})
	if err != nil || len(feed.Messages) != 0 {
		t.Fatal("unresolved feed displayed", feed, err)
	}
	s.mu.RLock()
	next := s.nextTransientIgnoreSenderLocked("", "")
	s.mu.RUnlock()
	if next != "Alice" {
		t.Fatal("transient lookup not scheduled", next)
	}
	if err := s.releaseCommunityHeld(ctx, id, "Alice", netip.MustParseAddr("198.51.100.1")); err != nil {
		t.Fatal(err)
	}
	wall, err = s.CommunityRoomWall(ctx, CommunityRoomMembersRequest{CommunityIdentity: id, Room: "oto test"})
	if err != nil || len(wall.Entries) != 1 {
		t.Fatal(wall, err)
	}
	feed, err = s.CommunityFeed(ctx, CommunityFeedRequest{CommunityIdentity: id})
	if err != nil || len(feed.Messages) != 1 {
		t.Fatal(feed, err)
	}
	room := roomSnapshot(t, s, id, "oto test")
	messages, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: id, ConversationID: room.ConversationID})
	if err != nil || len(messages.Messages) != 2 || messages.Messages[0].Sender != "Alice" {
		t.Fatal(messages, err)
	}
	// A newly observed ignored address suppresses already cached transient text.
	s.mu.Lock()
	user := s.community.users["Alice"]
	user.IP, user.AddressFresh = "192.0.2.1", true
	s.community.users["Alice"] = user
	s.mu.Unlock()
	wall, err = s.CommunityRoomWall(ctx, CommunityRoomMembersRequest{CommunityIdentity: id, Room: "oto test"})
	if err != nil || len(wall.Entries) != 0 {
		t.Fatal(wall, err)
	}
	feed, err = s.CommunityFeed(ctx, CommunityFeedRequest{CommunityIdentity: id})
	if err != nil || len(feed.Messages) != 0 {
		t.Fatal(feed, err)
	}
}

func TestCommunityIgnoreAddressExpiresOnPeerDeparture(t *testing.T) {
	s := downloadService(t)
	_, _, id := communityTestConnection(t, s)
	for _, message := range []soulseek.SocialMessage{soulseek.UserPresence{Username: "Alice", Status: soulseek.UserStatusOffline}, soulseek.WatchUserResponse{Username: "Alice", Exists: false}, soulseek.WatchUserResponse{Username: "Alice", Exists: true, Status: soulseek.UserStatusOffline}} {
		s.mu.Lock()
		s.community.users["Alice"] = CommunityUser{Username: "Alice", Status: soulseek.UserStatusOnline, StatusFresh: true, IP: "192.0.2.1", AddressFresh: true}
		s.community.ignoreAddresses = map[string]communityIgnoreAddress{"Alice": {address: netip.MustParseAddr("192.0.2.1"), expires: time.Now().Add(time.Minute)}}
		s.mu.Unlock()
		if err := s.communityUpdate(context.Background(), id, message); err != nil {
			t.Fatal(err)
		}
		s.mu.RLock()
		address := s.communityIgnoreAddressLocked("Alice")
		s.mu.RUnlock()
		if address.IsValid() {
			t.Fatal("departed peer kept an authoritative ignore address")
		}
	}
}
