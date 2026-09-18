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
	must(t, err)
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
		failIfFmt(t, w.Code != status, "status %d, body %s", w.Code, w.Body.String())
		return w
	}
	w := request(http.MethodPut, raw, 200)
	var safe config.SafeConfig
	must(t, json.Unmarshal(w.Body.Bytes(), &safe))
	failIf(t, safe.Bandwidth.ActiveProfileLimits() != cfg.Bandwidth.ActiveProfileLimits() || strings.Contains(w.Body.String(), "secret"), "canonical response lost limits or leaked password")
	failIf(t, svc.Snapshot().Presence != daemon.PresenceOffline, "bandwidth save connected offline service")
	var body map[string]json.RawMessage
	_ = json.Unmarshal(raw, &body)
	body["bandwidth"] = json.RawMessage(`null`)
	invalid, _ := json.Marshal(body)
	request(http.MethodPatch, invalid, 400)
	failIf(t, svc.Config().Bandwidth.ActiveProfileLimits() != cfg.Bandwidth.ActiveProfileLimits(), "invalid request changed accepted limits")
	delete(body, "bandwidth")
	body["uploads"] = json.RawMessage(`{"profiles":[{"name":"Legacy","speed_limit_kib":9}],"active_profile":"Legacy","limit_scope":"per_transfer","scheduling":"random"}`)
	legacy, _ := json.Marshal(body)
	w = request(http.MethodPatch, legacy, 200)
	must(t, json.Unmarshal(w.Body.Bytes(), &safe))
	if got := safe.Bandwidth.ActiveProfileLimits(); got.Name != "Legacy" || got.UploadSpeedLimitKiB != 9 || got.DownloadSpeedLimitKiB != 0 {
		t.Fatalf("legacy migration: %+v", got)
	}
	loaded, err := config.Load(path)
	failIfFmt(t, err != nil || loaded.Bandwidth.ActiveProfileLimits() != safe.Bandwidth.ActiveProfileLimits(), "offline save persistence: %+v %v", loaded, err)
}
