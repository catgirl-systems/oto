package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestCommunityPrivateRoomRoleStorageFailureAndUnknownRestart(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg, path := testConfig(t), filepath.Join(t.TempDir(), "state.sqlite3")
	s, err := New(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	client, peer, id := communityTestConnection(t, s)
	ctx := context.Background()
	applyRoomEvent(t, s, id, soulseek.RoomDirectory{Owned: []soulseek.RoomPopulation{{Room: "oto test"}}})
	syncTestRooms(t, s, client, peer, id, "room-invitations-true-client")
	if _, err = s.stateDB.SQL().Exec(`CREATE TRIGGER fail_role BEFORE INSERT ON community_submissions BEGIN SELECT RAISE(ABORT,'test storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	req := CommunityRoomRoleRequest{CommunityIdentity: id, Room: "oto test", Action: soulseek.RoomAddMember, Username: "Bob", RequestID: "storage", Revision: roomRevision(t, s, id), Confirm: true}
	if _, err = s.ChangeCommunityRoomRole(ctx, req); err == nil {
		t.Fatal("unpersisted role action written")
	}
	if roomSnapshot(t, s, id, req.Room).LastRoleAction != nil {
		t.Fatal("rolled back role action leaked pending state")
	}
	if _, err = s.stateDB.SQL().Exec("DROP TRIGGER fail_role"); err != nil {
		t.Fatal(err)
	}
	req = changeTestRoomRole(t, s, peer, id, soulseek.RoomAddMember, "Bob", "role-add-member-request")
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	client, peer, fresh := communityTestConnection(t, s)
	req.CommunityIdentity = fresh
	out, err := s.ChangeCommunityRoomRole(ctx, req)
	if err != nil || !out.Duplicate || out.State != "unknown" {
		t.Fatal(out, err)
	}
	syncTestRooms(t, s, client, peer, fresh, "room-invitations-true-client") // No automatic role replay after restart.
	if roomSnapshot(t, s, fresh, req.Room).RoleFresh {
		t.Fatal("persisted a role as authority")
	}
}

func TestCommunityPrivateRoomWallBoundsAndControls(t *testing.T) {
	s := downloadService(t)
	client, peer, id := communityTestConnection(t, s)
	ctx := context.Background()
	if _, err := s.CommunityRoomAction(ctx, CommunityRoomActionRequest{CommunityIdentity: id, Room: "oto test", Action: "join", RequestID: "join"}); err != nil {
		t.Fatal(err)
	}
	syncTestRooms(t, s, client, peer, id, "room-join-public", "room-invitations-true-client")
	applyRoomFixture(t, s, id, "room-joined")
	for _, text := range []string{"bad\x1b[2J", "bad\u202eevil", "two\nlines", strings.Repeat("x", soulseek.MaxChatBytes+1)} {
		if err := s.SetCommunityRoomWall(ctx, CommunityRoomWallRequest{CommunityIdentity: id, Room: "oto test", Text: text, Revision: roomRevision(t, s, id)}); err == nil {
			t.Fatal("invalid wall accepted")
		}
	}
	text := strings.Repeat("<", soulseek.MaxChatBytes)
	if err := s.SetCommunityRoomWall(ctx, CommunityRoomWallRequest{CommunityIdentity: id, Room: "oto test", Text: text, Revision: roomRevision(t, s, id)}); err != nil {
		t.Fatal(err)
	}
	applyRoomEvent(t, s, id, soulseek.RoomWallSnapshot{Room: "oto test", Entries: []soulseek.RoomWallEntry{{Username: "a", Text: text}, {Username: "b", Text: text}}})
	page, err := s.CommunityRoomWall(ctx, CommunityRoomMembersRequest{CommunityIdentity: id, Room: "oto test"})
	if err != nil || len(page.Entries) != 1 || page.NextCursor != "a" {
		t.Fatal(len(page.Entries), page.NextCursor, err)
	}
	encoded, err := json.Marshal(page)
	if err != nil || len(encoded) >= 1<<20 {
		t.Fatal("oversize wall response", len(encoded), err)
	}
	page, err = s.CommunityRoomWall(ctx, CommunityRoomMembersRequest{CommunityIdentity: id, Room: "oto test", Cursor: page.NextCursor})
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Username != "b" || page.NextCursor != "" {
		t.Fatal("wall page lost", err)
	}
	applyRoomEvent(t, s, id, soulseek.RoomWallUpdate{Room: "oto test", RoomWallEntry: soulseek.RoomWallEntry{Username: "control", Text: "hello\x1b[2J\u202e"}})
	page, err = s.CommunityRoomWall(ctx, CommunityRoomMembersRequest{CommunityIdentity: id, Room: "oto test", Query: "control"})
	if err != nil || len(page.Entries) != 1 || strings.ContainsAny(page.Entries[0].Text, "\x1b\u202e") {
		t.Fatal("wall terminal controls survived", err)
	}
	for i := 0; ; i++ {
		err = s.communityUpdate(ctx, id, soulseek.RoomWallUpdate{Room: "oto test", RoomWallEntry: soulseek.RoomWallEntry{Username: fmt.Sprint("user", i), Text: text}})
		if err != nil {
			break
		}
		if i > communityRoomWallBytes/len(text) {
			t.Fatal("wall byte limit ignored")
		}
	}
	r := s.community.rooms["oto test"]
	before := r.wallBytes
	if before > communityRoomWallBytes || !strings.Contains(err.Error(), "byte limit") {
		t.Fatal(before, err)
	}
	applyRoomEvent(t, s, id, soulseek.RoomWallUpdate{Room: "oto test", RoomWallEntry: soulseek.RoomWallEntry{Username: "a"}, Remove: true})
	if r.wallBytes != before-len(text)-1 {
		t.Fatal("wall byte accounting", r.wallBytes, before)
	}
	applyRoomEvent(t, s, id, soulseek.RoomWallUpdate{Room: "oto test", RoomWallEntry: soulseek.RoomWallEntry{Username: "b", Text: "short"}})
	if r.wallBytes != before-2*len(text)+4 {
		t.Fatal("wall replacement accounting", r.wallBytes, before)
	}
	messages, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: id, ConversationID: r.conversationID})
	if err != nil || len(messages.Messages) != 1 {
		t.Fatal("wall was logged as chat", len(messages.Messages), err)
	}
}
