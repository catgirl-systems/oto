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
	if err != nil {
		t.Fatal(err)
	}
	req := initial
	req.Settings.Keywords = []string{"hello"}
	saved, err := s.SetCommunityTextSettings(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.Settings.Keywords[0] = "mutated"
	got, err := s.CommunityTextSettings(ctx, id)
	if err != nil || got.Settings.Keywords[0] != "hello" {
		t.Fatal(got, err)
	}
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
	if now.Revision != saved.Revision {
		t.Fatal("published before save")
	}
	s.configPath = goodPath
	cfg, err := config.Load(goodPath)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := New(cfg, filepath.Join(t.TempDir(), "restart.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	now, err = restored.CommunityTextSettings(ctx, restored.community.identity)
	if err != nil || now.Revision != saved.Revision {
		t.Fatal("restart", now, err)
	}
	cfg.Soulseek.Username = "other"
	other, err := New(cfg, filepath.Join(t.TempDir(), "other.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	now, err = other.CommunityTextSettings(ctx, other.community.identity)
	if err != nil || len(now.Settings.Keywords) != 0 {
		t.Fatal("cross-account settings", now, err)
	}
	cmd, err := s.RunCommand(ctx, CommandRequest{CommunityIdentity: id, Name: "text-tools"})
	if err != nil || cmd.TextTools == nil {
		t.Fatal(cmd, err)
	}
	for _, input := range []string{`null`, `{} {}`, `{"unknown":true}`, `[]`} {
		if _, err := s.RunCommand(ctx, CommandRequest{CommunityIdentity: id, Name: "text-tools", Args: []string{saved.Revision, input}}); err == nil {
			t.Fatal("invalid JSON accepted", input)
		}
	}
	cmd, err = s.RunCommand(ctx, CommandRequest{CommunityIdentity: id, Name: "text-tools", Args: []string{saved.Revision, `{"keywords":["world"]}`}})
	if err != nil || cmd.TextTools.Settings.Keywords[0] != "world" {
		t.Fatal(cmd, err)
	}
}
