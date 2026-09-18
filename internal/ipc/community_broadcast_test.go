package ipc

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestBroadcastPreviewIPCAndRestart(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "u", "p"
	cfg.DownloadDir = t.TempDir()
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	c, s := communityIPC(t, cfg, path)
	ctx := context.Background()
	summary, err := c.CommunitySummary(ctx)
	must(t, err)
	id := summary.CommunityIdentity
	if _, err := s.SetCommunityBuddy(ctx, daemon.CommunityBuddyRequest{CommunityIdentity: id, Username: "Alice"}); err != nil {
		t.Fatal(err)
	}
	req := daemon.CommunityBroadcastRequest{CommunityIdentity: id, RequestID: "broadcast-preview", Audience: "buddies", Text: "hello"}
	if _, err := c.PreviewCommunityBroadcast(ctx, req); err == nil {
		t.Fatal("offline buddy selected implicitly")
	}
	req.Offline = []string{"Alice"}
	out, err := c.PreviewCommunityBroadcast(ctx, req)
	failIf(t, err != nil || out.Total != 1 || out.State != "preview", out, err)
	if _, err := c.CommunityBroadcast(ctx, id, req.RequestID, -1); err == nil {
		t.Fatal("invalid cursor")
	}
	must(t, s.Close())
	next, _ := communityIPC(t, cfg, path)
	summary, err = next.CommunitySummary(ctx)
	must(t, err)
	recovered, err := next.CommunityBroadcast(ctx, summary.CommunityIdentity, req.RequestID, 0)
	failIf(t, err != nil || recovered.State != "stale-preview" || recovered.Token != out.Token || recovered.Recipients[0].Username != "Alice", recovered, err)
	if _, err := next.CommunityBroadcast(ctx, id, req.RequestID, 0); err == nil {
		t.Fatal("old daemon identity accepted")
	}
}
