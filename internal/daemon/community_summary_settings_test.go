package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
)

func TestCommunitySummaryReportsSettingsState(t *testing.T) {
	s := downloadService(t)
	s.configPath = filepath.Join(t.TempDir(), "config.json")
	ctx := context.Background()
	id := s.community.identity

	page, err := s.SetCommunityRule(ctx, CommunityRuleRequest{CommunityIdentity: id, Confirm: true, Rule: CommunityRule{Action: "ban", Kind: "username", Value: "Alice"}})
	must(t, err)
	failIf(t, page.Revision == 0, page)
	_, err = s.SetCommunityAlias(ctx, CommunityAliasRequest{CommunityIdentity: id, Name: "wave", Expansion: "me waves"})
	must(t, err)

	p, err := compileCommunityTextTools(CommunityTextTools{Keywords: []string{"a"}, Substitutions: []CommunitySubstitution{{From: "x", To: "y"}, {From: "y", To: "z"}}, Censorship: []string{"bad"}, CTCPVersion: true})
	must(t, err)
	s.mu.Lock()
	s.community.text = p
	s.mu.Unlock()

	_, err = s.SetCommunityAwaySettings(ctx, CommunityAwaySettingsRequest{CommunityIdentity: id, Expected: config.CommunityAway{}, Settings: config.CommunityAway{AutoAwaySeconds: 60, AutoReply: "later"}})
	must(t, err)

	summary, err := s.CommunitySummary(ctx)
	must(t, err)
	want := CommunitySettingsSummary{PrivacyRules: 1, Aliases: 1, Keywords: 1, Substitutions: 2, Censorship: 1, CTCPVersion: true, AwaySeconds: 60, AwayReply: true}
	failIf(t, summary.Settings != want, summary.Settings, want)
}
