package daemon

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

func TestCommunityInterestsPersistenceVersionsAndAccountIsolation(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	s, err := New(cfg, path)
	must(t, err)
	t.Cleanup(func() { _ = s.Close() })
	id := s.community.identity
	first, err := s.SetCommunityInterest(ctx, CommunityInterestRequest{CommunityIdentity: id, Item: "  TeChNo  ", Opinion: "like"})
	failIf(t, err != nil || first.Interest.Item != "techno" || first.Interest.State != "offline", first, err)
	again, err := s.SetCommunityInterest(ctx, CommunityInterestRequest{CommunityIdentity: id, Item: "TECHNO", Opinion: "like"})
	failIf(t, err != nil || again.Interest.Revision != first.Interest.Revision, "replayed create", again, err)
	changed, err := s.SetCommunityInterest(ctx, CommunityInterestRequest{CommunityIdentity: id, Item: "techno", Opinion: "dislike", Revision: &first.Interest.Revision})
	must(t, err)
	if _, err := s.SetCommunityInterest(ctx, CommunityInterestRequest{CommunityIdentity: id, Item: "techno", Opinion: "like", Revision: &first.Interest.Revision}); !errors.Is(err, ErrCommunityMessageState) {
		t.Fatal("stale overwrite", err)
	}
	profile, err := s.CommunitySelfProfile(ctx, id)
	must(t, err)
	profile.Description = "description 猫/?\r\nsecond line"
	saved, err := s.SetCommunitySelfProfile(ctx, profile)
	failIf(t, err != nil || saved.Description != "description 猫/?\nsecond line", saved, err)
	profile.Description = "stale"
	if _, err := s.SetCommunitySelfProfile(ctx, profile); !errors.Is(err, ErrCommunityMessageState) {
		t.Fatal("stale description", err)
	}
	for _, invalid := range []string{"\x1b[2J", "\u202e", string([]byte{255}), strings.Repeat("x", soulseek.MaxProfileDescriptionBytes+1)} {
		p := saved
		p.Description = invalid
		if _, err := s.SetCommunitySelfProfile(ctx, p); err == nil {
			t.Fatal("accepted invalid description")
		}
	}
	if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE TRIGGER reject_interests BEFORE INSERT ON community_interests BEGIN SELECT RAISE(ABORT, 'test failure'); END")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetCommunityInterest(ctx, CommunityInterestRequest{CommunityIdentity: id, Item: "rollback", Opinion: "like"}); err == nil {
		t.Fatal("expected storage failure")
	}
	if _, ok := s.community.discovery.interests["rollback"]; ok {
		t.Fatal("published rolled back interest")
	}
	// Unrelated account preferences survive description changes.
	row, err := s.stateDB.Queries().GetCommunityAccount(ctx, id.Account)
	must(t, err)
	if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := db.New(tx).EditCommunityAccount(ctx, db.EditCommunityAccountParams{Account: id.Account, Revision: row.Revision, Description: row.Description, AcceptInvitations: 0, RetentionDays: 30, PublicFeedLogging: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	saved.Description = "final description"
	saved, err = s.SetCommunitySelfProfile(ctx, saved)
	must(t, err)
	row, err = s.stateDB.Queries().GetCommunityAccount(ctx, id.Account)
	failIf(t, err != nil || row.AcceptInvitations != 0 || row.RetentionDays != 30 || row.PublicFeedLogging != 1, "overwrote unrelated preferences", row, err)
	must(t, s.Close())
	s, err = New(cfg, path)
	must(t, err)
	id = s.community.identity
	page, err := s.CommunityInterests(ctx, CommunityInterestsRequest{CommunityIdentity: id})
	failIf(t, err != nil || len(page.Interests) != 1 || page.Interests[0].Opinion != changed.Interest.Opinion || page.Interests[0].State != "offline", "restart", page, err)
	restored, err := s.CommunitySelfProfile(ctx, id)
	failIf(t, err != nil || restored.Description != saved.Description, "description restart", restored, err)
	s.mu.Lock()
	s.cfg.Soulseek.Username = "other"
	err = s.loadCommunityLocked(ctx)
	other := s.community.identity
	s.mu.Unlock()
	must(t, err)
	if _, err := s.CommunitySelfProfile(ctx, id); !errors.Is(err, ErrCommunitySession) {
		t.Fatal("old account accepted", err)
	}
	empty, err := s.CommunityInterests(ctx, CommunityInterestsRequest{CommunityIdentity: other})
	failIf(t, err != nil || empty.Total != 0, "interest account leak", empty, err)
	p, err := s.CommunitySelfProfile(ctx, other)
	failIf(t, err != nil || p.Description != "", "description account leak", p, err)
}

func TestCommunityInterestsWireSyncPagingAndRemoval(t *testing.T) {
	s := downloadService(t)
	client, peer, id := communityTestConnection(t, s)
	ctx := context.Background()
	sync := func(names ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- s.syncCommunityInterests(ctx, client, id) }()
		for _, name := range names {
			f := roomFixture(t, name)
			code, p, err := soulseek.ReadFrame(peer)
			failIf(t, err != nil || code != f.Code || !bytes.Equal(p, f.Payload(t)), name, code, p, err)
		}
		must(t, <-done)
	}
	first, err := s.SetCommunityInterest(ctx, CommunityInterestRequest{CommunityIdentity: id, Item: "techno", Opinion: "like"})
	must(t, err)
	failIf(t, first.Interest.State != "pending", first)
	sync("interest-add-like")
	sync()
	changed, err := s.SetCommunityInterest(ctx, CommunityInterestRequest{CommunityIdentity: id, Item: "techno", Opinion: "dislike", Revision: &first.Interest.Revision})
	must(t, err)
	sync("interest-remove-like", "interest-add-dislike")
	page, err := s.CommunityInterests(ctx, CommunityInterestsRequest{CommunityIdentity: id, Query: "TECH"})
	failIf(t, err != nil || page.Total != 1 || page.Interests[0].State != "sent", page, err)
	req := CommunityInterestRequest{CommunityIdentity: id, Item: "techno", Remove: true, Revision: &changed.Interest.Revision}
	if _, err := s.SetCommunityInterest(ctx, req); err == nil {
		t.Fatal("unconfirmed delete")
	}
	req.Confirm = true
	if _, err := s.SetCommunityInterest(ctx, req); err != nil {
		t.Fatal(err)
	}
	sync("interest-remove-dislike")
	sync()
	for _, item := range []string{"techno", "ambient", "猫"} {
		_, err := s.SetCommunityInterest(ctx, CommunityInterestRequest{CommunityIdentity: id, Item: item, Opinion: "like"})
		must(t, err)
	}
	var items []string
	for cursor := ""; ; {
		page, err := s.CommunityInterests(ctx, CommunityInterestsRequest{CommunityIdentity: id, Cursor: cursor, Limit: 1})
		failIf(t, err != nil || len(page.Interests) != 1 || page.Total != 3, page, err)
		items = append(items, page.Interests[0].Item)
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	failIf(t, strings.Join(items, ",") != "ambient,techno,猫", items)
	if _, err := s.SetCommunityInterest(ctx, req); !errors.Is(err, ErrCommunityMessageState) {
		t.Fatal("stale deletion removed recreated interest", err)
	}
	stale := id
	stale.Session++
	if err := s.syncCommunityInterests(ctx, client, stale); !errors.Is(err, ErrCommunitySession) {
		t.Fatal("old session sync", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.syncCommunityInterests(cancelled, client, id); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled write", err)
	}
}
