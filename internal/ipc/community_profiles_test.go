package ipc

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestCommunityProfilesIPCValidation(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "local", "local-only"
	client, _ := communityIPC(t, cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
	ctx := context.Background()
	summary, err := client.CommunitySummary(ctx)
	must(t, err)
	req := daemon.CommunityProfileRequest{CommunityIdentity: summary.CommunityIdentity, Username: "Alice", Frontend: "one"}
	p, err := client.CommunityProfile(ctx, req)
	failIf(t, err != nil || p.State != "offline" || p.Username != "Alice" || p.Description != "", p, err)
	if _, err := client.StartCommunityProfile(ctx, req); err == nil {
		t.Fatal("offline fetch succeeded")
	}
	if _, err := client.CommunityProfilePicture(ctx, daemon.CommunityProfilePictureRequest{CommunityIdentity: req.CommunityIdentity, Username: req.Username}); err == nil {
		t.Fatal("uncached picture succeeded")
	}
	base := "/v1/community/profile?" + communityRoomValues(req.CommunityIdentity).Encode()
	for _, suffix := range []string{"&username=Alice", "&frontend=one", "&username=%1B&frontend=one"} {
		resp, err := client.http.Do(mustRequest(http.MethodGet, "http://oto.local"+base+suffix, nil))
		must(t, err)
		_ = resp.Body.Close()
		failIf(t, resp.StatusCode != http.StatusBadRequest, suffix, resp.StatusCode)
	}
	req.Session++
	if _, err := client.CommunityProfile(ctx, req); err == nil {
		t.Fatal("stale identity accepted")
	}
}
func TestCommunityEscapedMetadataPageBudgets(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "local", "local-only"
	client, _ := communityIPC(t, cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
	ctx := context.Background()
	summary, err := client.CommunitySummary(ctx)
	must(t, err)
	id := summary.CommunityIdentity
	for i := 0; i < 200; i++ {
		_, err := client.SetCommunityBuddy(ctx, daemon.CommunityBuddyRequest{CommunityIdentity: id, Username: fmt.Sprintf("u%03d", i), Note: strings.Repeat("<", 4096)})
		must(t, err)
		if _, err := client.SetCommunityInterest(ctx, daemon.CommunityInterestRequest{CommunityIdentity: id, Item: strings.Repeat("<", 1020) + fmt.Sprintf("%04d", i), Opinion: "like"}); err != nil {
			t.Fatal(err)
		}
	}
	total, pages := 0, 0
	for cursor := ""; ; {
		page, err := client.CommunityBuddies(ctx, daemon.CommunityBuddiesRequest{CommunityIdentity: id, Cursor: cursor, Limit: 200, Sort: "note"})
		must(t, err)
		total += len(page.Buddies)
		pages++
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
		failIf(t, pages > 200, "buddy pagination loop")
	}
	failIf(t, total != 200 || pages < 2, "buddy byte paging", total, pages)
	total, pages = 0, 0
	for cursor := ""; ; {
		page, err := client.CommunityInterests(ctx, daemon.CommunityInterestsRequest{CommunityIdentity: id, Cursor: cursor, Limit: 200})
		must(t, err)
		total += len(page.Interests)
		pages++
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
		failIf(t, pages > 200, "interest pagination loop")
	}
	failIf(t, total != 200 || pages < 2, "interest byte paging", total, pages)
}
