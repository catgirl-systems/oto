package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
)

func TestBulkSettingsCannotOverwriteAccountScopedConsent(t *testing.T) {
	s := downloadService(t)
	s.configPath = filepath.Join(t.TempDir(), "config.json")
	account := accountKey(s.cfg)
	s.cfg.CommunityAway = map[string]config.CommunityAway{account: {AutoReply: "unchanged"}}
	s.cfg.CommunityText = map[string]config.CommunityTextTools{account: {}}
	enabled := config.Receiving{Mode: "users", Users: []string{"Alice"}}
	s.cfg.Receiving = map[string]config.Receiving{account: enabled}
	stale := s.cfg
	if _, err := s.SetReceivingSettings(context.Background(), ReceivingSettingsRequest{CommunityIdentity: s.community.identity, Expected: enabled, Settings: config.Receiving{Mode: "off"}, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	stale.CommunityAway = nil
	stale.CommunityText = nil
	stale.Downloads.FileNotifications = !stale.Downloads.FileNotifications
	if err := s.UpdateConfig(stale); err != nil {
		t.Fatal(err)
	}
	if s.cfg.Receiving[account].Mode != "off" || s.cfg.CommunityAway[account].AutoReply != "unchanged" || len(s.cfg.CommunityText) != 1 {
		t.Fatal("stale bulk snapshot overwrote account-scoped settings")
	}
	stale.Receiving = nil
	if err := s.UpdateConfig(stale); err != nil {
		t.Fatal(err)
	}
	if s.cfg.Receiving[account].Mode != "off" {
		t.Fatal("omitted receiving policy overwrote consent")
	}
}
