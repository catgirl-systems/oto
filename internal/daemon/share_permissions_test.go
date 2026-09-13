package daemon

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestShareAccessAtomicPersistenceAndStaleSettings(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "local", "local-only"
	cfg.DownloadDir = t.TempDir()
	cfg.Shares = []config.Share{{Name: "Music", Path: t.TempDir()}}
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	s, err := New(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	configPath := filepath.Join(t.TempDir(), "config.toml")
	s.SetConfigPath(configPath)
	ctx := context.Background()
	summary, err := s.CommunitySummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	req := ShareAccessRequest{CommunityIdentity: summary.CommunityIdentity, Expected: cfg.Shares[0], Revision: s.shareIndexRevision, Access: "buddy", Reveal: true}
	index := s.shares
	if _, err := s.SetShareAccess(ctx, req); err == nil {
		t.Fatal("unconfirmed mutation")
	}
	req.Confirm = true
	out, err := s.SetShareAccess(ctx, req)
	if err != nil || out.Share.Access != "buddy" || !out.Share.Reveal || out.Revision <= req.Revision || s.shares != index {
		t.Fatal("metadata save rebuilt index or failed", out, err)
	}
	duplicate, err := s.SetShareAccess(ctx, req)
	if err != nil || duplicate.Revision != out.Revision {
		t.Fatal("retry was not idempotent", duplicate, err)
	}
	stale := req
	stale.Access = "public"
	if _, err := s.SetShareAccess(ctx, stale); !errors.Is(err, ErrCommunityMessageState) {
		t.Fatal("stale overwrite", err)
	}
	if err := s.UpdateConfig(cfg); err == nil {
		t.Fatal("stale settings reopened restricted root")
	}
	persisted, err := config.Load(configPath)
	if err != nil || persisted.Shares[0] != out.Share {
		t.Fatal("not persisted", persisted.Shares, err)
	}
	before := out
	req.Expected, req.Revision, req.Access = out.Share, out.Revision, "trusted"
	s.SetConfigPath(t.TempDir())
	if _, err := s.SetShareAccess(ctx, req); err == nil {
		t.Fatal("failed write reported saved")
	}
	if s.cfg.Shares[0] != before.Share || s.shareIndexRevision != before.Revision {
		t.Fatal("failed write changed live policy")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := New(persisted, path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err := restored.CommunitySummary(ctx); err != nil {
		t.Fatal(err)
	}
	permission := restored.communitySharePermission(restored.uploadEpoch, accountKey(persisted), "stranger", netip.MustParseAddr("127.0.0.1"))
	if permission.Roots["Music"] != soulseek.ShareLocked {
		t.Fatal("restarted permission", permission)
	}
}
