package daemon

import (
	"bytes"
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

func roomRevision(t *testing.T, s *Service, id CommunityIdentity) uint64 {
	t.Helper()
	page, err := s.CommunityRooms(context.Background(), CommunityRoomsRequest{CommunityIdentity: id})
	must(t, err)
	return page.Revision
}
func applyRoomEvent(t *testing.T, s *Service, id CommunityIdentity, event soulseek.SocialMessage) {
	t.Helper()
	must(t, s.communityUpdate(context.Background(), id, event))
}
func changeTestRoomRole(t *testing.T, s *Service, peer net.Conn, id CommunityIdentity, action soulseek.RoomRoleAction, username, fixture string) CommunityRoomRoleRequest {
	t.Helper()
	req := CommunityRoomRoleRequest{CommunityIdentity: id, Room: "oto test", Action: action, Username: username, RequestID: string(action) + username, Revision: roomRevision(t, s, id), Confirm: true}
	done := make(chan error, 1)
	go func() {
		out, err := s.ChangeCommunityRoomRole(context.Background(), req)
		if err == nil && out.State != "pending" {
			err = errors.New("role write invented confirmation")
		}
		done <- err
	}()
	f := roomFixture(t, fixture)
	code, payload, err := soulseek.ReadFrame(peer)
	failIf(t, err != nil || code != f.Code || !bytes.Equal(payload, f.Payload(t)), code, payload, err)
	must(t, <-done)
	return req
}
func TestCommunityPrivateRoomRolesAndRevocation(t *testing.T) {
	s := downloadService(t)
	client, peer, id := communityTestConnection(t, s)
	ctx := context.Background()
	_, err := s.CommunityRoomAction(ctx, CommunityRoomActionRequest{CommunityIdentity: id, Room: "oto test", Action: "join", Private: true, Remember: true, RequestID: "create"})
	must(t, err)
	syncTestRooms(t, s, client, peer, id, "room-join-private", "room-invitations-true-client")
	failIf(t, roomSnapshot(t, s, id, "oto test").RoleFresh, "creation invented owner role")
	applyRoomEvent(t, s, id, soulseek.RoomJoined{Room: "oto test", Private: true, Owner: s.cfg.Soulseek.Username})
	applyRoomFixture(t, s, id, "role-members")
	if room := roomSnapshot(t, s, id, "oto test"); room.Role != "owner" || !room.RoleFresh || !room.Joined {
		t.Fatal(room)
	}
	for _, item := range []struct {
		action         soulseek.RoomRoleAction
		request, reply string
	}{
		{soulseek.RoomAddMember, "role-add-member-request", "role-add-member"},
		{soulseek.RoomAddOperator, "role-add-operator-request", "role-add-operator"},
		{soulseek.RoomRemoveOperator, "role-remove-operator-request", "role-remove-operator"},
		{soulseek.RoomRemoveMember, "role-remove-member-request", "role-remove-member"},
	} {
		req := changeTestRoomRole(t, s, peer, id, item.action, "Bob", item.request)
		out, err := s.ChangeCommunityRoomRole(ctx, req)
		failIf(t, err != nil || !out.Duplicate || out.State != "pending", out, err)
		applyRoomFixture(t, s, id, item.reply)
		out, err = s.ChangeCommunityRoomRole(ctx, req)
		failIf(t, err != nil || !out.Duplicate || out.State != "confirmed", out, err)
		stored, err := s.stateDB.Queries().GetCommunitySubmission(ctx, db.GetCommunitySubmissionParams{Account: id.Account, RequestID: req.RequestID})
		failIf(t, err != nil || !strings.Contains(stored.Result, `"state":"confirmed"`), stored, err)
		req.Username = "Alice"
		if _, err = s.ChangeCommunityRoomRole(ctx, req); err == nil {
			t.Fatal("request identity reused")
		}
	}
	members, err := s.CommunityRoomMembers(ctx, CommunityRoomMembersRequest{CommunityIdentity: id, Room: "oto test", Private: true, Limit: 1})
	failIf(t, err != nil || !members.MembersFresh || len(members.Members) != 1 || members.NextCursor == "", members, err)
	req := changeTestRoomRole(t, s, peer, id, soulseek.RoomCancelOwnership, "", "role-cancel-ownership-request")
	// A fresh directory, not the write completion, removes ownership.
	applyRoomEvent(t, s, id, soulseek.RoomDirectory{Member: []soulseek.RoomPopulation{{Room: "oto test"}}})
	if room := roomSnapshot(t, s, id, "oto test"); room.Role != "member" || room.LastRoleAction.State != "confirmed" {
		t.Fatal(room)
	}
	if _, err = s.ChangeCommunityRoomRole(ctx, CommunityRoomRoleRequest{CommunityIdentity: id, Room: req.Room, Action: soulseek.RoomAddMember, Username: "Bob", RequestID: "member-denied", Revision: roomRevision(t, s, id), Confirm: true}); err == nil {
		t.Fatal("member changed roles")
	}
	applyRoomFixture(t, s, id, "role-operatorship-granted")
	applyRoomFixture(t, s, id, "role-operators")
	if _, err = s.ChangeCommunityRoomRole(ctx, CommunityRoomRoleRequest{CommunityIdentity: id, Room: req.Room, Action: soulseek.RoomRemoveMember, Username: "Bob", RequestID: "operator-denied", Revision: roomRevision(t, s, id), Confirm: true}); err == nil {
		t.Fatal("operator removed another operator")
	}
	applyRoomFixture(t, s, id, "role-operatorship-revoked")
	failIf(t, roomSnapshot(t, s, id, req.Room).Role != "member", "revoked operator retained authority")
	applyRoomFixture(t, s, id, "role-membership-revoked")
	room := roomSnapshot(t, s, id, req.Room)
	failIf(t, room.Joined || room.Remembered || room.Role != "none" || !strings.Contains(room.Error, "revoked"), room)
	stored, err := s.stateDB.Queries().GetCommunityRoom(ctx, db.GetCommunityRoomParams{Account: id.Account, Room: req.Room})
	failIf(t, err != nil || stored.Autojoin != 0, stored, err)
	if _, err = s.CommunityRoomAction(ctx, CommunityRoomActionRequest{CommunityIdentity: id, Room: req.Room, Action: "join", RequestID: "unauthorized-rejoin"}); err == nil {
		t.Fatal("revoked membership rejoined")
	}
	page, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: id, ConversationID: room.ConversationID})
	failIf(t, err != nil || len(page.Messages) < 2 || !strings.Contains(page.Messages[0].Text, "revoked"), page, err)
	applyRoomEvent(t, s, id, soulseek.RoomDirectory{}) // Clears any pending directory refresh safely.
	s.mu.Lock()
	s.community.directoryRefresh = false
	s.mu.Unlock()
	syncTestRooms(t, s, client, peer, id)
}

func TestCommunityPrivateRoomInvitationAndWallRestart(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg, path := testConfig(t), filepath.Join(t.TempDir(), "state.sqlite3")
	s, err := New(cfg, path)
	must(t, err)
	defer s.Close()
	client, peer, id := communityTestConnection(t, s)
	ctx := context.Background()
	applyRoomFixture(t, s, id, "role-membership-granted")
	page, err := s.CommunityRooms(ctx, CommunityRoomsRequest{CommunityIdentity: id, Mode: "invitations"})
	failIf(t, err != nil || len(page.Rooms) != 1 || page.Rooms[0].Joined, page, err)
	if err = s.SetCommunityRoomInvitations(ctx, CommunityRoomInvitationsRequest{CommunityIdentity: id, Revision: page.Revision, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetCommunityRoomInvitations(ctx, CommunityRoomInvitationsRequest{CommunityIdentity: id, Revision: page.Revision, Enabled: true}); !errors.Is(err, ErrCommunityMessageState) {
		t.Fatal("stale edit", err)
	}
	syncTestRooms(t, s, client, peer, id, "room-invitations-false-client")
	applyRoomFixture(t, s, id, "room-invitations-false-server")
	page, _ = s.CommunityRooms(ctx, CommunityRoomsRequest{CommunityIdentity: id})
	failIf(t, page.InvitationsState != "confirmed" || page.InvitationsEnabled, page)
	if err = s.SetCommunityRoomWall(ctx, CommunityRoomWallRequest{CommunityIdentity: id, Room: "oto test", Text: "hello 世界", Revision: page.Revision}); err != nil {
		t.Fatal(err)
	}
	syncTestRooms(t, s, client, peer, id) // A saved wall is not a reason to join.
	if _, err = s.CommunityRoomAction(ctx, CommunityRoomActionRequest{CommunityIdentity: id, Room: "oto test", Action: "join", Remember: true, RequestID: "join"}); err != nil {
		t.Fatal(err)
	}
	syncTestRooms(t, s, client, peer, id, "room-join-private")
	applyRoomEvent(t, s, id, soulseek.RoomJoined{Room: "oto test", Private: true, Owner: "Owner"})
	applyRoomFixture(t, s, id, "wall-snapshot")
	syncTestRooms(t, s, client, peer, id, "wall-set")
	applyRoomEvent(t, s, id, soulseek.RoomWallUpdate{Room: "oto test", RoomWallEntry: soulseek.RoomWallEntry{Username: cfg.Soulseek.Username, Text: "hello 世界"}})
	wall, err := s.CommunityRoomWall(ctx, CommunityRoomMembersRequest{CommunityIdentity: id, Room: "oto test", Limit: 1})
	failIf(t, err != nil || !wall.Fresh || wall.State != "confirmed" || len(wall.Entries) != 1 || wall.NextCursor == "", wall, err)
	applyRoomFixture(t, s, id, "wall-removed")
	wall, err = s.CommunityRoomWall(ctx, CommunityRoomMembersRequest{CommunityIdentity: id, Room: "oto test"})
	failIf(t, err != nil || len(wall.Entries) != 1, wall, err)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(cfg, path)
	must(t, err)
	defer s.Close()
	client, peer, fresh := communityTestConnection(t, s)
	wall, err = s.CommunityRoomWall(ctx, CommunityRoomMembersRequest{CommunityIdentity: fresh, Room: "oto test"})
	failIf(t, err != nil || wall.Fresh || wall.Room.RoleFresh || wall.OwnText != "hello 世界" || len(wall.Entries) != 0, wall, err)
	syncTestRooms(t, s, client, peer, fresh, "room-invitations-false-client") // Wait for authoritative membership before autojoin.
	applyRoomEvent(t, s, fresh, soulseek.RoomDirectory{Member: []soulseek.RoomPopulation{{Room: "oto test"}}})
	syncTestRooms(t, s, client, peer, fresh, "room-join-private")
	applyRoomEvent(t, s, fresh, soulseek.RoomJoined{Room: "oto test", Private: true, Owner: "Owner"})
	syncTestRooms(t, s, client, peer, fresh, "wall-set")
	if err = s.SetCommunityRoomWall(ctx, CommunityRoomWallRequest{CommunityIdentity: fresh, Room: "oto test", Revision: roomRevision(t, s, fresh)}); err == nil {
		t.Fatal("wall clear lacked confirmation")
	}
	if err = s.SetCommunityRoomWall(ctx, CommunityRoomWallRequest{CommunityIdentity: fresh, Room: "oto test", Revision: roomRevision(t, s, fresh), Confirm: true}); err != nil {
		t.Fatal(err)
	}
	syncTestRooms(t, s, client, peer, fresh, "wall-clear")
	applyRoomEvent(t, s, fresh, soulseek.RoomWallUpdate{Room: "oto test", RoomWallEntry: soulseek.RoomWallEntry{Username: cfg.Soulseek.Username}, Remove: true})
	wall, err = s.CommunityRoomWall(ctx, CommunityRoomMembersRequest{CommunityIdentity: fresh, Room: "oto test"})
	failIf(t, err != nil || wall.State != "confirmed" || wall.OwnText != "", wall, err)
	if err = s.SetCommunityRoomWall(ctx, CommunityRoomWallRequest{CommunityIdentity: id, Room: "oto test", Text: "old", Revision: wall.Revision}); !errors.Is(err, ErrCommunitySession) {
		t.Fatal("old daemon edited wall", err)
	}
}

func TestCommunityPrivateRoomUnknownAndCreationErrors(t *testing.T) {
	s := downloadService(t)
	client, peer, id := communityTestConnection(t, s)
	ctx := context.Background()
	_, err := s.CommunityRoomAction(ctx, CommunityRoomActionRequest{CommunityIdentity: id, Room: "oto test", Private: true, Action: "join", RequestID: "create"})
	must(t, err)
	syncTestRooms(t, s, client, peer, id, "room-join-private", "room-invitations-true-client")
	applyRoomFixture(t, s, id, "role-creation-rejected")
	if room := roomSnapshot(t, s, id, "oto test"); room.Joined || room.RoleFresh || !strings.Contains(room.Error, "rejected") {
		t.Fatal(room)
	}
	syncTestRooms(t, s, client, peer, id)
	applyRoomEvent(t, s, id, soulseek.RoomDirectory{Owned: []soulseek.RoomPopulation{{Room: "oto test"}}})
	req := changeTestRoomRole(t, s, peer, id, soulseek.RoomAddMember, "Bob", "role-add-member-request")
	s.mu.Lock()
	s.community.rooms[req.Room].roleMutation.deadline = time.Now().Add(-time.Second)
	s.community.invitationsDeadline = time.Now().Add(-time.Second)
	s.mu.Unlock()
	syncTestRooms(t, s, client, peer, id)
	out, err := s.ChangeCommunityRoomRole(ctx, req)
	failIf(t, err != nil || out.State != "unknown" || !out.Duplicate, out, err)
	page, _ := s.CommunityRooms(ctx, CommunityRoomsRequest{CommunityIdentity: id})
	failIf(t, page.InvitationsState != "unknown", page.InvitationsState)
	stale := req
	stale.RequestID = "stale"
	if _, err = s.ChangeCommunityRoomRole(ctx, stale); !errors.Is(err, ErrCommunityMessageState) {
		t.Fatal("stale revision accepted", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	stale.Revision = page.Revision
	if _, err = s.ChangeCommunityRoomRole(cancelled, stale); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	applyRoomFixture(t, s, id, "role-add-member") // A late authoritative acknowledgement can resolve Unknown.
	out, err = s.ChangeCommunityRoomRole(ctx, req)
	failIf(t, err != nil || out.State != "confirmed", out, err)
	s.mu.Lock()
	s.retireCommunityLocked()
	s.mu.Unlock()
	stale.Revision = roomRevision(t, s, id)
	if _, err = s.ChangeCommunityRoomRole(ctx, stale); !errors.Is(err, soulseek.ErrNotConnected) {
		t.Fatal(err)
	}
}
