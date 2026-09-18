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
	must(t, err)
	req := daemon.CommunityDiscoveryRequest{CommunityIdentity: summary.CommunityIdentity, Kind: "global", Frontend: "one"}
	page, err := client.CommunityDiscovery(ctx, req)
	failIf(t, err != nil || page.State != "offline" || len(page.Rows) != 0 || page.CommunityIdentity != req.CommunityIdentity, page, err)
	if _, err := client.StartCommunityDiscovery(ctx, req); err == nil {
		t.Fatal("offline query pretended to send")
	}
	base := "/v1/community/discovery?" + communityRoomValues(req.CommunityIdentity).Encode()
	for _, suffix := range []string{"&kind=invalid&frontend=one", "&kind=global", "&kind=item&target=&frontend=one", "&kind=global&target=not-allowed&frontend=one", "&kind=similar&limit=-1&frontend=one", "&kind=similar&limit=no&frontend=one"} {
		resp, err := client.http.Do(mustRequest(http.MethodGet, "http://oto.local"+base+suffix, nil))
		must(t, err)
		_ = resp.Body.Close()
		failIf(t, resp.StatusCode != http.StatusBadRequest, suffix, resp.StatusCode)
	}
	req.Session++
	if _, err := client.CommunityDiscovery(ctx, req); err == nil {
		t.Fatal("stale session query")
	}
}
