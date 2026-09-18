package ipc

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestCommunityInterestsIPCMultipleFrontends(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "local", "local-only"
	first, service := communityIPC(t, cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
	second := NewClient(first.path)
	ctx := context.Background()
	summary, err := first.CommunitySummary(ctx)
	must(t, err)
	failIf(t, !slices.Contains(summary.Capabilities, "interests") || !slices.Contains(summary.Capabilities, "self-profile"), "missing capabilities")
	id := summary.CommunityIdentity
	added, err := first.SetCommunityInterest(ctx, daemon.CommunityInterestRequest{CommunityIdentity: id, Item: " TECHno ", Opinion: "like"})
	must(t, err)
	page, err := second.CommunityInterests(ctx, daemon.CommunityInterestsRequest{CommunityIdentity: id})
	failIf(t, err != nil || len(page.Interests) != 1 || page.Interests[0].Item != "techno", page, err)
	changed, err := second.SetCommunityInterest(ctx, daemon.CommunityInterestRequest{CommunityIdentity: id, Item: "techno", Opinion: "dislike", Revision: &added.Interest.Revision})
	must(t, err)
	if _, err := first.SetCommunityInterest(ctx, daemon.CommunityInterestRequest{CommunityIdentity: id, Item: "techno", Opinion: "like", Revision: &added.Interest.Revision}); err == nil {
		t.Fatal("stale interest overwrite")
	}
	req := daemon.CommunityInterestRequest{CommunityIdentity: id, Item: "techno", Remove: true, Revision: &changed.Interest.Revision}
	if _, err := first.SetCommunityInterest(ctx, req); err == nil {
		t.Fatal("unconfirmed delete")
	}
	req.Confirm = true
	if err := first.Do(ctx, http.MethodPut, "/v1/community/interests", req, nil); err == nil {
		t.Fatal("PUT removed interest")
	}
	removed, err := first.SetCommunityInterest(ctx, req)
	failIf(t, err != nil || !removed.Removed || removed.Interest.Item != "techno", removed, err)
	p, err := first.CommunitySelfProfile(ctx, id)
	must(t, err)
	p.Description = "private description 猫/?\nline"
	saved, err := second.SetCommunitySelfProfile(ctx, p)
	must(t, err)
	p.Description = "stale"
	if _, err := first.SetCommunitySelfProfile(ctx, p); err == nil {
		t.Fatal("stale description overwrite")
	}
	latest, err := first.CommunitySelfProfile(ctx, id)
	failIf(t, err != nil || latest != saved, latest, err)
	encoded, err := json.Marshal(service.Snapshot())
	must(t, err)
	failIf(t, strings.Contains(string(encoded), "private description"), "state contains description")
	base := "/v1/community/interests?" + communityRoomValues(id).Encode()
	for _, path := range []string{base + "&limit=-1", base + "&limit=no", base + "&cursor=%1B", base + "&cursor=UPPERCASE", base + "&query=%1B", "/v1/community/profile/self?session=no"} {
		resp, err := first.http.Do(mustRequest(http.MethodGet, "http://oto.local"+path, nil))
		must(t, err)
		_ = resp.Body.Close()
		failIf(t, resp.StatusCode != http.StatusBadRequest, path, resp.StatusCode)
	}
	id.Session++
	if _, err := first.CommunitySelfProfile(ctx, id); err == nil {
		t.Fatal("stale identity accepted")
	}
	if _, err := first.CommunityInterests(ctx, daemon.CommunityInterestsRequest{CommunityIdentity: id}); err == nil {
		t.Fatal("stale interest identity accepted")
	}
}
