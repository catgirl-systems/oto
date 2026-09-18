package daemon

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/country"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestCommunityRuleValidationAndMatching(t *testing.T) {
	for _, rule := range []CommunityRule{
		{Action: "ban", Kind: "ip", Value: "192.0.2.123/24"},
		{Action: "ignore", Kind: "ip", Value: "::ffff:192.0.2.1"},
		{Action: "ban", Kind: "country", Value: "us"},
		{Action: "ignore", Kind: "username", Value: "Case"},
	} {
		normal, err := NormalizeCommunityRule(rule)
		must(t, err)
		s := &Service{community: communityState{rules: []CommunityRule{normal}}}
		if rule.Kind == "ip" {
			if matched, pending := s.communityRuleLocked(rule.Action, "peer", netip.MustParseAddr("192.0.2.1")); matched == nil || pending {
				t.Fatal("IP match", normal)
			}
			if matched, pending := s.communityRuleLocked(rule.Action, "peer", netip.Addr{}); matched != nil || !pending {
				t.Fatal("unresolved IP")
			}
		}
		if rule.Kind == "username" {
			if matched, _ := s.communityRuleLocked("ignore", "case", netip.Addr{}); matched != nil {
				t.Fatal("case-folded identity")
			}
			if matched, _ := s.communityRuleLocked("ban", "Case", netip.Addr{}); matched != nil {
				t.Fatal("ignore became ban")
			}
		}
		if rule.Kind == "country" {
			if matched, pending := s.communityRuleLocked("ban", "peer", netip.MustParseAddr("2001:db8::1")); matched != nil || pending {
				t.Fatal("unknown country blocked")
			}
		}
	}
	for _, rule := range []CommunityRule{
		{Action: "ban", Kind: "ip", Value: "192.0.2.*"},
		{Action: "ban", Kind: "ip", Value: "fe80::1%eth0"},
		{Action: "ignore", Kind: "country", Value: "US"},
		{Action: "ban", Kind: "country", Value: "USA"},
		{Action: "ignore", Kind: "username", Value: "peer", Message: "reply"},
		{Action: "ban", Kind: "username", Value: "peer", Message: "\x1b[2J"},
	} {
		if _, err := NormalizeCommunityRule(rule); err == nil {
			t.Fatalf("accepted %+v", rule)
		}
	}
}

func TestCommunityRulesPersistenceConflictAndAccountIsolation(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	s, err := New(cfg, path)
	must(t, err)
	defer func() { _ = s.Close() }()
	id := s.community.identity
	page, err := s.CommunityRules(ctx, CommunityRulesRequest{CommunityIdentity: id})
	must(t, err)
	req := CommunityRuleRequest{CommunityIdentity: id, Revision: page.Revision, Rule: CommunityRule{Action: "ban", Kind: "ip", Value: "192.0.2.123/24", Message: "Not available"}, Confirm: true}
	if _, err := s.SetCommunityRule(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetCommunityRule(ctx, req); !errors.Is(err, ErrCommunityMessageState) {
		t.Fatal("stale mutation", err)
	}
	page, err = s.CommunityRules(ctx, CommunityRulesRequest{CommunityIdentity: id})
	failIf(t, err != nil || len(page.Rules) != 1 || page.Rules[0].Value != "192.0.2.0/24", page, err)
	must(t, s.Close())
	s, err = New(cfg, path)
	must(t, err)
	id = s.community.identity
	reloaded, err := s.CommunityRules(ctx, CommunityRulesRequest{CommunityIdentity: id})
	failIf(t, err != nil || len(reloaded.Rules) != 1 || reloaded.Rules[0].ID != page.Rules[0].ID, reloaded, err)
	s.mu.Lock()
	s.cfg.Soulseek.Username = "other"
	err = s.loadCommunityLocked(ctx)
	s.mu.Unlock()
	failIf(t, err != nil || len(s.community.rules) != 0, "account rule leak", err)
}

func TestCommunitySharePolicyMatrix(t *testing.T) {
	cfg := testConfig(t)
	cfg.Soulseek.Username = "self"
	cfg.Shares = []config.Share{{Name: "public", Access: "public"}, {Name: "buddy", Access: "buddy"}, {Name: "trusted", Access: "trusted"}}
	s := &Service{cfg: cfg, uploadEpoch: 1, community: communityState{buddies: map[string]CommunityBuddy{"buddy": {}, "trusted": {Trusted: true}, "self": {Trusted: true}}}}
	countryAddress := netip.MustParseAddr("8.8.8.8")
	code := country.Lookup(countryAddress)
	failIf(t, code == "", "country fixture has no country")
	for _, tc := range []struct {
		name             string
		rule             CommunityRule
		address          netip.Addr
		blocked, pending bool
	}{
		{name: "none", address: netip.MustParseAddr("192.0.2.1")},
		{name: "ignore", rule: CommunityRule{Action: "ignore", Kind: "username", Value: "trusted"}},
		{name: "username", rule: CommunityRule{Action: "ban", Kind: "username", Value: "trusted"}, blocked: true},
		{name: "ip", rule: CommunityRule{Action: "ban", Kind: "ip", Value: "192.0.2.0/24"}, address: netip.MustParseAddr("192.0.2.1"), blocked: true},
		{name: "ip-pending", rule: CommunityRule{Action: "ban", Kind: "ip", Value: "192.0.2.0/24"}, blocked: true, pending: true},
		{name: "country", rule: CommunityRule{Action: "ban", Kind: "country", Value: code}, address: countryAddress, blocked: true},
		{name: "country-unknown", rule: CommunityRule{Action: "ban", Kind: "country", Value: code}, address: netip.MustParseAddr("2001:db8::1")},
		{name: "country-pending", rule: CommunityRule{Action: "ban", Kind: "country", Value: code}, blocked: true, pending: true},
	} {
		s.community.rules = nil
		if tc.rule.Action != "" {
			rule, err := NormalizeCommunityRule(tc.rule)
			must(t, err)
			s.community.rules = []CommunityRule{rule}
		}
		for _, reveal := range []bool{false, true} {
			for i := range s.cfg.Shares {
				s.cfg.Shares[i].Reveal = reveal
			}
			for _, username := range []string{"stranger", "buddy", "trusted", "self", "Trusted"} {
				permission := s.communitySharePermission(1, accountKey(cfg), username, tc.address)
				blocked := tc.blocked && (tc.name != "username" || username == "trusted")
				failIfFmt(t, permission.Banned != blocked || permission.NeedsAddress != tc.pending, "%s/%s: wrong ban or pending state %+v", tc.name, username, permission)
				for _, root := range []string{"public", "buddy", "trusted"} {
					allowed := root == "public" || root == "buddy" && (username == "buddy" || username == "trusted") || root == "trusted" && username == "trusted"
					want := soulseek.ShareHidden
					if !blocked {
						if allowed {
							want = soulseek.ShareAllowed
						} else if reveal {
							want = soulseek.ShareLocked
						}
					}
					failIfFmt(t, permission.Roots[root] != want, "%s/%s/%s reveal=%v: %v != %v", tc.name, username, root, reveal, permission.Roots[root], want)
				}
			}
			anonymous := s.communitySharePermission(1, accountKey(cfg), "", netip.Addr{})
			failIf(t, anonymous.Banned || anonymous.Roots["public"] != soulseek.ShareAllowed || anonymous.Roots["buddy"] != soulseek.ShareHidden || anonymous.Roots["trusted"] != soulseek.ShareHidden, "anonymous counts disclosed restricted roots", anonymous)
		}
	}
	failIf(t, !s.communitySharePermission(2, accountKey(cfg), "trusted", countryAddress).Banned, "stale session allowed")
}
