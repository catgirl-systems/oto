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
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.SetCommunityBuddy(ctx, req)
	if err != nil || replay.Buddy.Revision != first.Buddy.Revision {
		t.Fatal("idempotent put", err, replay)
	}
	if _, err := s.SetCommunityBuddy(ctx, CommunityBuddyRequest{CommunityIdentity: id, Username: "alice"}); err != nil {
		t.Fatal(err)
	}
	req.Revision = &first.Buddy.Revision
	req.Note = "edited"
	second, err := s.SetCommunityBuddy(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.Note = "stale frontend"
	if _, err := s.SetCommunityBuddy(ctx, req); !errors.Is(err, ErrCommunityMessageState) {
		t.Fatal("stale note overwrote buddy", err)
	}
	if err := s.WatchCommunityUsers(id, "second frontend", []string{"Alice"}); err != nil {
		t.Fatal(err)
	}
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
	if !watched || buddy {
		t.Fatal("removal dropped another frontend watch or retained buddy")
	}
	if err := s.WatchCommunityUsers(id, "second frontend", nil); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	_, watched = s.community.users["Alice"]
	s.mu.Unlock()
	if watched {
		t.Fatal("last consumer did not release watch")
	}
	recreated, err := s.SetCommunityBuddy(ctx, CommunityBuddyRequest{CommunityIdentity: id, Username: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	if recreated.Buddy.Revision == first.Buddy.Revision {
		t.Fatal("recreated revision reused")
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetCommunityBuddy(ctx, CommunityBuddyRequest{CommunityIdentity: id, Username: "late"}); !errors.Is(err, ErrCommunitySession) {
		t.Fatal("stale account mutation", err)
	}
	page, err := s.CommunityBuddies(ctx, CommunityBuddiesRequest{CommunityIdentity: next})
	if err != nil || len(page.Buddies) != 0 {
		t.Fatal("account leak", err, page)
	}
}

func TestCommunityBuddiesPagingSortingAndRollback(t *testing.T) {
	s := downloadService(t)
	ctx := context.Background()
	id := s.community.identity
	for i := 0; i < 207; i++ {
		_, err := s.SetCommunityBuddy(ctx, CommunityBuddyRequest{CommunityIdentity: id, Username: fmt.Sprintf("user%03d", i), Note: fmt.Sprintf("note %03d", 206-i), Priority: i%2 == 0, Trusted: i%3 == 0, NotifyOnline: i%5 == 0})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, order := range []string{"username", "status", "country", "last_seen", "note", "priority", "trusted", "notify"} {
		names := map[string]bool{}
		cursor := ""
		for n := 0; ; n++ {
			if n > 40 {
				t.Fatal("unbounded paging")
			}
			page, err := s.CommunityBuddies(ctx, CommunityBuddiesRequest{CommunityIdentity: id, Sort: order, Limit: 11, Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			if page.Total != 207 || len(page.Buddies) > 11 {
				t.Fatal("wrong page bound/count", page)
			}
			for _, b := range page.Buddies {
				if names[b.Username] {
					t.Fatal("duplicate paging", order, b.Username)
				}
				names[b.Username] = true
			}
			cursor = page.NextCursor
			if cursor == "" {
				break
			}
		}
		if len(names) != 207 {
			t.Fatal("paging omitted buddies", order, len(names))
		}
	}
	page, err := s.CommunityBuddies(ctx, CommunityBuddiesRequest{CommunityIdentity: id, Sort: "note", Limit: 999})
	if err != nil || len(page.Buddies) != 200 || page.Buddies[0].Username != "user206" {
		t.Fatal("global sorting/page clamp", err)
	}
	if _, err := s.CommunityBuddies(ctx, CommunityBuddiesRequest{CommunityIdentity: id, Sort: "priority", Cursor: page.NextCursor}); err == nil {
		t.Fatal("cursor reused with different sort")
	}
	page, err = s.CommunityBuddies(ctx, CommunityBuddiesRequest{CommunityIdentity: id, Query: "NOTE 002"})
	if err != nil || len(page.Buddies) != 1 || page.Buddies[0].Username != "user204" {
		t.Fatal("note search", err, page)
	}
	before := page.Buddies[0]
	if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE TRIGGER buddy_fail BEFORE UPDATE ON community_buddies BEGIN SELECT RAISE(ABORT, 'synthetic write failure'); END;")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err = s.SetCommunityBuddy(ctx, CommunityBuddyRequest{CommunityIdentity: id, Username: before.Username, Note: "must not publish", Revision: &before.Revision})
	if err == nil {
		t.Fatal("synthetic persistence failure ignored")
	}
	after, err := s.CommunityBuddies(ctx, CommunityBuddiesRequest{CommunityIdentity: id, Username: before.Username})
	if err != nil || after.Buddies[0].Note != before.Note || after.Buddies[0].Revision != before.Revision {
		t.Fatal("failed transaction published", err, after)
	}
}

func TestCommunityBuddiesNotificationAccountRoundtrip(t *testing.T) {
	s := downloadService(t)
	ctx := context.Background()
	_, err := s.SetCommunityBuddy(ctx, CommunityBuddyRequest{CommunityIdentity: s.community.identity, Username: "Alice", NotifyOnline: true})
	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != second.Sequence || first.SessionID == second.SessionID {
		t.Fatal("account roundtrip reused notification identity")
	}
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
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var desktop atomic.Int32
	s.desktopNotify = func(context.Context, string, string) error { desktop.Add(1); return nil }
	_, _, id := communityTestConnection(t, s)
	added, err := s.SetCommunityBuddy(ctx, CommunityBuddyRequest{CommunityIdentity: id, Username: "Alice", Note: "retained", NotifyOnline: true, Priority: true, Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	apply := func(event soulseek.SocialMessage) {
		t.Helper()
		if err := s.communityUpdate(ctx, id, event); err != nil {
			t.Fatal(err)
		}
	}
	apply(soulseek.WatchUserResponse{Username: "Alice", Exists: true, Status: soulseek.UserStatusOnline})
	summary, err := s.CommunitySummary(ctx)
	if err != nil || summary.BuddyNotification.Sequence != 0 || desktop.Load() != 0 {
		t.Fatal("hydration notified", err, summary)
	}
	apply(soulseek.UserPresence{Username: "Alice", Status: soulseek.UserStatusOffline})
	apply(soulseek.UserPresence{Username: "Alice", Status: soulseek.UserStatusOnline})
	apply(soulseek.UserPresence{Username: "Alice", Status: soulseek.UserStatusOnline})
	apply(soulseek.UserPresence{Username: "Alice", Status: soulseek.UserStatusAway})
	summary, err = s.CommunitySummary(ctx)
	if err != nil || summary.BuddyNotification.Sequence != 1 {
		t.Fatal("duplicate/away transition notified", err, summary)
	}
	page, err := s.CommunityBuddies(ctx, CommunityBuddiesRequest{CommunityIdentity: id, Username: "Alice"})
	if err != nil || page.Buddies[0].LastSeen.IsZero() || !page.Buddies[0].StatusFresh {
		t.Fatal("last-seen/presence", err, page)
	}
	seen := page.Buddies[0].LastSeen
	if page.Buddies[0].Revision != added.Buddy.Revision {
		t.Fatal("live presence invalidated metadata editor")
	}
	s.mu.Lock()
	s.retireCommunityLocked()
	s.community.online = true
	s.mu.Unlock()
	apply(soulseek.WatchUserResponse{Username: "Alice", Exists: true, Status: soulseek.UserStatusOnline})
	summary, err = s.CommunitySummary(ctx)
	if err != nil || summary.BuddyNotification.Sequence != 1 {
		t.Fatal("reconnect hydration notified", err, summary)
	}
	row, err := s.stateDB.Queries().GetCommunityBuddy(ctx, db.GetCommunityBuddyParams{Account: id.Account, Username: "Alice"})
	if err != nil || row.LastSeen == nil || *row.LastSeen != seen.UnixMilli() {
		t.Fatal("local disconnect invented last-seen", err, row)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := New(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	id = next.community.identity
	page, err = next.CommunityBuddies(ctx, CommunityBuddiesRequest{CommunityIdentity: id})
	if err != nil || len(page.Buddies) != 1 {
		t.Fatal("restart lost buddy", err, page)
	}
	b := page.Buddies[0]
	if b.Note != "retained" || !b.NotifyOnline || !b.Priority || !b.Trusted || !b.LastSeen.Equal(time.UnixMilli(seen.UnixMilli()).UTC()) || b.StatusFresh {
		t.Fatal("restart lost flags/history or invented live status", b)
	}
	if !slices.Contains(next.community.watches["buddies"].users, "Alice") {
		t.Fatal("restart lost daemon watch ownership")
	}
}
