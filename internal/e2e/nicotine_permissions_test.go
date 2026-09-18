//go:build communitye2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

// Reuse the running unmodified Nicotine network/userbrowse components. Each
// request explicitly refreshes rather than reading a saved browse snapshot.
func verifyNicotineSharePermissions(t *testing.T, h *terminal, ctx context.Context, state string) {
	t.Helper()
	for _, access := range []string{"public", "buddy", "trusted"} {
		root := t.TempDir()
		must(t, os.WriteFile(filepath.Join(root, "sample.txt"), []byte("local interoperability"), 0600))
		_, err := h.client.AddShare(ctx, config.Share{Name: access, Path: root, Access: access})
		must(t, err)
	}
	summary, err := h.client.CommunitySummary(ctx)
	must(t, err)
	id := summary.CommunityIdentity
	sequence := 0
	browse := func(public, locked []string) {
		t.Helper()
		sequence++
		request := filepath.Join(state, "browse-request.json")
		must(t, os.WriteFile(request+".tmp", []byte(fmt.Sprint(sequence)), 0600))
		must(t, os.Rename(request+".tmp", request))
		response := filepath.Join(state, fmt.Sprintf("shares-%d.json", sequence))
		h.wait("Nicotine tiered browse", func() bool { _, err := os.Stat(response); return err == nil })
		data, err := os.ReadFile(response)
		must(t, err)
		var got struct{ Public, Locked []string }
		must(t, json.Unmarshal(data, &got))
		failIfFmt(t, !slices.Equal(got.Public, public) || !slices.Equal(got.Locked, locked), "Nicotine browse %d: public=%v locked=%v; want %v/%v", sequence, got.Public, got.Locked, public, locked)
	}
	access := func(name, tier string, reveal bool) {
		t.Helper()
		snapshot, err := h.client.Status(ctx)
		must(t, err)
		for _, root := range snapshot.Shares {
			if root.Name != name {
				continue
			}
			_, err := h.client.SetShareAccess(ctx, daemon.ShareAccessRequest{CommunityIdentity: id, Expected: root, Revision: snapshot.ShareIndexRevision, Access: tier, Reveal: reveal, Confirm: true})
			must(t, err)
			return
		}
		t.Fatal("missing share root", name)
	}
	browse([]string{"public"}, nil)
	buddy, err := h.client.SetCommunityBuddy(ctx, daemon.CommunityBuddyRequest{CommunityIdentity: id, Username: "reference", Confirm: true})
	must(t, err)
	browse([]string{"buddy", "public"}, nil)
	buddy, err = h.client.SetCommunityBuddy(ctx, daemon.CommunityBuddyRequest{CommunityIdentity: id, Username: "reference", Revision: &buddy.Buddy.Revision, Trusted: true, Confirm: true})
	must(t, err)
	browse([]string{"buddy", "public", "trusted"}, nil)
	if _, err := h.client.SetCommunityBuddy(ctx, daemon.CommunityBuddyRequest{CommunityIdentity: id, Username: "reference", Revision: &buddy.Buddy.Revision, Remove: true, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	browse([]string{"public"}, nil)
	access("buddy", "buddy", true)
	access("trusted", "trusted", true)
	browse([]string{"public"}, []string{"buddy", "trusted"})
	for _, rule := range []daemon.CommunityRule{{Action: "ban", Kind: "username", Value: "reference"}, {Action: "ban", Kind: "ip", Value: "127.0.0.1"}} {
		page, err := h.client.CommunityRules(ctx, daemon.CommunityRulesRequest{CommunityIdentity: id})
		must(t, err)
		page, err = h.client.SetCommunityRule(ctx, daemon.CommunityRuleRequest{CommunityIdentity: id, Revision: page.Revision, Rule: rule, Confirm: true})
		must(t, err)
		browse(nil, nil)
		page, err = h.client.CommunityRules(ctx, daemon.CommunityRulesRequest{CommunityIdentity: id})
		must(t, err)
		failIf(t, len(page.Rules) != 1, "unexpected local rules")
		if _, err := h.client.SetCommunityRule(ctx, daemon.CommunityRuleRequest{CommunityIdentity: id, Revision: page.Revision, Rule: page.Rules[0], Remove: true, Confirm: true}); err != nil {
			t.Fatal(err)
		}
		browse([]string{"public"}, []string{"buddy", "trusted"})
	}
	access("public", "trusted", false)
	browse(nil, []string{"buddy", "trusted"})
	if _, err := h.client.SetCommunityBuddy(ctx, daemon.CommunityBuddyRequest{CommunityIdentity: id, Username: "reference", Trusted: true, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	browse([]string{"buddy", "public", "trusted"}, nil)
	h.attach("permissions", 120, 40)
	h.screen("permissions", "No matching results")
	h.command("send-keys", "-t", "permissions", "Tab", "Tab", "Tab", "Tab", "C-NPage", "C-NPage")
	h.screen("permissions", "reference")
	h.command("send-keys", "-t", "permissions", "U")
	h.screen("permissions", "User actions")
	h.command("send-keys", "-t", "permissions", "Down", "Down", "Down", "Down", "Down", "Enter")
	h.screen("permissions", "Privacy rule editor")
	h.command("send-keys", "-t", "permissions", "Right", "Enter", "Enter", "Enter")
	h.command("set-buffer", "--", "q/?猫")
	h.command("paste-buffer", "-p", "-t", "permissions")
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}, {120, 40}} {
		h.command("resize-window", "-t", "permissions", "-x", fmt.Sprint(size[0]), "-y", fmt.Sprint(size[1]))
		h.screen("permissions", "q/?猫")
	}
	rules := func() daemon.CommunityRulesPage {
		t.Helper()
		page, err := h.client.CommunityRules(ctx, daemon.CommunityRulesRequest{CommunityIdentity: id})
		must(t, err)
		return page
	}
	failIf(t, len(rules().Rules) != 0, "paste submitted a rule")
	h.command("send-keys", "-t", "permissions", "Enter")
	h.screen("permissions", "Confirm privacy change")
	h.command("send-keys", "-t", "permissions", "Enter")
	h.screen("permissions", "Privacy rule editor")
	failIf(t, len(rules().Rules) != 0, "default confirmation mutated privacy")
	h.command("send-keys", "-t", "permissions", "Enter", "Right", "Enter")
	h.screen("permissions", "Privacy rules · page")
	h.wait("terminal privacy save", func() bool { return len(rules().Rules) == 1 })
	failIf(t, rules().Rules[0].Message != "q/?猫", "paste changed rejection message")
	browse(nil, nil)
	h.command("send-keys", "-t", "permissions", "d", "Enter")
	h.screen("permissions", "Privacy rules · page")
	failIf(t, len(rules().Rules) != 1, "default delete confirmed")
	h.command("send-keys", "-t", "permissions", "d", "Right", "Enter")
	h.wait("terminal privacy removal", func() bool { return len(rules().Rules) == 0 })
	browse([]string{"buddy", "public", "trusted"}, nil)
	h.screen("permissions", "No privacy rules.")
	h.command("send-keys", "-t", "permissions", "Escape")
	h.screen("permissions", "[Buddies]")
	h.command("send-keys", "-t", "permissions", "Tab", "Tab")
	h.screen("permissions", "SHARES")
	h.command("send-keys", "-t", "permissions", "Home", "Enter")
	h.screen("permissions", "Root: \"buddy\"")
	h.command("send-keys", "-t", "permissions", "Right", "Tab", "Right", "Enter")
	h.screen("permissions", "Confirm share access")
	h.command("send-keys", "-t", "permissions", "Enter")
	h.screen("permissions", "Share access")
	h.command("send-keys", "-t", "permissions", "Enter", "Right", "Enter")
	h.screen("permissions", "SHARES")
	h.wait("terminal share access persisted", func() bool {
		snapshot, err := h.client.Status(ctx)
		if err != nil {
			return false
		}
		for _, root := range snapshot.Shares {
			if root.Name == "buddy" {
				return root.Access == "trusted" && !root.Reveal
			}
		}
		return false
	})
}
