package ipc

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestReceivingSettingsIPCConsentAndSessionFence(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "receiver", "local-only"
	cfg.DownloadDir = t.TempDir()
	client, _ := communityIPC(t, cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
	ctx := context.Background()
	summary, err := client.CommunitySummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before, err := client.ReceivingSettings(ctx, summary.CommunityIdentity)
	if err != nil || before.Settings.Mode != "" {
		t.Fatal(before, err)
	}
	req := daemon.ReceivingSettingsRequest{CommunityIdentity: summary.CommunityIdentity, Expected: before.Settings, Settings: config.Receiving{Mode: "trusted"}}
	if _, err := client.SetReceivingSettings(ctx, req); err == nil {
		t.Fatal("missing confirmation accepted")
	}
	req.Confirm = true
	got, err := client.SetReceivingSettings(ctx, req)
	if err != nil || got.Settings.Mode != "trusted" || got.Settings.CompletionHooks {
		t.Fatal(got, err)
	}
	req.Expected = got.Settings
	req.Settings.Mode = "everyone"
	if _, err := client.SetReceivingSettings(ctx, req); err == nil {
		t.Fatal("everyone accepted")
	}
	req.Settings.Mode = "off"
	req.Session++
	if _, err := client.SetReceivingSettings(ctx, req); err == nil {
		t.Fatal("stale session accepted")
	}
	after, err := client.ReceivingSettings(ctx, summary.CommunityIdentity)
	if err != nil || after.Settings.Mode != "trusted" {
		t.Fatal(after, err)
	}
}
