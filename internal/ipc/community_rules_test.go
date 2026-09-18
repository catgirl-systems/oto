package ipc

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestCommunityRulesIPC(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "local", "local-only"
	first, _ := communityIPC(t, cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
	second := NewClient(first.path)
	ctx := context.Background()
	summary, err := first.CommunitySummary(ctx)
	must(t, err)
	id := summary.CommunityIdentity
	page, err := first.CommunityRules(ctx, daemon.CommunityRulesRequest{CommunityIdentity: id})
	must(t, err)
	req := daemon.CommunityRuleRequest{CommunityIdentity: id, Revision: page.Revision, Rule: daemon.CommunityRule{Action: "ban", Kind: "username", Value: "Case", Message: "Unavailable"}}
	if _, err := first.SetCommunityRule(ctx, req); err == nil {
		t.Fatal("missing confirmation accepted")
	}
	req.Confirm = true
	if _, err := first.SetCommunityRule(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := second.SetCommunityRule(ctx, req); err == nil {
		t.Fatal("stale revision accepted")
	}
	page, err = second.CommunityRules(ctx, daemon.CommunityRulesRequest{CommunityIdentity: id, Limit: 1})
	failIf(t, err != nil || len(page.Rules) != 1 || page.Rules[0].Value != "Case", page, err)
	req.Rule, req.Revision, req.Remove = page.Rules[0], page.Revision, true
	if err := first.Do(ctx, http.MethodPut, "/v1/community/rules", req, nil); err == nil {
		t.Fatal("PUT removed a rule")
	}
	if _, err := second.SetCommunityRule(ctx, req); err != nil {
		t.Fatal(err)
	}
	page, err = first.CommunityRules(ctx, daemon.CommunityRulesRequest{CommunityIdentity: id})
	failIf(t, err != nil || len(page.Rules) != 0, page, err)
	for _, cursor := range []string{"-1", "x", "9223372036854775808"} {
		if _, err := first.CommunityRules(ctx, daemon.CommunityRulesRequest{CommunityIdentity: id, Cursor: cursor}); err == nil {
			t.Fatal("invalid cursor", cursor)
		}
	}
	if page, err := first.CommunityRules(ctx, daemon.CommunityRulesRequest{CommunityIdentity: id, Limit: 201}); err != nil || len(page.Rules) > 200 {
		t.Fatal("unbounded page")
	}
	id.Session++
	if _, err := first.CommunityRules(ctx, daemon.CommunityRulesRequest{CommunityIdentity: id}); err == nil {
		t.Fatal("stale session")
	}
}
