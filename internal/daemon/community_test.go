package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
	"github.com/catgirl-systems/oto/internal/testutil"
)

func communityTestConnection(t *testing.T, s *Service) (*soulseek.Client, net.Conn, CommunityIdentity) {
	t.Helper()
	left, right := net.Pipe()
	_ = right.SetDeadline(time.Now().Add(5 * time.Second))
	t.Cleanup(func() { _ = right.Close() })
	client := soulseek.NewClientOnConn(soulseek.ClientConfig{}, left)
	s.mu.Lock()
	s.client = client
	s.community.identity.Session++
	s.community.online = true
	identity := s.community.identity
	s.mu.Unlock()
	return client, right, identity
}

type communityWatchPacket struct {
	command  uint32
	username string
}

func syncCommunityTestWatches(t *testing.T, s *Service, client *soulseek.Client, peer net.Conn, identity CommunityIdentity, sent map[string]uint64, expected ...communityWatchPacket) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- s.syncUserWatches(ctx, client, identity, sent) }()
	for _, want := range expected {
		command, payload, err := soulseek.ReadFrame(peer)
		must(t, err)
		d := soulseek.NewDecoder(payload)
		username := d.String()
		failIfFmt(t, d.Done() != nil || command != want.command || username != want.username, "watch %d %q, want %+v: %v", command, username, want, d.Done())
	}
	must(t, <-result)
}

func TestCommunityWatchesSharedIdentityAndExpiry(t *testing.T) {
	s := downloadService(t)
	client, peer, identity := communityTestConnection(t, s)
	s.mu.Lock()
	s.setUserWatchesLocked("buddies", []string{"Alice"}, time.Time{})
	s.setUserWatchesLocked("conversations", []string{"Alice"}, time.Time{})
	s.mu.Unlock()
	users := []string{"Alice", "alice", "alice"}
	must(t, s.WatchCommunityUsers(identity, "one", users))
	users[0] = "unrelated" // Caller does not own the stored slice.
	must(t, s.WatchCommunityUsers(identity, "two", []string{"alice"}))
	sent := map[string]uint64{}
	syncCommunityTestWatches(t, s, client, peer, identity, sent,
		communityWatchPacket{soulseek.ServerWatchUser, "Alice"}, communityWatchPacket{soulseek.ServerWatchUser, "alice"})
	must(t, s.WatchCommunityUsers(identity, "one", nil))
	s.mu.Lock()
	s.setUserWatchesLocked("buddies", nil, time.Time{})
	s.mu.Unlock()
	syncCommunityTestWatches(t, s, client, peer, identity, sent)
	s.mu.Lock()
	lease := s.community.watches["frontend:two"]
	lease.expiresAt = time.Now().Add(-time.Second)
	s.community.watches["frontend:two"] = lease
	s.mu.Unlock()
	syncCommunityTestWatches(t, s, client, peer, identity, sent, communityWatchPacket{soulseek.ServerUnwatchUser, "alice"})
	if _, remains := s.community.users["alice"]; remains {
		t.Fatal("expired live cache retained")
	}
	s.mu.Lock()
	s.setUserWatchesLocked("conversations", nil, time.Time{})
	s.mu.Unlock()
	syncCommunityTestWatches(t, s, client, peer, identity, sent, communityWatchPacket{soulseek.ServerUnwatchUser, "Alice"})
	failIf(t, len(s.community.users) != 0, "last consumer did not release cache")
	if err := s.WatchCommunityUsers(identity, "too-many", make([]string, 201)); err == nil {
		t.Fatal("discovery/frontend watch bound not enforced")
	}
	for n := range 64 {
		must(t, s.WatchCommunityUsers(identity, fmt.Sprint(n), []string{"Alice"}))
	}
	if err := s.WatchCommunityUsers(identity, "overflow", []string{"Alice"}); err == nil {
		t.Fatal("unbounded frontend leases")
	}
	if err := s.WatchCommunityUsers(CommunityIdentity{Account: identity.Account, Session: identity.Session + 1}, "bad", nil); !errors.Is(err, ErrCommunitySession) {
		t.Fatal(err)
	}
}

func TestCommunityPresenceFreshnessAndEpoch(t *testing.T) {
	s := downloadService(t)
	_, _, identity := communityTestConnection(t, s)
	ctx := context.Background()
	must(t, s.stateDB.Queries().PutCommunityBuddy(ctx, db.PutCommunityBuddyParams{Account: identity.Account, Username: "Alice"}))
	s.mu.Lock()
	s.setUserWatchesLocked("buddies", []string{"Alice"}, time.Time{})
	s.mu.Unlock()
	apply := func(m soulseek.SocialMessage) {
		t.Helper()
		must(t, s.communityUpdate(ctx, identity, m))
	}
	apply(soulseek.WatchUserResponse{Username: "Alice", Exists: true, Status: soulseek.UserStatusOffline})
	failIf(t, !s.community.users["Alice"].LastSeen.IsZero(), "initial hydration invented last seen")
	if u := s.community.users["Alice"]; u.PrivilegeFresh || !u.PrivilegeUpdatedAt.IsZero() {
		t.Fatal("watch hydration invented privilege information")
	}
	apply(soulseek.UserPresence{Username: "Alice", Status: soulseek.UserStatusOnline, Privileged: true})
	apply(soulseek.UserStatistics{Username: "Alice", Stats: soulseek.UserStats{Files: 99}})
	apply(soulseek.PeerAddress{Username: "Alice", IP: "1.1.1.1", Port: 2234})
	if u := s.community.users["Alice"]; !u.StatusFresh || !u.StatsFresh || !u.AddressFresh || !u.PrivilegeFresh || !u.Privileged || u.PrivilegeUpdatedAt.IsZero() {
		t.Fatalf("freshness: %+v", u)
	}
	s.mu.Lock()
	s.retireCommunityLocked()
	s.mu.Unlock()
	if u := s.community.users["Alice"]; u.StatusFresh || u.StatsFresh || u.AddressFresh || u.PrivilegeFresh || !u.Privileged || u.PrivilegeUpdatedAt.IsZero() || !u.LastSeen.IsZero() {
		t.Fatalf("disconnect: %+v", u)
	}
	if err := s.communityUpdate(ctx, identity, soulseek.UserPresence{Username: "Alice", Status: soulseek.UserStatusOffline}); !errors.Is(err, ErrCommunitySession) {
		t.Fatalf("late disconnected event: %v", err)
	}
	s.mu.Lock()
	s.community.identity.Session++
	s.community.online = true
	s.mu.Unlock()
	if err := s.communityUpdate(ctx, identity, soulseek.PeerAddress{Username: "Alice", IP: "9.9.9.9"}); !errors.Is(err, ErrCommunitySession) {
		t.Fatalf("old address epoch: %v", err)
	}
	identity = s.community.identity
	apply(soulseek.WatchUserResponse{Username: "Alice", Exists: true, Status: soulseek.UserStatusOnline})
	storageTrigger(t, s, "reject_seen", "CREATE TRIGGER reject_seen BEFORE UPDATE ON community_buddies BEGIN SELECT RAISE(ABORT, 'injected failure'); END")
	before := s.community.users["Alice"]
	if err := s.communityUpdate(ctx, identity, soulseek.UserPresence{Username: "Alice", Status: soulseek.UserStatusOffline}); err == nil {
		t.Fatal("failed last-seen persistence accepted")
	}
	failIf(t, s.community.users["Alice"] != before, "failed update published volatile state")
	dropStorageTrigger(t, s, "reject_seen")
	apply(soulseek.UserPresence{Username: "Alice", Status: soulseek.UserStatusOffline})
	row, err := s.stateDB.Queries().GetCommunityBuddy(ctx, db.GetCommunityBuddyParams{Account: identity.Account, Username: "Alice"})
	failIfFmt(t, err != nil || row.LastSeen == nil || *row.LastSeen != s.community.users["Alice"].LastSeen.UnixMilli(), "last seen not durable: %+v %v", row, err)
	before = s.community.users["Alice"]
	apply(soulseek.UserPresence{Username: "Alice", Status: soulseek.UserStatusOffline})
	failIf(t, !s.community.users["Alice"].LastSeen.Equal(before.LastSeen), "duplicate offline transition changed last seen")
	apply(soulseek.UserPresence{Username: "alice", Status: soulseek.UserStatusOnline})
	if _, exists := s.community.users["alice"]; exists {
		t.Fatal("unsolicited case-variant cache created")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.communityUpdate(cancelled, identity, soulseek.UserPresence{Username: "Alice", Status: soulseek.UserStatusOnline}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.cfg.Soulseek.Username = "U" // Even before cache reload, old results are fenced.
	s.mu.Unlock()
	if err := s.communityUpdate(ctx, identity, soulseek.UserPresence{Username: "Alice"}); !errors.Is(err, ErrCommunitySession) {
		t.Fatal(err)
	}
	s.mu.Lock()
	err = s.loadCommunityLocked(ctx)
	s.mu.Unlock()
	failIfFmt(t, err != nil || len(s.community.users) != 0 || len(s.community.watches["buddies"].users) != 0, "account switch retained users: %v", err)
}

func TestCommunityWatchesRestoreStorage(t *testing.T) {
	s := downloadService(t)
	ctx := context.Background()
	account := accountKey(s.cfg)
	err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		for i := range 205 {
			if err := q.PutCommunityBuddy(ctx, db.PutCommunityBuddyParams{Account: account, Username: fmt.Sprintf("buddy%03d", i)}); err != nil {
				return err
			}
		}
		if err := q.SetCommunityBuddyLastSeen(ctx, db.SetCommunityBuddyLastSeenParams{Account: account, Username: "buddy000", SeenAt: 123456}); err != nil {
			return err
		}
		for _, username := range []string{"Alice", "alice", "closed"} {
			row, err := q.EnsureCommunityConversation(ctx, db.EnsureCommunityConversationParams{Account: account, Kind: "private", Target: username})
			if err != nil {
				return err
			}
			if username == "closed" {
				if _, err = q.SetCommunityConversationClosed(ctx, db.SetCommunityConversationClosedParams{Account: account, ID: row.ID, Closed: 1}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	must(t, err)
	must(t, s.Close())
	restored, err := New(s.cfg, s.journalPath)
	must(t, err)
	defer restored.Close()
	failIfFmt(t, len(restored.community.users) != 207, "paged watch restoration: %d", len(restored.community.users))
	if got := restored.community.users["buddy000"]; got.LastSeen.UnixMilli() != 123456 || got.StatusFresh || got.StatsFresh || got.AddressFresh {
		t.Fatalf("restored presence: %+v", got)
	}
	if !reflect.DeepEqual(restored.community.watches["conversations"].users, []string{"Alice", "alice"}) {
		t.Fatal("open conversations not restored exactly")
	}
}

func TestCommunityWatchesReconnect(t *testing.T) {
	fixtures := testutil.SocialFixtureMap(t, "login-ok", "watch-online")
	login, watch := fixtures["login-ok"].Payload(t), fixtures["watch-online"].Payload(t)
	connections := make(chan net.Conn, 4)
	watches := make(chan string, 16)
	var sessions atomic.Int32
	server := testutil.ListenScript(t, func(ctx context.Context, conn net.Conn) error {
		command, _, err := soulseek.ReadFrame(conn)
		if err != nil {
			return err
		}
		if command != soulseek.ServerLogin {
			return fmt.Errorf("expected login, got %d", command)
		}
		if err := soulseek.WriteFrame(conn, soulseek.ServerLogin, login); err != nil {
			return err
		}
		session := sessions.Add(1)
		connections <- conn
		for {
			command, payload, err := soulseek.ReadFrame(conn)
			if err != nil {
				return nil
			} // A local lifecycle test deliberately closes it.
			if command == soulseek.ServerWatchUser {
				username := soulseek.NewDecoder(payload).String()
				if err != nil {
					return err
				}
				watches <- fmt.Sprintf("%d:%s", session, username)
				if username == "Alice" {
					if err := soulseek.WriteFrame(conn, soulseek.ServerWatchUser, watch); err != nil {
						return nil
					}
				}
			}
		}
	})
	cfg := testConfig(t)
	cfg.Soulseek.Server, cfg.Soulseek.ListenAddr = server.Listener.Addr().String(), closedAddress(t)
	s, err := New(cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
	must(t, err)
	defer s.Close()
	s.mu.Lock()
	s.setUserWatchesLocked("buddies", []string{"Alice"}, time.Time{})
	s.setUserWatchesLocked("room:local", []string{"Alice", "Bob"}, time.Time{})
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	must(t, s.Start(ctx))
	readWatches := func(session int) {
		t.Helper()
		for _, username := range []string{"Alice", "Bob"} {
			select {
			case got := <-watches:
				failIf(t, got != fmt.Sprintf("%d:%s", session, username), got)
			case <-time.After(3 * time.Second):
				t.Fatal("watch not restored")
			}
		}
	}
	readWatches(1)
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.community.users["Alice"].StatusFresh })
	s.mu.RLock()
	old := s.community.identity
	s.mu.RUnlock()
	first := <-connections
	_ = first.Close()
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return !s.community.online })
	s.mu.RLock()
	stale := s.community.users["Alice"]
	s.mu.RUnlock()
	failIfFmt(t, stale.StatusFresh || stale.StatsFresh || stale.AddressFresh || !stale.LastSeen.IsZero(), "disconnect manufactured presence: %+v", stale)
	readWatches(2)
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.community.users["Alice"].StatusFresh })
	if err := s.communityUpdate(ctx, old, soulseek.UserPresence{Username: "Alice", Status: soulseek.UserStatusOffline}); !errors.Is(err, ErrCommunitySession) {
		t.Fatalf("old session accepted: %v", err)
	}
	must(t, s.SetPresence(PresenceOffline))
	s.mu.RLock()
	stale = s.community.users["Alice"]
	s.mu.RUnlock()
	failIfFmt(t, stale.StatusFresh || !stale.LastSeen.IsZero(), "manual offline: %+v", stale)
}

func TestCommunityWatchWriteCancellation(t *testing.T) {
	s := downloadService(t)
	client, _, identity := communityTestConnection(t, s)
	must(t, s.WatchCommunityUsers(identity, "one", []string{"Alice"}))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- s.syncUserWatches(ctx, client, identity, map[string]uint64{}) }()
	// The stalled server writer must not hold the daemon lock or block updates.
	must(t, s.communityUpdate(context.Background(), identity, soulseek.UserPresence{Username: "Alice", Status: soulseek.UserStatusOnline}))
	select {
	case err := <-result:
		failIf(t, err == nil, "blocked watch write succeeded")
	case <-time.After(time.Second):
		t.Fatal("watch write ignored cancellation")
	}
}

func TestCommunityWatchReacquireBeforeFlush(t *testing.T) {
	s := downloadService(t)
	client, peer, identity := communityTestConnection(t, s)
	set := func(users []string) {
		t.Helper()
		must(t, s.WatchCommunityUsers(identity, "one", users))
	}
	set([]string{"Alice"})
	sent := map[string]uint64{}
	watch := communityWatchPacket{soulseek.ServerWatchUser, "Alice"}
	syncCommunityTestWatches(t, s, client, peer, identity, sent, watch)
	set(nil)
	set([]string{"Alice"}) // Neither change has reached the wire yet.
	syncCommunityTestWatches(t, s, client, peer, identity, sent, watch)
	must(t, s.communityUpdate(context.Background(), identity, soulseek.WatchUserResponse{Username: "Alice", Exists: true, Status: soulseek.UserStatusOnline}))
	failIf(t, !s.community.users["Alice"].StatusFresh, "reacquired user never hydrated")
	syncCommunityTestWatches(t, s, client, peer, identity, sent)
}

func TestCommunityExpiredWatchAdmissionOffline(t *testing.T) {
	s := downloadService(t)
	identity := s.community.identity
	for n := range 64 {
		must(t, s.WatchCommunityUsers(identity, fmt.Sprint(n), []string{"Alice"}))
	}
	s.mu.Lock()
	for key, lease := range s.community.watches {
		if !lease.expiresAt.IsZero() {
			lease.expiresAt = time.Now().Add(-time.Second)
			s.community.watches[key] = lease
		}
	}
	s.mu.Unlock()
	must(t, s.WatchCommunityUsers(identity, "new", []string{"Bob"}))
	failIf(t, len(s.community.users) != 1 || s.community.users["Bob"].Username != "Bob", "expired offline cache not reclaimed")
}

func TestCommunityOfflineAccountCycleFencesRequests(t *testing.T) {
	s := downloadService(t)
	ctx := context.Background()
	before, err := s.CommunitySummary(ctx)
	must(t, err)
	must(t, s.WatchCommunityUsers(before.CommunityIdentity, "old", []string{"Alice"}))
	s.mu.RLock()
	original := s.cfg
	s.mu.RUnlock()
	other := original
	other.Soulseek.Username = "other"
	must(t, s.UpdateConfig(other))
	must(t, s.UpdateConfig(original))
	// No summary or connection between A -> B -> A: polling must not be
	// responsible for invalidating mutations or clearing frontend leases.
	if err := s.WatchCommunityUsers(before.CommunityIdentity, "old", []string{"Alice"}); !errors.Is(err, ErrCommunitySession) {
		t.Fatalf("old mutation survived offline account cycle: %v", err)
	}
	if _, err := s.CommunityUsers(ctx, CommunityUsersRequest{CommunityIdentity: before.CommunityIdentity}); !errors.Is(err, ErrCommunitySession) {
		t.Fatalf("old read survived offline account cycle: %v", err)
	}
	after, err := s.CommunitySummary(ctx)
	must(t, err)
	failIfFmt(t, before.Account != after.Account || before.Daemon != after.Daemon || after.Session <= before.Session, "reused identity: %+v %+v", before, after)
	users, err := s.CommunityUsers(ctx, CommunityUsersRequest{CommunityIdentity: after.CommunityIdentity})
	failIfFmt(t, err != nil || len(users.Users) != 0, "old leases restored: %+v %v", users, err)
}
