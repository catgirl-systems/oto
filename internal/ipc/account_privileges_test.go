package ipc

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestAccountPrivilegesIPCOfflineAndValidation(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username = "local"
	cfg.Soulseek.Password = "local-only"
	client, _ := communityIPC(t, cfg, filepath.Join(t.TempDir(), "state.db"))
	ctx := context.Background()
	summary, err := client.CommunitySummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	req := daemon.AccountPrivilegesRequest{CommunityIdentity: summary.CommunityIdentity}
	out, err := client.AccountPrivileges(ctx, req)
	if err != nil || out.Fresh || out.Known || out.Error == "" {
		t.Fatal(out, err)
	}
	if _, err := client.GiftAccountPrivileges(ctx, daemon.AccountPrivilegeGiftRequest{CommunityIdentity: req.CommunityIdentity, Username: "Alice", RequestID: "one", Days: 1, Confirm: true}); err == nil {
		t.Fatal("offline gift accepted")
	}
	base := "/v1/account/privileges?" + communityRoomValues(req.CommunityIdentity).Encode()
	if err := client.Do(ctx, http.MethodGet, base+"&refresh=invalid", nil, &out); err == nil {
		t.Fatal("invalid refresh accepted")
	}
	req.Session++
	if _, err := client.AccountPrivileges(ctx, req); err == nil {
		t.Fatal("stale query accepted")
	}
}
