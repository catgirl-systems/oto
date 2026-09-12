package ipc

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestCommunityBuddiesIPCFrontendsAndValidation(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "local", "local-only"
	first, service := communityIPC(t, cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
	second := NewClient(first.path)
	ctx := context.Background()
	summary, err := first.CommunitySummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(summary.Capabilities, "buddies") {
		t.Fatal("missing implemented buddy capability")
	}
	id := summary.CommunityIdentity
	req := daemon.CommunityBuddyRequest{CommunityIdentity: id, Username: "Alice", Note: "猫/?\nprivate note", NotifyOnline: true, Trusted: true, Priority: true}
	added, err := first.SetCommunityBuddy(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	page, err := second.CommunityBuddies(ctx, daemon.CommunityBuddiesRequest{CommunityIdentity: id, Username: "Alice"})
	if err != nil || len(page.Buddies) != 1 || page.Buddies[0].Note != req.Note || !page.Buddies[0].NotifyOnline || !page.Buddies[0].Trusted || !page.Buddies[0].Priority {
		t.Fatal("second frontend lost exact metadata", err, page)
	}
	req.Revision = &added.Buddy.Revision
	req.Note = "changed by second frontend"
	changed, err := second.SetCommunityBuddy(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.Note = "stale first frontend"
	if _, err := first.SetCommunityBuddy(ctx, req); err == nil {
		t.Fatal("stale frontend overwrote note")
	}
	if _, err := first.SetCommunityBuddy(ctx, daemon.CommunityBuddyRequest{CommunityIdentity: id, Username: "alice", Note: strings.Repeat("猫", 100)}); err != nil {
		t.Fatal(err)
	}
	page, err = first.CommunityBuddies(ctx, daemon.CommunityBuddiesRequest{CommunityIdentity: id, Limit: 1, Query: "ALICE"})
	if err != nil || len(page.Buddies) != 1 || page.NextCursor == "" || page.Total != 2 {
		t.Fatal("bounded case-folded filter", err, page)
	}
	next, err := first.CommunityBuddies(ctx, daemon.CommunityBuddiesRequest{CommunityIdentity: id, Limit: 1, Query: "ALICE", Cursor: page.NextCursor})
	if err != nil || len(next.Buddies) != 1 || next.Buddies[0].Username == page.Buddies[0].Username {
		t.Fatal("cursor/exact identity", err, next)
	}
	encoded, err := json.Marshal(service.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "changed by second frontend") || strings.Contains(string(encoded), "private note") {
		t.Fatal("state snapshot leaked buddy records")
	}
	base := "/v1/community/buddies?" + communityRoomValues(id).Encode()
	for _, path := range []string{base + "&limit=-1", base + "&limit=no", base + "&sort=invalid", base + "&cursor=invalid", "/v1/community/buddies?session=invalid"} {
		resp, err := first.http.Do(mustRequest(http.MethodGet, "http://oto.local"+path, nil))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatal("invalid page status", path, resp.StatusCode)
		}
	}
	req.Note = ""
	req.Revision = &changed.Buddy.Revision
	req.Remove = true
	if _, err := first.SetCommunityBuddy(ctx, req); err == nil {
		t.Fatal("DELETE lacks confirmation")
	}
	req.Confirm = true
	removed, err := first.SetCommunityBuddy(ctx, req)
	if err != nil || !removed.Removed {
		t.Fatal("confirmed removal", err, removed)
	}
	page, err = second.CommunityBuddies(ctx, daemon.CommunityBuddiesRequest{CommunityIdentity: id, Username: "alice"})
	if err != nil || len(page.Buddies) != 1 {
		t.Fatal("case variant removed", err, page)
	}
	req.CommunityIdentity.Session++
	req.Username = "late"
	req.Remove = false
	req.Revision = nil
	if _, err := first.SetCommunityBuddy(ctx, req); err == nil {
		t.Fatal("old session accepted")
	}
}

func TestCommunityBuddiesEscapedCursorIPC(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "local", "local-only"
	client, _ := communityIPC(t, cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
	ctx := context.Background()
	summary, err := client.CommunitySummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prefix := strings.Repeat("&", 1023)
	for _, suffix := range []string{"a", "b"} {
		if _, err := client.SetCommunityBuddy(ctx, daemon.CommunityBuddyRequest{CommunityIdentity: summary.CommunityIdentity, Username: prefix + suffix, Note: strings.Repeat("<", daemon.MaxCommunityBuddyNoteBytes)}); err != nil {
			t.Fatal(err)
		}
	}
	req := daemon.CommunityBuddiesRequest{CommunityIdentity: summary.CommunityIdentity, Sort: "note", Query: prefix, Limit: 1}
	first, err := client.CommunityBuddies(ctx, req)
	if err != nil || len(first.Buddies) != 1 || len(first.NextCursor) <= 32768 {
		t.Fatal("escaped cursor setup", err, len(first.NextCursor))
	}
	req.Cursor = first.NextCursor
	second, err := client.CommunityBuddies(ctx, req)
	if err != nil || len(second.Buddies) != 1 || second.Buddies[0].Username != prefix+"b" || second.NextCursor != "" {
		t.Fatal("cannot consume generated maximum cursor", err)
	}
}
