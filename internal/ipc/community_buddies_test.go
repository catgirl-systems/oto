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
	must(t, err)
	failIf(t, !slices.Contains(summary.Capabilities, "buddies"), "missing implemented buddy capability")
	id := summary.CommunityIdentity
	req := daemon.CommunityBuddyRequest{CommunityIdentity: id, Username: "Alice", Note: "猫/?\nprivate note", NotifyOnline: true, Trusted: true, Priority: true}
	added, err := first.SetCommunityBuddy(ctx, req)
	must(t, err)
	page, err := second.CommunityBuddies(ctx, daemon.CommunityBuddiesRequest{CommunityIdentity: id, Username: "Alice"})
	failIf(t, err != nil || len(page.Buddies) != 1 || page.Buddies[0].Note != req.Note || !page.Buddies[0].NotifyOnline || !page.Buddies[0].Trusted || !page.Buddies[0].Priority, "second frontend lost exact metadata", err, page)
	req.Revision = &added.Buddy.Revision
	req.Note = "changed by second frontend"
	changed, err := second.SetCommunityBuddy(ctx, req)
	must(t, err)
	req.Note = "stale first frontend"
	if _, err := first.SetCommunityBuddy(ctx, req); err == nil {
		t.Fatal("stale frontend overwrote note")
	}
	if _, err := first.SetCommunityBuddy(ctx, daemon.CommunityBuddyRequest{CommunityIdentity: id, Username: "alice", Note: strings.Repeat("猫", 100)}); err != nil {
		t.Fatal(err)
	}
	page, err = first.CommunityBuddies(ctx, daemon.CommunityBuddiesRequest{CommunityIdentity: id, Limit: 1, Query: "ALICE"})
	failIf(t, err != nil || len(page.Buddies) != 1 || page.NextCursor == "" || page.Total != 2, "bounded case-folded filter", err, page)
	next, err := first.CommunityBuddies(ctx, daemon.CommunityBuddiesRequest{CommunityIdentity: id, Limit: 1, Query: "ALICE", Cursor: page.NextCursor})
	failIf(t, err != nil || len(next.Buddies) != 1 || next.Buddies[0].Username == page.Buddies[0].Username, "cursor/exact identity", err, next)
	encoded, err := json.Marshal(service.Snapshot())
	must(t, err)
	failIf(t, strings.Contains(string(encoded), "changed by second frontend") || strings.Contains(string(encoded), "private note"), "state snapshot leaked buddy records")
	base := "/v1/community/buddies?" + communityRoomValues(id).Encode()
	for _, tc := range []struct {
		path   string
		status int
	}{{base + "&limit=-1", 400}, {base + "&limit=no", 422}, {base + "&sort=invalid", 400}, {base + "&cursor=invalid", 400}, {"/v1/community/buddies?session=invalid", 400}} {
		resp, err := first.http.Do(mustRequest(http.MethodGet, "http://oto.local"+tc.path, nil))
		must(t, err)
		_ = resp.Body.Close()
		failIf(t, resp.StatusCode != tc.status, "invalid page status", tc.path, resp.StatusCode)
	}
	req.Note = ""
	req.Revision = &changed.Buddy.Revision
	req.Remove = true
	if _, err := first.SetCommunityBuddy(ctx, req); err == nil {
		t.Fatal("DELETE lacks confirmation")
	}
	req.Confirm = true
	removed, err := first.SetCommunityBuddy(ctx, req)
	failIf(t, err != nil || !removed.Removed, "confirmed removal", err, removed)
	page, err = second.CommunityBuddies(ctx, daemon.CommunityBuddiesRequest{CommunityIdentity: id, Username: "alice"})
	failIf(t, err != nil || len(page.Buddies) != 1, "case variant removed", err, page)
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
	must(t, err)
	prefix := strings.Repeat("&", 1023)
	for _, suffix := range []string{"a", "b"} {
		_, err := client.SetCommunityBuddy(ctx, daemon.CommunityBuddyRequest{CommunityIdentity: summary.CommunityIdentity, Username: prefix + suffix, Note: strings.Repeat("<", daemon.MaxCommunityBuddyNoteBytes)})
		must(t, err)
	}
	req := daemon.CommunityBuddiesRequest{CommunityIdentity: summary.CommunityIdentity, Sort: "note", Query: prefix, Limit: 1}
	first, err := client.CommunityBuddies(ctx, req)
	failIf(t, err != nil || len(first.Buddies) != 1 || len(first.NextCursor) <= 32768, "escaped cursor setup", err, len(first.NextCursor))
	req.Cursor = first.NextCursor
	second, err := client.CommunityBuddies(ctx, req)
	failIf(t, err != nil || len(second.Buddies) != 1 || second.Buddies[0].Username != prefix+"b" || second.NextCursor != "", "cannot consume generated maximum cursor", err)
}
