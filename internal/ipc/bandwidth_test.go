package ipc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestBandwidthConfigIPC(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "u", "secret"
	cfg.Soulseek.ConnectOnStartup = false
	cfg.DownloadDir = t.TempDir()
	svc, err := daemon.New(cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	svc.SetConfigPath(path)
	handler := NewServer(svc, "").handler()
	cfg.Bandwidth.Profiles[0].UploadSpeedLimitKiB = 17
	cfg.Bandwidth.Profiles[0].DownloadSpeedLimitKiB = 31
	raw, _ := json.Marshal(cfg)
	request := func(method string, raw []byte, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, "/v1/config", bytes.NewReader(raw)))
		if w.Code != status {
			t.Fatalf("status %d, body %s", w.Code, w.Body.String())
		}
		return w
	}
	w := request(http.MethodPut, raw, 200)
	var safe config.SafeConfig
	if err := json.Unmarshal(w.Body.Bytes(), &safe); err != nil {
		t.Fatal(err)
	}
	if safe.Bandwidth.ActiveProfileLimits() != cfg.Bandwidth.ActiveProfileLimits() || strings.Contains(w.Body.String(), "secret") {
		t.Fatal("canonical response lost limits or leaked password")
	}
	if svc.Snapshot().Presence != daemon.PresenceOffline {
		t.Fatal("bandwidth save connected offline service")
	}
	var body map[string]json.RawMessage
	_ = json.Unmarshal(raw, &body)
	body["bandwidth"] = json.RawMessage(`null`)
	invalid, _ := json.Marshal(body)
	request(http.MethodPatch, invalid, 400)
	if svc.Config().Bandwidth.ActiveProfileLimits() != cfg.Bandwidth.ActiveProfileLimits() {
		t.Fatal("invalid request changed accepted limits")
	}
	delete(body, "bandwidth")
	body["uploads"] = json.RawMessage(`{"profiles":[{"name":"Legacy","speed_limit_kib":9}],"active_profile":"Legacy","limit_scope":"per_transfer","scheduling":"random"}`)
	legacy, _ := json.Marshal(body)
	w = request(http.MethodPatch, legacy, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &safe); err != nil {
		t.Fatal(err)
	}
	if got := safe.Bandwidth.ActiveProfileLimits(); got.Name != "Legacy" || got.UploadSpeedLimitKiB != 9 || got.DownloadSpeedLimitKiB != 0 {
		t.Fatalf("legacy migration: %+v", got)
	}
	loaded, err := config.Load(path)
	if err != nil || loaded.Bandwidth.ActiveProfileLimits() != safe.Bandwidth.ActiveProfileLimits() {
		t.Fatalf("offline save persistence: %+v %v", loaded, err)
	}
}
