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
	must(t, err)
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
	failIf(t, roomSnapshot(t, s, id, req.Room).LastRoleAction != nil, "rolled back role action leaked pending state")
	if _, err = s.stateDB.SQL().Exec("DROP TRIGGER fail_role"); err != nil {
		t.Fatal(err)
	}
	req = changeTestRoomRole(t, s, peer, id, soulseek.RoomAddMember, "Bob", "role-add-member-request")
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(cfg, path)
	must(t, err)
	defer s.Close()
	client, peer, fresh := communityTestConnection(t, s)
	req.CommunityIdentity = fresh
	out, err := s.ChangeCommunityRoomRole(ctx, req)
	failIf(t, err != nil || !out.Duplicate || out.State != "unknown", out, err)
	syncTestRooms(t, s, client, peer, fresh, "room-invitations-true-client") // No automatic role replay after restart.
	failIf(t, roomSnapshot(t, s, fresh, req.Room).RoleFresh, "persisted a role as authority")
}

func TestCommunityPrivateRoomWallBoundsAndControls(t *testing.T) {
	s := downloadService(t)
	client, peer, id := communityTestConnection(t, s)
	ctx := context.Background()
	_, err := s.CommunityRoomAction(ctx, CommunityRoomActionRequest{CommunityIdentity: id, Room: "oto test", Action: "join", RequestID: "join"})
	must(t, err)
	syncTestRooms(t, s, client, peer, id, "room-join-public", "room-invitations-true-client")
	applyRoomFixture(t, s, id, "room-joined")
	for _, text := range []string{"bad\x1b[2J", "bad\u202eevil", "two\nlines", strings.Repeat("x", soulseek.MaxChatBytes+1)} {
		if err := s.SetCommunityRoomWall(ctx, CommunityRoomWallRequest{CommunityIdentity: id, Room: "oto test", Text: text, Revision: roomRevision(t, s, id)}); err == nil {
			t.Fatal("invalid wall accepted")
		}
	}
	text := strings.Repeat("<", soulseek.MaxChatBytes)
	must(t, s.SetCommunityRoomWall(ctx, CommunityRoomWallRequest{CommunityIdentity: id, Room: "oto test", Text: text, Revision: roomRevision(t, s, id)}))
	applyRoomEvent(t, s, id, soulseek.RoomWallSnapshot{Room: "oto test", Entries: []soulseek.RoomWallEntry{{Username: "a", Text: text}, {Username: "b", Text: text}}})
	page, err := s.CommunityRoomWall(ctx, CommunityRoomMembersRequest{CommunityIdentity: id, Room: "oto test"})
	failIf(t, err != nil || len(page.Entries) != 1 || page.NextCursor != "a", len(page.Entries), page.NextCursor, err)
	encoded, err := json.Marshal(page)
	failIf(t, err != nil || len(encoded) >= 1<<20, "oversize wall response", len(encoded), err)
	page, err = s.CommunityRoomWall(ctx, CommunityRoomMembersRequest{CommunityIdentity: id, Room: "oto test", Cursor: page.NextCursor})
	failIf(t, err != nil || len(page.Entries) != 1 || page.Entries[0].Username != "b" || page.NextCursor != "", "wall page lost", err)
	applyRoomEvent(t, s, id, soulseek.RoomWallUpdate{Room: "oto test", RoomWallEntry: soulseek.RoomWallEntry{Username: "control", Text: "hello\x1b[2J\u202e"}})
	page, err = s.CommunityRoomWall(ctx, CommunityRoomMembersRequest{CommunityIdentity: id, Room: "oto test", Query: "control"})
	failIf(t, err != nil || len(page.Entries) != 1 || strings.ContainsAny(page.Entries[0].Text, "\x1b\u202e"), "wall terminal controls survived", err)
	for i := 0; ; i++ {
		err = s.communityUpdate(ctx, id, soulseek.RoomWallUpdate{Room: "oto test", RoomWallEntry: soulseek.RoomWallEntry{Username: fmt.Sprint("user", i), Text: text}})
		if err != nil {
			break
		}
		failIf(t, i > communityRoomWallBytes/len(text), "wall byte limit ignored")
	}
	r := s.community.rooms["oto test"]
	before := r.wallBytes
	failIf(t, before > communityRoomWallBytes || !strings.Contains(err.Error(), "byte limit"), before, err)
	applyRoomEvent(t, s, id, soulseek.RoomWallUpdate{Room: "oto test", RoomWallEntry: soulseek.RoomWallEntry{Username: "a"}, Remove: true})
	failIf(t, r.wallBytes != before-len(text)-1, "wall byte accounting", r.wallBytes, before)
	applyRoomEvent(t, s, id, soulseek.RoomWallUpdate{Room: "oto test", RoomWallEntry: soulseek.RoomWallEntry{Username: "b", Text: "short"}})
	failIf(t, r.wallBytes != before-2*len(text)+4, "wall replacement accounting", r.wallBytes, before)
	messages, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: id, ConversationID: r.conversationID})
	failIf(t, err != nil || len(messages.Messages) != 1, "wall was logged as chat", len(messages.Messages), err)
}
