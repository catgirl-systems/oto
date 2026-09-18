package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
)

func TestAwaySettingsAtomicRestartAndIsolation(t *testing.T) {
	s := downloadService(t)
	s.configPath = filepath.Join(t.TempDir(), "config.json")
	ctx := context.Background()
	id := s.community.identity
	out, err := s.RunCommand(ctx, CommandRequest{CommunityIdentity: id, Name: "auto-away", Args: []string{"0", "60"}})
	failIf(t, err != nil || out.Away.Settings.AutoAwaySeconds != 60, out, err)
	if _, err := s.RunCommand(ctx, CommandRequest{CommunityIdentity: id, Name: "auto-away", Args: []string{"0", "20"}}); err == nil {
		t.Fatal("stale command accepted")
	}
	out, err = s.RunCommand(ctx, CommandRequest{CommunityIdentity: id, Name: "away-reply", Args: []string{"", "back later"}})
	failIf(t, err != nil || out.Away.Settings.AutoReply != "back later", out, err)
	path := s.configPath
	s.configPath = t.TempDir()
	req := CommunityAwaySettingsRequest{CommunityIdentity: id, Expected: out.Away.Settings, Settings: config.CommunityAway{AutoAwaySeconds: 30}}
	if _, err := s.SetCommunityAwaySettings(ctx, req); err == nil {
		t.Fatal("failed save accepted")
	}
	current, err := s.CommunityAwaySettings(ctx, id)
	failIf(t, err != nil || current.Settings.AutoAwaySeconds != 60, "failed save published", current, err)
	cfg, err := config.Load(path)
	must(t, err)
	restarted, err := New(cfg, filepath.Join(t.TempDir(), "state.db"))
	must(t, err)
	defer restarted.Close()
	current, err = restarted.CommunityAwaySettings(ctx, restarted.community.identity)
	failIf(t, err != nil || current.Settings.AutoAwaySeconds != 60 || current.Settings.AutoReply != "back later", "restart", current, err)
	cfg.Soulseek.Username = "other"
	other, err := New(cfg, filepath.Join(t.TempDir(), "other.db"))
	must(t, err)
	defer other.Close()
	current, err = other.CommunityAwaySettings(ctx, other.community.identity)
	failIf(t, err != nil || current.Settings.AutoAwaySeconds != 0, "account leakage", current, err)
}
