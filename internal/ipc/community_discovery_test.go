package ipc

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestCommunityDiscoveryIPCValidationAndOffline(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "local", "local-only"
	client, _ := communityIPC(t, cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
	ctx := context.Background()
	summary, err := client.CommunitySummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	req := daemon.CommunityDiscoveryRequest{CommunityIdentity: summary.CommunityIdentity, Kind: "global", Frontend: "one"}
	page, err := client.CommunityDiscovery(ctx, req)
	if err != nil || page.State != "offline" || len(page.Rows) != 0 || page.CommunityIdentity != req.CommunityIdentity {
		t.Fatal(page, err)
	}
	if _, err := client.StartCommunityDiscovery(ctx, req); err == nil {
		t.Fatal("offline query pretended to send")
	}
	base := "/v1/community/discovery?" + communityRoomValues(req.CommunityIdentity).Encode()
	for _, suffix := range []string{"&kind=invalid&frontend=one", "&kind=global", "&kind=item&target=&frontend=one", "&kind=global&target=not-allowed&frontend=one", "&kind=similar&limit=-1&frontend=one", "&kind=similar&limit=no&frontend=one"} {
		resp, err := client.http.Do(mustRequest(http.MethodGet, "http://oto.local"+base+suffix, nil))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatal(suffix, resp.StatusCode)
		}
	}
	req.Session++
	if _, err := client.CommunityDiscovery(ctx, req); err == nil {
		t.Fatal("stale session query")
	}
}
