package ipc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/stats"
)

func TestFilterForceAndStatisticsEndpoints(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "local-test", "pw"
	cfg.DownloadDir = t.TempDir()
	cfg.Downloads.FiltersEnabled = true
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	svc, err := daemon.New(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	downloads, err := svc.QueueDownloads([]daemon.DownloadRequest{{Username: "peer", Files: []daemon.DownloadItem{{Filename: `Music\a.exe`, Size: 1}, {Filename: `Music\b.exe`, Size: 2}}}})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(svc, "").handler()
	request := func(method, url, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, url, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(w, r)
		return w
	}
	force := request("POST", "/v1/downloads/force", fmt.Sprintf(`{"ids":[%q,%q]}`, downloads[0].ID, downloads[0].ID))
	if force.Code != 200 {
		t.Fatalf("force: %d %s", force.Code, force.Body.String())
	}
	svc.Close()
	svc, err = daemon.New(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	handler = NewServer(svc, "").handler()
	overview := request("GET", "/v1/stats", "")
	var totals daemon.StatsOverview
	if err = json.Unmarshal(overview.Body.Bytes(), &totals); err != nil || overview.Code != 200 || totals.Lifetime.Download.Filtered != 2 || totals.Lifetime.Download.Forced != 1 {
		t.Fatalf("stats: %d %s %v", overview.Code, overview.Body.String(), err)
	}
	for _, url := range []string{"/v1/stats/peers?limit=1001", "/v1/stats/series?bins=401", "/v1/stats?from=1969-01-01", "/v1/stats?outcome=failed", "/v1/transfer-log?cursor=invalid"} {
		if w := request("GET", url, ""); w.Code != http.StatusBadRequest {
			t.Fatalf("validation %s: %d %s", url, w.Code, w.Body.String())
		}
	}
	log := request("GET", "/v1/transfer-log?limit=1", "")
	var page stats.LogPage
	if err = json.Unmarshal(log.Body.Bytes(), &page); err != nil || len(page.Entries) != 1 || page.NextCursor == "" {
		t.Fatalf("log page: %s %v", log.Body.String(), err)
	}
	body := fmt.Sprintf(`{"cutoff":%q,"logs":true,"daily":true}`, time.Now().UTC().Format(time.RFC3339Nano))
	preview := request("POST", "/v1/stats/prune/preview", body)
	if preview.Code != 200 {
		t.Fatal(preview.Body.String())
	}
	if after := request("GET", "/v1/transfer-log?limit=1", ""); !strings.Contains(after.Body.String(), page.Entries[0].ID) {
		t.Fatal("preview deleted data")
	}
	if apply := request("POST", "/v1/stats/prune", body); apply.Code != 200 {
		t.Fatal(apply.Body.String())
	}
	after := request("GET", "/v1/stats", "")
	var kept daemon.StatsOverview
	if err = json.Unmarshal(after.Body.Bytes(), &kept); err != nil || kept.Lifetime != totals.Lifetime {
		t.Fatalf("pruning changed lifetime: %s", after.Body.String())
	}
}
