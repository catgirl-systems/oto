package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

func TestCommunityPrivateRoomCreationDirectoryAndLateJoin(t *testing.T) {
	s := downloadService(t)
	client, peer, id := communityTestConnection(t, s)
	ctx := context.Background()
	_, err := s.CommunityRoomAction(ctx, CommunityRoomActionRequest{CommunityIdentity: id, Room: "oto test", Action: "join", Private: true, Remember: true, RequestID: "create"})
	must(t, err)
	applyRoomEvent(t, s, id, soulseek.RoomDirectory{}) // Directory requested before the creation.
	r := s.community.rooms["oto test"]
	failIf(t, !r.creating || !r.autojoin || !r.wanted || r.joined || r.role == "owner", "directory cancelled creation or invented authority", roomSnapshot(t, s, id, "oto test"))
	history, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: id, ConversationID: r.conversationID})
	failIf(t, err != nil || len(history.Messages) != 0, "creation inserted revocation history", err)
	syncTestRooms(t, s, client, peer, id, "room-join-private", "room-invitations-true-client")
	joined := soulseek.RoomJoined{Room: "oto test", Private: true, Owner: s.cfg.Soulseek.Username}
	applyRoomEvent(t, s, id, joined)
	applyRoomFixture(t, s, id, "role-membership-revoked")
	applyRoomEvent(t, s, id, joined) // Late confirmation must not undo revocation.
	failIf(t, r.joined || r.role != "none" || r.autojoin || !r.rejectJoin, "late join restored authority", roomSnapshot(t, s, id, "oto test"))
	syncTestRooms(t, s, client, peer, id, "room-leave")
	applyRoomFixture(t, s, id, "room-left")
	failIf(t, r.joined || r.wanted || r.pending != "", "late-join cleanup failed")
}

// Pause deadline construction after the initial service validation, before the
// transport invokes its reservation callback. Database calls under s.mu cannot
// trigger the barrier, so the interleaving does not depend on scheduler sleeps.
type roomWriteBarrierContext struct {
	context.Context
	service        *Service
	ready, release chan struct{}
	once           sync.Once
}

func (c *roomWriteBarrierContext) Deadline() (time.Time, bool) {
	if c.service.mu.TryLock() {
		c.service.mu.Unlock()
		c.once.Do(func() { close(c.ready); <-c.release })
	}
	return c.Context.Deadline()
}
func TestCommunityPrivateRoomRevisionRecheckedBeforeWrite(t *testing.T) {
	s := downloadService(t)
	client, peer, id := communityTestConnection(t, s)
	applyRoomEvent(t, s, id, soulseek.RoomDirectory{Owned: []soulseek.RoomPopulation{{Room: "oto test"}}})
	syncTestRooms(t, s, client, peer, id, "room-invitations-true-client")
	req := CommunityRoomRoleRequest{CommunityIdentity: id, Room: "oto test", Action: soulseek.RoomAddMember, Username: "Bob", RequestID: "stale-during-write", Revision: roomRevision(t, s, id), Confirm: true}
	ctx := &roomWriteBarrierContext{Context: context.Background(), service: s, ready: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := s.ChangeCommunityRoomRole(ctx, req); done <- err }()
	select {
	case <-ctx.ready:
	case <-time.After(time.Second):
		close(ctx.release)
		t.Fatal("request did not reach transport barrier")
	}
	s.mu.Lock()
	s.community.revision++
	s.mu.Unlock()
	close(ctx.release)
	select {
	case err := <-done:
		failIf(t, !errors.Is(err, ErrCommunityMessageState), "stale mutation written", err)
	case <-time.After(time.Second):
		t.Fatal("stale mutation blocked on network write")
	}
	if _, err := s.stateDB.Queries().GetCommunitySubmission(context.Background(), db.GetCommunitySubmissionParams{Account: id.Account, RequestID: req.RequestID}); err == nil {
		t.Fatal("stale mutation reserved")
	}
	syncTestRooms(t, s, client, peer, id) // No ghost pending operation.
}

func TestCommunityPrivateRoomRemoteCachesReleasedAndBounded(t *testing.T) {
	s := downloadService(t)
	_, _, id := communityTestConnection(t, s)
	text := strings.Repeat("x", soulseek.MaxChatBytes)
	// Sequential room visits retain history/preferences but not remote caches.
	for i := range 12 {
		name := fmt.Sprintf("visited %d", i)
		s.community.rooms[name] = &communityRoomState{wanted: true, intent: 1, issued: 1, private: true, creating: true}
		applyRoomEvent(t, s, id, soulseek.RoomJoined{Room: name, Private: true, Owner: s.cfg.Soulseek.Username, Users: []soulseek.RoomUser{{Username: "Bob"}}})
		applyRoomEvent(t, s, id, soulseek.RoomRoleList{Room: name, Users: []string{"Bob"}})
		applyRoomEvent(t, s, id, soulseek.RoomWallSnapshot{Room: name, Entries: []soulseek.RoomWallEntry{{Username: "Bob", Text: text}}})
		failIf(t, s.community.wallBytes == 0, "wall byte accounting missing")
		applyRoomEvent(t, s, id, soulseek.RoomLeft{Room: name})
		r := s.community.rooms[name]
		failIf(t, s.community.wallBytes != 0 || r.wallBytes != 0 || len(r.wall)+len(r.members)+len(r.privateMembers)+len(r.operators) != 0, "remote room cache survived leave")
		// This is an observed leave, not a new autojoin request.
		r.wanted = false
	}
	for i := range 30 {
		applyRoomEvent(t, s, id, soulseek.RoomDirectory{Member: []soulseek.RoomPopulation{{Room: fmt.Sprintf("directory %d", i)}}})
		failIf(t, len(s.community.rooms) > 13, "directory-only rooms accumulated", len(s.community.rooms))
	}
	var entries []soulseek.RoomWallEntry
	for i := range 63 {
		entries = append(entries, soulseek.RoomWallEntry{Username: fmt.Sprint(i), Text: text})
	}
	for i := range 5 {
		name := fmt.Sprintf("active %d", i)
		s.community.rooms[name] = &communityRoomState{wanted: true, intent: 1, issued: 1}
		applyRoomEvent(t, s, id, soulseek.RoomJoined{Room: name})
		err := s.communityUpdate(context.Background(), id, soulseek.RoomWallSnapshot{Room: name, Entries: entries})
		failIf(t, i < 4 && err != nil || i == 4 && err == nil, "aggregate wall budget", i, err)
		failIf(t, s.community.wallBytes > communityRoomWallsBytes, "aggregate wall bytes exceeded")
	}
	s.mu.Lock()
	s.retireCommunityLocked()
	s.mu.Unlock()
	failIf(t, s.community.wallBytes != 0, "disconnect retained wall cache")
	for _, r := range s.community.rooms {
		failIf(t, len(r.wall)+len(r.members)+len(r.privateMembers)+len(r.operators) != 0, "disconnect retained remote cache")
	}
}

// Hold the directory write after the peer has consumed it, before sync resumes
// iterating the previously captured room names.
type pausedRoomDirectoryConn struct {
	net.Conn
	release <-chan struct{}
}

func (c pausedRoomDirectoryConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	<-c.release
	return n, err
}
func TestCommunityPrivateRoomDirectoryPrunesDuringSync(t *testing.T) {
	s := downloadService(t)
	left, right := net.Pipe()
	defer right.Close()
	_ = right.SetDeadline(time.Now().Add(2 * time.Second))
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	client := soulseek.NewClientOnConn(soulseek.ClientConfig{}, pausedRoomDirectoryConn{Conn: left, release: release})
	s.mu.Lock()
	s.client = client
	s.community.online = true
	s.community.identity.Session++
	id := s.community.identity
	s.community.directoryRefresh = true
	enabled := s.community.invitationsWanted
	s.community.invitationsWritten = &enabled
	s.community.rooms["old directory"] = &communityRoomState{private: true}
	s.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- s.syncCommunityRooms(context.Background(), client, id) }()
	code, _, err := soulseek.ReadFrame(right)
	failIf(t, err != nil || code != 64, code, err)
	applyRoomEvent(t, s, id, soulseek.RoomDirectory{})
	failIf(t, s.community.rooms["old directory"] != nil, "obsolete directory room was not pruned")
	once.Do(func() { close(release) })
	select {
	case err := <-done:
		must(t, err)
	case <-time.After(time.Second):
		t.Fatal("room sync did not finish after pruning")
	}
}
