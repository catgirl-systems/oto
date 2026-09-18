package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
)

func TestReceivingSettingsConsentPersistenceAndIsolation(t *testing.T) {
	s := downloadService(t)
	s.configPath = filepath.Join(t.TempDir(), "config.json")
	ctx := context.Background()
	id := s.community.identity
	current, err := s.ReceivingSettings(ctx, id)
	failIf(t, err != nil || current.Settings.Mode != "" || current.Settings.CompletionHooks || current.EffectiveDirectory != filepath.Join(s.cfg.DownloadDir, "received"), current, err)
	req := ReceivingSettingsRequest{CommunityIdentity: id, Settings: config.Receiving{Mode: "users", Users: []string{"bob", "Alice"}}}
	if _, err := s.SetReceivingSettings(ctx, req); err == nil {
		t.Fatal("implicit consent")
	}
	req.Confirm = true
	out, err := s.SetReceivingSettings(ctx, req)
	must(t, err)
	failIf(t, out.Settings.Users[0] != "Alice", "not canonical")
	out.Settings.Users[0] = "mutated"
	req.Settings.Users[0] = "mutated"
	current, err = s.ReceivingSettings(ctx, id)
	failIf(t, err != nil || current.Settings.Users[0] != "Alice", "mutable alias", current, err)
	cfg, err := config.Load(s.configPath)
	failIf(t, err != nil || !equalReceiving(cfg.Receiving[id.Account], current.Settings), "policy not persisted", err)
	req.Settings = config.Receiving{Mode: "trusted"}
	if _, err := s.SetReceivingSettings(ctx, req); !errors.Is(err, ErrCommunityMessageState) {
		t.Fatal("stale edit", err)
	}
	req.Expected = current.Settings
	req.Account = "another account"
	if _, err := s.SetReceivingSettings(ctx, req); err == nil {
		t.Fatal("cross-account edit")
	}
	req.Account = id.Account
	req.Settings.Directory = s.cfg.DownloadDir
	if _, err := s.SetReceivingSettings(ctx, req); err == nil {
		t.Fatal("ordinary download destination accepted")
	}
	req.Settings.Directory = ""
	s.configPath = t.TempDir()
	if _, err := s.SetReceivingSettings(ctx, req); err == nil {
		t.Fatal("failed persistence accepted")
	}
	got, err := s.ReceivingSettings(ctx, id)
	failIf(t, err != nil || !equalReceiving(got.Settings, current.Settings), "failed save changed consent", err)
}
