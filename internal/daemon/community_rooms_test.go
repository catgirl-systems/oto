package daemon

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
	"github.com/catgirl-systems/oto/internal/testutil"
)

func roomFixture(t testing.TB, name string) testutil.WireFixture {
	t.Helper()
	for _, f := range testutil.SocialFixtures(t) {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("missing room fixture %s", name)
	return testutil.WireFixture{}
}
func applyRoomFixture(t *testing.T, s *Service, identity CommunityIdentity, name string) {
	t.Helper()
	f := roomFixture(t, name)
	msg, err := soulseek.DecodeServerMessage(f.Code, f.Payload(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.communityUpdate(context.Background(), identity, msg.(soulseek.SocialMessage)); err != nil {
		t.Fatal(err)
	}
}
func syncTestRooms(t *testing.T, s *Service, c *soulseek.Client, peer net.Conn, identity CommunityIdentity, names ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.syncCommunityRooms(ctx, c, identity) }()
	for _, name := range names {
		f := roomFixture(t, name)
		code, p, err := soulseek.ReadFrame(peer)
		if err != nil || code != f.Code || !bytes.Equal(p, f.Payload(t)) {
			t.Fatalf("%s: %d %x %v", name, code, p, err)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("room worker did not finish", ctx.Err())
	}
}
func roomSnapshot(t *testing.T, s *Service, identity CommunityIdentity, name string) CommunityRoom {
	t.Helper()
	page, err := s.CommunityRooms(context.Background(), CommunityRoomsRequest{CommunityIdentity: identity, Room: name})
	if err != nil || len(page.Rooms) != 1 {
		t.Fatal(page, err)
	}
	return page.Rooms[0]
}

func TestCommunityRoomLifecycleHistoryAndWatches(t *testing.T) {
	s := downloadService(t)
	client, peer, identity := communityTestConnection(t, s)
	ctx := context.Background()
	applyRoomFixture(t, s, identity, "room-directory")
	join := CommunityRoomActionRequest{CommunityIdentity: identity, Room: "oto test", Action: "join", Remember: true, RequestID: "join"}
	result, err := s.CommunityRoomAction(ctx, join)
	if err != nil || result.Room.Joined || result.Room.State != "join-pending" || !result.Room.Remembered {
		t.Fatal("join invented membership", result, err)
	}
	if _, err := s.SendCommunityRoom(ctx, CommunityRoomSendRequest{CommunityIdentity: identity, Room: "oto test", Text: "too early", RequestID: "early"}); err == nil {
		t.Fatal("sent without confirmation")
	}
	syncTestRooms(t, s, client, peer, identity, "room-join-public", "room-invitations-true-client")
	if r := roomSnapshot(t, s, identity, "oto test"); r.Joined || r.State != "joining" {
		t.Fatal(r)
	}
	applyRoomFixture(t, s, identity, "room-joined")
	r := roomSnapshot(t, s, identity, "oto test")
	if !r.Joined || !r.RosterFresh || r.Population != 2 || r.ConversationID == 0 {
		t.Fatal("confirmation", r)
	}
	if s.community.users["Alice"].Country != "NL" || !s.community.users["Bob"].StatsFresh {
		t.Fatal("roster hydration")
	}
	if err := s.WatchCommunityUsers(identity, "inspector", []string{"Bob"}); err != nil {
		t.Fatal(err)
	}
	applyRoomFixture(t, s, identity, "room-user-left")
	if _, ok := s.community.users["Bob"]; !ok {
		t.Fatal("room leave removed another consumer's watch")
	}
	applyRoomFixture(t, s, identity, "room-user-joined")
	for range 2 {
		applyRoomFixture(t, s, identity, "room-echo")
	} // Identical room messages are not PM replays.
	if err := s.communityUpdate(ctx, identity, soulseek.RoomMessage{Room: "oto test", Username: s.cfg.Soulseek.Username, Text: "own echo"}); err != nil {
		t.Fatal(err)
	}
	page, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: r.ConversationID})
	if err != nil || len(page.Messages) != 4 || page.Conversation.Unread != 2 || page.Messages[0].Direction != "outgoing" {
		t.Fatal(page, err)
	}
	if err := s.CommunityConversationAction(ctx, CommunityConversationActionRequest{CommunityIdentity: identity, ConversationID: r.ConversationID, Action: "close"}); err != nil {
		t.Fatal(err)
	}
	if !roomSnapshot(t, s, identity, "oto test").Joined {
		t.Fatal("close left membership")
	}
	if _, err := s.OpenCommunityConversation(ctx, CommunityOpenConversationRequest{CommunityIdentity: identity, Room: "oto test"}); err != nil {
		t.Fatal(err)
	}
	private, err := s.OpenCommunityConversation(ctx, CommunityOpenConversationRequest{CommunityIdentity: identity, Username: "oto test"})
	if err != nil || private.ID == r.ConversationID {
		t.Fatal("room/private identity collision", err)
	}
	leave := CommunityRoomActionRequest{CommunityIdentity: identity, Room: "oto test", Action: "leave", RequestID: "leave"}
	if _, err := s.CommunityRoomAction(ctx, leave); err != nil {
		t.Fatal(err)
	}
	if r := roomSnapshot(t, s, identity, "oto test"); !r.Joined || r.State != "leave-pending" {
		t.Fatal("optimistic leave", r)
	}
	syncTestRooms(t, s, client, peer, identity, "room-leave")
	applyRoomFixture(t, s, identity, "room-left")
	if r := roomSnapshot(t, s, identity, "oto test"); r.Joined || !r.Remembered {
		t.Fatal("leave changed autojoin", r)
	}
	duplicate, err := s.CommunityRoomAction(ctx, join)
	if err != nil || !duplicate.Duplicate || s.community.rooms["oto test"].wanted {
		t.Fatal("old join retry overwrote leave", err)
	}
	join.RequestID = "join-again"
	if _, err := s.CommunityRoomAction(ctx, join); err != nil {
		t.Fatal(err)
	}
	syncTestRooms(t, s, client, peer, identity, "room-join-public")
	applyRoomFixture(t, s, identity, "room-joined")
	page, err = s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: r.ConversationID})
	if err != nil || !strings.Contains(page.Messages[0].Text, "History gap") {
		t.Fatal("missing rejoin gap", err)
	}
	summary, _ := s.CommunitySummary(ctx)
	forget := CommunityRoomActionRequest{CommunityIdentity: identity, Room: "oto test", Action: "forget", RequestID: "forget", Revision: summary.Revision - 1}
	if _, err := s.CommunityRoomAction(ctx, forget); !errors.Is(err, ErrCommunityMessageState) {
		t.Fatal("stale preference accepted", err)
	}
	forget.Revision = summary.Revision
	if _, err := s.CommunityRoomAction(ctx, forget); err != nil {
		t.Fatal(err)
	}
	if r := roomSnapshot(t, s, identity, "oto test"); !r.Joined || r.Remembered {
		t.Fatal("forget left room", r)
	}
	if _, err := s.ExportCommunityHistory(ctx, CommunityExportRequest{CommunityIdentity: identity, ConversationID: r.ConversationID, Format: "json"}); err != nil {
		t.Fatal(err)
	}
}

func TestCommunityRoomFailuresAndReadOnlyOpen(t *testing.T) {
	s := downloadService(t)
	client, peer, identity := communityTestConnection(t, s)
	ctx := context.Background()
	conversation, err := s.OpenCommunityConversation(ctx, CommunityOpenConversationRequest{CommunityIdentity: identity, Room: "oto test"})
	if err != nil {
		t.Fatal(err)
	}
	syncTestRooms(t, s, client, peer, identity, "room-invitations-true-client")
	if s.community.rooms["oto test"].wanted {
		t.Fatal("history open joined")
	}
	join := CommunityRoomActionRequest{CommunityIdentity: identity, Room: "oto test", Action: "join", RequestID: "join"}
	if _, err := s.CommunityRoomAction(ctx, join); err != nil {
		t.Fatal(err)
	}
	syncTestRooms(t, s, client, peer, identity, "room-join-public")
	if _, err := s.stateDB.SQL().Exec(`CREATE TRIGGER fail_room BEFORE INSERT ON community_messages BEGIN SELECT RAISE(ABORT,'test storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	f := roomFixture(t, "room-joined")
	msg, err := soulseek.DecodeServerMessage(f.Code, f.Payload(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.communityUpdate(ctx, identity, msg.(soulseek.SocialMessage)); err == nil {
		t.Fatal("failed history commit published membership")
	}
	if len(s.community.users) != 0 || roomSnapshot(t, s, identity, "oto test").Joined {
		t.Fatal("rolled back join leaked authority")
	}
	if _, err := s.stateDB.SQL().Exec("DROP TRIGGER fail_room"); err != nil {
		t.Fatal(err)
	}
	applyRoomFixture(t, s, identity, "room-joined")
	s.mu.Lock()
	s.retireCommunityLocked()
	s.mu.Unlock()
	if r := roomSnapshot(t, s, identity, "oto test"); r.Joined || r.RosterFresh || r.State != "offline" {
		t.Fatal("disconnect kept membership", r)
	}
	if err := s.communityUpdate(ctx, identity, soulseek.RoomMessage{Room: "oto test", Username: "Alice", Text: "stale"}); !errors.Is(err, ErrCommunitySession) {
		t.Fatal(err)
	}
	if _, err := s.SendCommunityRoom(ctx, CommunityRoomSendRequest{CommunityIdentity: identity, Room: "oto test", Text: "offline", RequestID: "offline"}); !errors.Is(err, soulseek.ErrNotConnected) {
		t.Fatal("offline room send", err)
	}
	page, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: conversation.ID})
	if err != nil || len(page.Messages) != 1 {
		t.Fatal("offline history lost", err)
	}
	s.mu.Lock()
	s.community.online = true
	s.community.rooms["oto test"].wanted = true
	s.community.rooms["oto test"].intent++
	s.mu.Unlock()
	syncTestRooms(t, s, client, peer, identity, "room-join-public", "room-invitations-true-client")
	if err := s.communityUpdate(ctx, identity, soulseek.PrivateMessage{ID: 1, Timestamp: 1, Username: "server", Text: "Could not create room", New: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(roomSnapshot(t, s, identity, "oto test").Error, "Chats -> server") {
		t.Fatal("server PM error not surfaced")
	}
	s.mu.Lock()
	s.community.rooms["oto test"].deadline = time.Now().Add(-time.Second)
	s.mu.Unlock()
	syncTestRooms(t, s, client, peer, identity) // Timeout never blindly retries.
	if r := roomSnapshot(t, s, identity, "oto test"); r.State != "unknown" {
		t.Fatal(r)
	}
}

func TestCommunityRoomPagesAndBoundedFeed(t *testing.T) {
	s := downloadService(t)
	client, peer, identity := communityTestConnection(t, s)
	ctx := context.Background()
	directory := soulseek.RoomDirectory{}
	for i := range 305 {
		directory.Public = append(directory.Public, soulseek.RoomPopulation{Room: fmt.Sprintf("room-%03d", i), Users: uint32(i), UsersKnown: i != 0})
	}
	if err := s.communityUpdate(ctx, identity, directory); err != nil {
		t.Fatal(err)
	}
	first, err := s.CommunityRooms(ctx, CommunityRoomsRequest{CommunityIdentity: identity, Limit: 999})
	if err != nil || len(first.Rooms) != 200 || first.Rooms[0].PopulationKnown || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	last, err := s.CommunityRooms(ctx, CommunityRoomsRequest{CommunityIdentity: identity, Cursor: first.NextCursor, Query: "ROOM"})
	if err != nil || len(last.Rooms) != 105 || last.NextCursor != "" {
		t.Fatal(last, err)
	}
	if _, err := s.CommunityRoomAction(ctx, CommunityRoomActionRequest{CommunityIdentity: identity, Room: "oto test", Action: "join", RequestID: "join"}); err != nil {
		t.Fatal(err)
	}
	syncTestRooms(t, s, client, peer, identity, "room-join-public", "room-invitations-true-client")
	joined := soulseek.RoomJoined{Room: "oto test"}
	for i := range 305 {
		joined.Users = append(joined.Users, soulseek.RoomUser{Username: fmt.Sprintf("%03d", i) + strings.Repeat("<", 1000), Status: soulseek.UserStatusOnline, StatusKnown: true})
	}
	if err := s.communityUpdate(ctx, identity, joined); err != nil {
		t.Fatal(err)
	}
	members, err := s.CommunityRoomMembers(ctx, CommunityRoomMembersRequest{CommunityIdentity: identity, Room: "oto test"})
	encoded, _ := json.Marshal(members)
	if err != nil || len(encoded) > communityPageBytes+2048 || len(members.Members) >= 200 || members.NextCursor == "" {
		t.Fatal("roster escaped JSON budget", len(encoded), len(members.Members), err)
	}
	feed := soulseek.RoomMessage{Room: "oto test", Username: "Alice", Text: strings.Repeat("<", soulseek.MaxChatBytes), PublicFeed: true}
	if err := s.communityUpdate(ctx, identity, feed); err != nil {
		t.Fatal(err)
	}
	if len(s.community.feed) != 0 {
		t.Fatal("feed implicitly subscribed")
	}
	if err := s.SetCommunityFeed(ctx, CommunityFeedSubscription{CommunityIdentity: identity, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	syncTestRooms(t, s, client, peer, identity, "public-feed-subscribe")
	for range 205 {
		if err := s.communityUpdate(ctx, identity, feed); err != nil {
			t.Fatal(err)
		}
	}
	if s.community.feedBytes > 1<<20 || len(s.community.feed) > 200 {
		t.Fatal("unbounded feed")
	}
	page, err := s.CommunityFeed(ctx, CommunityFeedRequest{CommunityIdentity: identity})
	encoded, _ = json.Marshal(page)
	if err != nil || len(page.Messages) != 1 || page.NextCursor == 0 || len(encoded) > communityPageBytes+2048 {
		t.Fatal("feed budget", len(encoded), err)
	}
	if err := s.SetCommunityFeed(ctx, CommunityFeedSubscription{CommunityIdentity: identity}); err != nil {
		t.Fatal(err)
	}
	syncTestRooms(t, s, client, peer, identity, "public-feed-unsubscribe")
	id := s.community.feedID
	if err := s.communityUpdate(ctx, identity, feed); err != nil {
		t.Fatal(err)
	}
	if s.community.feedID != id {
		t.Fatal("unsubscribed late feed accepted")
	}
	var count int
	if err := s.stateDB.SQL().QueryRow("SELECT count(*) FROM community_messages").Scan(&count); err != nil || count != 1 {
		t.Fatal("feed logged by default", count, err)
	}
}

func TestCommunityRoomPreferencesSurviveRestart(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg, path := testConfig(t), filepath.Join(t.TempDir(), "state.sqlite3")
	s, err := New(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	client, peer, identity := communityTestConnection(t, s)
	if _, err := s.CommunityRoomAction(ctx, CommunityRoomActionRequest{CommunityIdentity: identity, Room: "oto test", Action: "join", Remember: true, RequestID: "join"}); err != nil {
		t.Fatal(err)
	}
	syncTestRooms(t, s, client, peer, identity, "room-join-public", "room-invitations-true-client")
	applyRoomFixture(t, s, identity, "room-joined")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fresh := s.community.identity
	room := roomSnapshot(t, s, fresh, "oto test")
	if !room.Remembered || room.Joined || room.RosterFresh || room.ConversationID == 0 {
		t.Fatal("persisted authority", room)
	}
	if _, err := s.CommunityRooms(ctx, CommunityRoomsRequest{CommunityIdentity: identity}); !errors.Is(err, ErrCommunitySession) {
		t.Fatal("restart fence", err)
	}
	client, peer, fresh = communityTestConnection(t, s)
	syncTestRooms(t, s, client, peer, fresh, "room-join-public", "room-invitations-true-client")
	applyRoomFixture(t, s, fresh, "room-joined")
	page, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: fresh, ConversationID: room.ConversationID})
	if err != nil || len(page.Messages) != 2 || !strings.Contains(page.Messages[0].Text, "History gap") {
		t.Fatal("restart gap/history", err)
	}
	// Database/account isolation remains independent of presentation folding.
	if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error { return db.New(tx).EnsureCommunityAccount(ctx, "other") }); err != nil {
		t.Fatal(err)
	}
}
