package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
)

func TestCommunityTextSettingsRestartAtomicityAndIsolation(t *testing.T) {
	s := downloadService(t)
	s.configPath = filepath.Join(t.TempDir(), "config.json")
	ctx := context.Background()
	id := s.community.identity
	initial, err := s.CommunityTextSettings(ctx, id)
	must(t, err)
	req := initial
	req.Settings.Keywords = []string{"hello"}
	saved, err := s.SetCommunityTextSettings(ctx, req)
	must(t, err)
	req.Settings.Keywords[0] = "mutated"
	got, err := s.CommunityTextSettings(ctx, id)
	failIf(t, err != nil || got.Settings.Keywords[0] != "hello", got, err)
	got.Settings.Keywords[0] = "changed"
	if _, err := s.SetCommunityTextSettings(ctx, CommunityTextSettings{CommunityIdentity: id, Revision: initial.Revision, Settings: got.Settings}); err == nil {
		t.Fatal("stale overwrite accepted")
	}
	goodPath := s.configPath
	s.configPath = t.TempDir()
	if _, err := s.SetCommunityTextSettings(ctx, got); err == nil {
		t.Fatal("failed save accepted")
	}
	now, _ := s.CommunityTextSettings(ctx, id)
	failIf(t, now.Revision != saved.Revision, "published before save")
	s.configPath = goodPath
	cfg, err := config.Load(goodPath)
	must(t, err)
	restored, err := New(cfg, filepath.Join(t.TempDir(), "restart.db"))
	must(t, err)
	defer restored.Close()
	now, err = restored.CommunityTextSettings(ctx, restored.community.identity)
	failIf(t, err != nil || now.Revision != saved.Revision, "restart", now, err)
	cfg.Soulseek.Username = "other"
	other, err := New(cfg, filepath.Join(t.TempDir(), "other.db"))
	must(t, err)
	defer other.Close()
	now, err = other.CommunityTextSettings(ctx, other.community.identity)
	failIf(t, err != nil || len(now.Settings.Keywords) != 0, "cross-account settings", now, err)
	cmd, err := s.RunCommand(ctx, CommandRequest{CommunityIdentity: id, Name: "text-tools"})
	failIf(t, err != nil || cmd.TextTools == nil, cmd, err)
	for _, input := range []string{`null`, `{} {}`, `{"unknown":true}`, `[]`} {
		if _, err := s.RunCommand(ctx, CommandRequest{CommunityIdentity: id, Name: "text-tools", Args: []string{saved.Revision, input}}); err == nil {
			t.Fatal("invalid JSON accepted", input)
		}
	}
	cmd, err = s.RunCommand(ctx, CommandRequest{CommunityIdentity: id, Name: "text-tools", Args: []string{saved.Revision, `{"keywords":["world"]}`}})
	failIf(t, err != nil || cmd.TextTools.Settings.Keywords[0] != "world", cmd, err)
}
