package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

func TestCommunityBuddiesCRUDVersionsAndWatchOwnership(t *testing.T) {
	s := downloadService(t)
	ctx := context.Background()
	id := s.community.identity
	req := CommunityBuddyRequest{CommunityIdentity: id, Username: "Alice", Note: "猫\nprivate note", NotifyOnline: true, Priority: true, Trusted: true}
	first, err := s.SetCommunityBuddy(ctx, req)
	must(t, err)
	replay, err := s.SetCommunityBuddy(ctx, req)
	failIf(t, err != nil || replay.Buddy.Revision != first.Buddy.Revision, "idempotent put", err, replay)
	if _, err := s.SetCommunityBuddy(ctx, CommunityBuddyRequest{CommunityIdentity: id, Username: "alice"}); err != nil {
		t.Fatal(err)
	}
	req.Revision = &first.Buddy.Revision
	req.Note = "edited"
	second, err := s.SetCommunityBuddy(ctx, req)
	must(t, err)
	req.Note = "stale frontend"
	if _, err := s.SetCommunityBuddy(ctx, req); !errors.Is(err, ErrCommunityMessageState) {
		t.Fatal("stale note overwrote buddy", err)
	}
	must(t, s.WatchCommunityUsers(id, "second frontend", []string{"Alice"}))
	req.Remove = true
	req.Revision = &second.Buddy.Revision
	if _, err := s.SetCommunityBuddy(ctx, req); err == nil {
		t.Fatal("unconfirmed remove")
	}
	req.Confirm = true
	if _, err := s.SetCommunityBuddy(ctx, req); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	_, watched := s.community.users["Alice"]
	_, buddy := s.community.buddies["Alice"]
	s.mu.Unlock()
	failIf(t, !watched || buddy, "removal dropped another frontend watch or retained buddy")
	must(t, s.WatchCommunityUsers(id, "second frontend", nil))
	s.mu.Lock()
	_, watched = s.community.users["Alice"]
	s.mu.Unlock()
	failIf(t, watched, "last consumer did not release watch")
	recreated, err := s.SetCommunityBuddy(ctx, CommunityBuddyRequest{CommunityIdentity: id, Username: "Alice"})
	must(t, err)
	failIf(t, recreated.Buddy.Revision == first.Buddy.Revision, "recreated revision reused")
	if _, err := s.SetCommunityBuddy(ctx, req); !errors.Is(err, ErrCommunityMessageState) {
		t.Fatal("stale delete removed recreated buddy", err)
	}
	for _, note := range []string{strings.Repeat("x", MaxCommunityBuddyNoteBytes+1), "\x1b[2J", "\u202e", string([]byte{255})} {
		if _, err := s.SetCommunityBuddy(ctx, CommunityBuddyRequest{CommunityIdentity: id, Username: "invalid", Note: note}); err == nil {
			t.Fatal("invalid note accepted")
		}
	}
	s.mu.Lock()
	s.cfg.Soulseek.Username = "another account"
	err = s.loadCommunityLocked(ctx)
	next := s.community.identity
	s.mu.Unlock()
	must(t, err)
	if _, err := s.SetCommunityBuddy(ctx, CommunityBuddyRequest{CommunityIdentity: id, Username: "late"}); !errors.Is(err, ErrCommunitySession) {
		t.Fatal("stale account mutation", err)
	}
	page, err := s.CommunityBuddies(ctx, CommunityBuddiesRequest{CommunityIdentity: next})
	failIf(t, err != nil || len(page.Buddies) != 0, "account leak", err, page)
}

func TestCommunityBuddiesPagingSortingAndRollback(t *testing.T) {
	s := downloadService(t)
	ctx := context.Background()
	id := s.community.identity
	for i := 0; i < 207; i++ {
		_, err := s.SetCommunityBuddy(ctx, CommunityBuddyRequest{CommunityIdentity: id, Username: fmt.Sprintf("user%03d", i), Note: fmt.Sprintf("note %03d", 206-i), Priority: i%2 == 0, Trusted: i%3 == 0, NotifyOnline: i%5 == 0})
		must(t, err)
	}
	for _, order := range []string{"username", "status", "country", "last_seen", "note", "priority", "trusted", "notify"} {
		names := map[string]bool{}
		cursor := ""
		for n := 0; ; n++ {
			failIf(t, n > 40, "unbounded paging")
			page, err := s.CommunityBuddies(ctx, CommunityBuddiesRequest{CommunityIdentity: id, Sort: order, Limit: 11, Cursor: cursor})
			must(t, err)
			failIf(t, page.Total != 207 || len(page.Buddies) > 11, "wrong page bound/count", page)
			for _, b := range page.Buddies {
				failIf(t, names[b.Username], "duplicate paging", order, b.Username)
				names[b.Username] = true
			}
			cursor = page.NextCursor
			if cursor == "" {
				break
			}
		}
		failIf(t, len(names) != 207, "paging omitted buddies", order, len(names))
	}
	page, err := s.CommunityBuddies(ctx, CommunityBuddiesRequest{CommunityIdentity: id, Sort: "note", Limit: 999})
	failIf(t, err != nil || len(page.Buddies) != 200 || page.Buddies[0].Username != "user206", "global sorting/page clamp", err)
	if _, err := s.CommunityBuddies(ctx, CommunityBuddiesRequest{CommunityIdentity: id, Sort: "priority", Cursor: page.NextCursor}); err == nil {
		t.Fatal("cursor reused with different sort")
	}
	page, err = s.CommunityBuddies(ctx, CommunityBuddiesRequest{CommunityIdentity: id, Query: "NOTE 002"})
	failIf(t, err != nil || len(page.Buddies) != 1 || page.Buddies[0].Username != "user204", "note search", err, page)
	before := page.Buddies[0]
	if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE TRIGGER buddy_fail BEFORE UPDATE ON community_buddies BEGIN SELECT RAISE(ABORT, 'synthetic write failure'); END;")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err = s.SetCommunityBuddy(ctx, CommunityBuddyRequest{CommunityIdentity: id, Username: before.Username, Note: "must not publish", Revision: &before.Revision})
	failIf(t, err == nil, "synthetic persistence failure ignored")
	after, err := s.CommunityBuddies(ctx, CommunityBuddiesRequest{CommunityIdentity: id, Username: before.Username})
	failIf(t, err != nil || after.Buddies[0].Note != before.Note || after.Buddies[0].Revision != before.Revision, "failed transaction published", err, after)
}

func TestCommunityBuddiesNotificationAccountRoundtrip(t *testing.T) {
	s := downloadService(t)
	ctx := context.Background()
	_, err := s.SetCommunityBuddy(ctx, CommunityBuddyRequest{CommunityIdentity: s.community.identity, Username: "Alice", NotifyOnline: true})
	must(t, err)
	delivered := make(chan struct{}, 2)
	release := make(chan struct{})
	defer close(release)
	s.desktopNotify = func(ctx context.Context, _, _ string) error {
		delivered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}
	s.mu.Lock()
	s.notifyBuddyOnlineLocked("Alice")
	first := s.community.buddyNotification
	s.mu.Unlock()
	select {
	case <-delivered:
	case <-time.After(3 * time.Second):
		t.Fatal("first notification not delivered")
	}
	s.mu.Lock()
	original := s.cfg.Soulseek.Username
	s.cfg.Soulseek.Username = "other"
	err = s.loadCommunityLocked(ctx)
	if err == nil {
		s.cfg.Soulseek.Username = original
		err = s.loadCommunityLocked(ctx)
	}
	if err == nil {
		s.notifyBuddyOnlineLocked("Alice")
	}
	second := s.community.buddyNotification
	s.mu.Unlock()
	must(t, err)
	failIf(t, first.Sequence != second.Sequence || first.SessionID == second.SessionID, "account roundtrip reused notification identity")
	// Leave the coalescing worker active during the account roundtrip. Each call
	// receives one release, so the second delivery proves it was not deduplicated.
	release <- struct{}{}
	select {
	case <-delivered:
	case <-time.After(4 * time.Second):
		t.Fatal("new account-generation notification suppressed")
	}
}

func TestCommunityBuddiesNotificationsHydrationAndRestart(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	s, err := New(cfg, path)
	must(t, err)
	t.Cleanup(func() { _ = s.Close() })
	var desktop atomic.Int32
	s.desktopNotify = func(context.Context, string, string) error { desktop.Add(1); return nil }
	_, _, id := communityTestConnection(t, s)
	added, err := s.SetCommunityBuddy(ctx, CommunityBuddyRequest{CommunityIdentity: id, Username: "Alice", Note: "retained", NotifyOnline: true, Priority: true, Trusted: true})
	must(t, err)
	apply := func(event soulseek.SocialMessage) {
		t.Helper()
		must(t, s.communityUpdate(ctx, id, event))
	}
	apply(soulseek.WatchUserResponse{Username: "Alice", Exists: true, Status: soulseek.UserStatusOnline})
	summary, err := s.CommunitySummary(ctx)
	failIf(t, err != nil || summary.BuddyNotification.Sequence != 0 || desktop.Load() != 0, "hydration notified", err, summary)
	apply(soulseek.UserPresence{Username: "Alice", Status: soulseek.UserStatusOffline})
	apply(soulseek.UserPresence{Username: "Alice", Status: soulseek.UserStatusOnline})
	apply(soulseek.UserPresence{Username: "Alice", Status: soulseek.UserStatusOnline})
	apply(soulseek.UserPresence{Username: "Alice", Status: soulseek.UserStatusAway})
	summary, err = s.CommunitySummary(ctx)
	failIf(t, err != nil || summary.BuddyNotification.Sequence != 1, "duplicate/away transition notified", err, summary)
	page, err := s.CommunityBuddies(ctx, CommunityBuddiesRequest{CommunityIdentity: id, Username: "Alice"})
	failIf(t, err != nil || page.Buddies[0].LastSeen.IsZero() || !page.Buddies[0].StatusFresh, "last-seen/presence", err, page)
	seen := page.Buddies[0].LastSeen
	failIf(t, page.Buddies[0].Revision != added.Buddy.Revision, "live presence invalidated metadata editor")
	s.mu.Lock()
	s.retireCommunityLocked()
	s.community.online = true
	s.mu.Unlock()
	apply(soulseek.WatchUserResponse{Username: "Alice", Exists: true, Status: soulseek.UserStatusOnline})
	summary, err = s.CommunitySummary(ctx)
	failIf(t, err != nil || summary.BuddyNotification.Sequence != 1, "reconnect hydration notified", err, summary)
	row, err := s.stateDB.Queries().GetCommunityBuddy(ctx, db.GetCommunityBuddyParams{Account: id.Account, Username: "Alice"})
	failIf(t, err != nil || row.LastSeen == nil || *row.LastSeen != seen.UnixMilli(), "local disconnect invented last-seen", err, row)
	must(t, s.Close())
	next, err := New(cfg, path)
	must(t, err)
	defer next.Close()
	id = next.community.identity
	page, err = next.CommunityBuddies(ctx, CommunityBuddiesRequest{CommunityIdentity: id})
	failIf(t, err != nil || len(page.Buddies) != 1, "restart lost buddy", err, page)
	b := page.Buddies[0]
	failIf(t, b.Note != "retained" || !b.NotifyOnline || !b.Priority || !b.Trusted || !b.LastSeen.Equal(time.UnixMilli(seen.UnixMilli()).UTC()) || b.StatusFresh, "restart lost flags/history or invented live status", b)
	failIf(t, !slices.Contains(next.community.watches["buddies"].users, "Alice"), "restart lost daemon watch ownership")
}
