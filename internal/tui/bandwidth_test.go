package tui

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/ipc"
)

func TestBandwidthSettingsSaveSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ipc.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan config.Config, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/v1/config" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var cfg config.Config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			t.Error(err)
		}
		requests <- cfg
		_ = json.NewEncoder(w).Encode(cfg.Redacted())
	})}
	go server.Serve(ln)
	defer server.Close()
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "u", "p"
	m := newModel(context.Background(), ipc.NewClient(path), "", false, cfg)
	m.workspace, m.settingsSection = workspaceSettings, settingsBandwidth
	m.cfg.Bandwidth.Profiles[0].UploadSpeedLimitKiB = 64
	m.cfg.Bandwidth.Profiles[0].DownloadSpeedLimitKiB = 128
	cmd := m.key(key("s"))
	if cmd == nil {
		t.Fatal("save key returned no command")
	}
	m.cfg.Bandwidth.Profiles[0].Name = "later edit"
	m.cfg.Bandwidth.Profiles[0].UploadSpeedLimitKiB = 0
	m.cfg.Bandwidth.Profiles[0].DownloadSpeedLimitKiB = 0
	if len(requests) != 0 {
		t.Fatal("staging sent request before command execution")
	}
	msg := cmd().(settingsMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	got := (<-requests).Bandwidth.ActiveProfileLimits()
	if got.Name != "Unlimited" || got.UploadSpeedLimitKiB != 64 || got.DownloadSpeedLimitKiB != 128 {
		t.Fatalf("save snapshot aliased staged edits: %+v", got)
	}
}
