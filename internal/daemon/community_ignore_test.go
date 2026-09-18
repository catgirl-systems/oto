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
		must(t, err)
		if _, err := s.SetCommunityRule(ctx, CommunityRuleRequest{CommunityIdentity: identity, Rule: rule, Confirm: true, Revision: page.Revision}); err != nil {
			t.Fatal(err)
		}
	}
	setRule(CommunityRule{Action: "ignore", Kind: "username", Value: "ignored"})
	message := soulseek.PrivateMessage{ID: 42, Timestamp: 1700000000, Username: "ignored", Text: "private text", New: true}
	must(t, s.communityUpdate(ctx, identity, message))
	page, err := s.CommunityConversations(ctx, CommunityConversationsRequest{CommunityIdentity: identity})
	failIf(t, err != nil || len(page.Conversations) != 0, "ignored conversation displayed", page, err)
	setRule(CommunityRule{Action: "ignore", Kind: "ip", Value: "192.0.2.0/24"})
	message.Username = "held"
	must(t, s.communityUpdate(ctx, identity, message))
	page, err = s.CommunityConversations(ctx, CommunityConversationsRequest{CommunityIdentity: identity})
	failIf(t, err != nil || len(page.Conversations) != 0, "held conversation displayed", page, err)
	rows, err := s.stateDB.Queries().ListHeldCommunityMessages(ctx, db.ListHeldCommunityMessagesParams{Account: identity.Account, Sender: "held", PageSize: 200})
	failIf(t, err != nil || len(rows) != 1, rows, err)
	must(t, s.releaseCommunityHeld(ctx, identity, "held", netip.MustParseAddr("192.0.2.1")))
	must(t, s.communityUpdate(ctx, identity, message))
	rows, err = s.stateDB.Queries().ListHeldCommunityMessages(ctx, db.ListHeldCommunityMessagesParams{Account: identity.Account, Sender: "held", PageSize: 200})
	failIf(t, err != nil || len(rows) != 0, "discarded replay resurrected", rows, err)
	message.Username = "allowed"
	must(t, s.communityUpdate(ctx, identity, message))
	rows, err = s.stateDB.Queries().ListHeldCommunityMessages(ctx, db.ListHeldCommunityMessagesParams{Account: identity.Account, Sender: "allowed", PageSize: 200})
	failIf(t, err != nil || len(rows) != 1, rows, err)
	heldID := rows[0].ID
	// Simulate a read marker advancing while content was withheld.
	if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE community_conversations SET read_through = ? WHERE id = ?", heldID, rows[0].ConversationID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	must(t, s.releaseCommunityHeld(ctx, identity, "allowed", netip.MustParseAddr("198.51.100.1")))
	page, err = s.CommunityConversations(ctx, CommunityConversationsRequest{CommunityIdentity: identity})
	failIf(t, err != nil || len(page.Conversations) != 1 || page.Conversations[0].Unread != 1, "released read marker", page, err)
	messages, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: rows[0].ConversationID})
	failIf(t, err != nil || len(messages.Messages) != 1 || messages.Messages[0].ID <= heldID, messages, err)
}

func TestCommunityIgnoreRoomsFeedAndWalls(t *testing.T) {
	s := downloadService(t)
	client, peer, id := communityTestConnection(t, s)
	ctx := context.Background()
	_, err := s.CommunityRoomAction(ctx, CommunityRoomActionRequest{CommunityIdentity: id, Room: "oto test", Action: "join", RequestID: "join"})
	must(t, err)
	syncTestRooms(t, s, client, peer, id, "room-join-public", "room-invitations-true-client")
	applyRoomFixture(t, s, id, "room-joined")
	rule, err := NormalizeCommunityRule(CommunityRule{Action: "ignore", Kind: "ip", Value: "192.0.2.0/24"})
	must(t, err)
	s.mu.Lock()
	s.community.rules = []CommunityRule{rule}
	s.community.feedWanted, s.community.feedWritten = true, true
	s.mu.Unlock()
	for _, feed := range []bool{false, true} {
		applyRoomEvent(t, s, id, soulseek.RoomMessage{Room: "oto test", Username: "Alice", Text: "hidden until address", PublicFeed: feed})
	}
	applyRoomEvent(t, s, id, soulseek.RoomWallSnapshot{Room: "oto test", Entries: []soulseek.RoomWallEntry{{Username: "Alice", Text: "wall"}}})
	wall, err := s.CommunityRoomWall(ctx, CommunityRoomMembersRequest{CommunityIdentity: id, Room: "oto test"})
	failIf(t, err != nil || len(wall.Entries) != 0, "unresolved wall displayed", wall, err)
	feed, err := s.CommunityFeed(ctx, CommunityFeedRequest{CommunityIdentity: id})
	failIf(t, err != nil || len(feed.Messages) != 0, "unresolved feed displayed", feed, err)
	s.mu.RLock()
	next := s.nextTransientIgnoreSenderLocked("", "")
	s.mu.RUnlock()
	failIf(t, next != "Alice", "transient lookup not scheduled", next)
	must(t, s.releaseCommunityHeld(ctx, id, "Alice", netip.MustParseAddr("198.51.100.1")))
	wall, err = s.CommunityRoomWall(ctx, CommunityRoomMembersRequest{CommunityIdentity: id, Room: "oto test"})
	failIf(t, err != nil || len(wall.Entries) != 1, wall, err)
	feed, err = s.CommunityFeed(ctx, CommunityFeedRequest{CommunityIdentity: id})
	failIf(t, err != nil || len(feed.Messages) != 1, feed, err)
	room := roomSnapshot(t, s, id, "oto test")
	messages, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: id, ConversationID: room.ConversationID})
	failIf(t, err != nil || len(messages.Messages) != 2 || messages.Messages[0].Sender != "Alice", messages, err)
	// A newly observed ignored address suppresses already cached transient text.
	s.mu.Lock()
	user := s.community.users["Alice"]
	user.IP, user.AddressFresh = "192.0.2.1", true
	s.community.users["Alice"] = user
	s.mu.Unlock()
	wall, err = s.CommunityRoomWall(ctx, CommunityRoomMembersRequest{CommunityIdentity: id, Room: "oto test"})
	failIf(t, err != nil || len(wall.Entries) != 0, wall, err)
	feed, err = s.CommunityFeed(ctx, CommunityFeedRequest{CommunityIdentity: id})
	failIf(t, err != nil || len(feed.Messages) != 0, feed, err)
}

func TestCommunityIgnoreAddressExpiresOnPeerDeparture(t *testing.T) {
	s := downloadService(t)
	_, _, id := communityTestConnection(t, s)
	for _, message := range []soulseek.SocialMessage{soulseek.UserPresence{Username: "Alice", Status: soulseek.UserStatusOffline}, soulseek.WatchUserResponse{Username: "Alice", Exists: false}, soulseek.WatchUserResponse{Username: "Alice", Exists: true, Status: soulseek.UserStatusOffline}} {
		s.mu.Lock()
		s.community.users["Alice"] = CommunityUser{Username: "Alice", Status: soulseek.UserStatusOnline, StatusFresh: true, IP: "192.0.2.1", AddressFresh: true}
		s.community.ignoreAddresses = map[string]communityIgnoreAddress{"Alice": {address: netip.MustParseAddr("192.0.2.1"), expires: time.Now().Add(time.Minute)}}
		s.mu.Unlock()
		must(t, s.communityUpdate(context.Background(), id, message))
		s.mu.RLock()
		address := s.communityIgnoreAddressLocked("Alice")
		s.mu.RUnlock()
		failIf(t, address.IsValid(), "departed peer kept an authoritative ignore address")
	}
}
