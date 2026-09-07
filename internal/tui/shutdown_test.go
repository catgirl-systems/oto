package tui

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/ipc"
)

func TestWaitForUploadsSettingSaveAndQuit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	socket := filepath.Join(t.TempDir(), "ipc.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var cfg config.Config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			t.Error(err)
		}
		if err := cfg.Save(path); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(cfg.Redacted())
	})}
	go server.Serve(ln)
	defer server.Close()
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "test", "test"
	m := newModel(context.Background(), ipc.NewClient(socket), path, true, cfg)
	m.width = 120
	m.workspace, m.settingsSection = workspaceSettings, settingsUploads
	found := false
	for i, field := range m.settingFields() {
		if field.id == settingWaitForActiveUploadsOnQuit {
			m.cursor = i
			m.key(key("enter"))
			found = true
			break
		}
	}
	if !found || !m.cfg.Uploads.WaitForActiveUploadsOnQuit {
		t.Fatal("missing opt-in toggle")
	}
	m.confirm = true
	if strings.Contains(m.footerView(), "Wait for") {
		t.Fatal("unsaved toggle changed quit policy")
	}
	m.confirm = false
	cmd := m.key(key("s"))
	if cmd == nil {
		t.Fatal("no save command")
	}
	if msg := cmd().(settingsMsg); msg.err != nil {
		t.Fatal(msg.err)
	}
	saved, err := config.Load(path)
	if err != nil || !saved.Uploads.WaitForActiveUploadsOnQuit {
		t.Fatal("save/reload", err)
	}
	updated, _ := m.Update(statusMsg{snapshot: daemon.Snapshot{Config: saved.Redacted()}})
	m = updated.(model)
	m.confirm = true
	if !strings.Contains(m.footerView(), "Wait for active uploads, then interrupt downloads?") {
		t.Fatal(m.footerView())
	}
	if cmd := m.key(key("y")); cmd == nil {
		t.Fatal("cannot confirm quit")
	} else if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("not quitting")
	}
	// Attached UI exit must only quit the frontend, regardless of the saved toggle.
	m.confirm, m.transient = false, false
	if cmd := m.key(key("q")); cmd == nil {
		t.Fatal("attached exit prompted")
	} else if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("attached exit did not quit")
	}
}
