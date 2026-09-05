package tui

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/ipc"
	"github.com/catgirl-systems/oto/internal/stats"
	"github.com/charmbracelet/x/ansi"
)

func TestStatsPruneDaysConfirmation(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	path := filepath.Join(t.TempDir(), "api.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	type request struct {
		path string
		body ipc.PruneRequest
	}
	requests := make(chan request, 2)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body ipc.PruneRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.Method != http.MethodPost {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requests <- request{r.URL.Path, body}
		_ = json.NewEncoder(w).Encode(stats.PruneResult{Logs: 3, Daily: 7})
	})}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	m := model{ctx: context.Background(), client: ipc.NewClient(path), workspace: workspaceSettings, settingsSection: settingsStatistics}
	for i, field := range m.settingFields() {
		if field.id == settingStatsPrune {
			m.cursor = i
			if field.label != "Prune" || !strings.Contains(field.value, "30 days") {
				t.Fatal("missing Prune day input")
			}
		}
	}
	m.key(key("enter"))
	if !m.stats.prune || m.workspace != workspaceSettings || m.stats.pruneDays != "30" {
		t.Fatal("prune did not open in Settings")
	}
	m.stats.pruneDays = "14"
	m.key(key("d")) // Logs only.
	before := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -14)
	cmd := m.key(key("enter"))
	if cmd == nil || !m.stats.prunePending || m.stats.pruneConfirm || m.key(key("enter")) != nil {
		t.Fatal("first Enter did not start exactly one preview")
	}
	msg := cmd().(statsPruneMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	updated, _ := m.Update(msg)
	m = updated.(model)
	preview := <-requests
	after := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -14)
	if preview.path != "/v1/stats/prune/preview" || !preview.body.Logs || preview.body.Daily || (preview.body.Cutoff != before && preview.body.Cutoff != after) {
		t.Fatalf("wrong preview scope: %+v", preview)
	}
	if !m.stats.pruneConfirm || m.stats.prunePending {
		t.Fatal("preview did not require confirmation")
	}
	view := m.renderSettings(80, 12)
	for _, want := range []string{"ALL ACCOUNTS", "3", "7", "Enter again", preview.body.Cutoff.Format(time.DateOnly)} {
		if !strings.Contains(view, want) {
			t.Fatalf("confirmation missing %q", want)
		}
	}
	for _, width := range []int{1, 12, 40, 80} {
		for _, line := range strings.Split(m.renderSettings(width, 8), "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatalf("prune overflow at %d", width)
			}
		}
	}
	cmd = m.key(key("enter"))
	if cmd == nil || m.key(key("enter")) != nil {
		t.Fatal("second Enter did not start exactly one deletion")
	}
	m.key(key("esc"))
	if !m.stats.prune {
		t.Fatal("claimed to cancel an already submitted deletion")
	}
	msg = cmd().(statsPruneMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	updated, _ = m.Update(msg)
	m = updated.(model)
	apply := <-requests
	if apply.path != "/v1/stats/prune" || apply.body != preview.body || m.stats.prune || !strings.Contains(m.notice, "Pruned") {
		t.Fatalf("confirmation changed scope or did not complete: %+v", apply)
	}
}

func TestStatsPruneValidationAndStalePreview(t *testing.T) {
	m := model{workspace: workspaceStats}
	m.openStatsPrune()
	for _, value := range []string{"", "0", "-1", "1.5", "days", "9999999999999999999999999", "100000"} {
		m.stats.pruneDays = value
		if m.key(key("enter")) != nil || m.stats.prunePending || m.stats.err == "" {
			t.Fatalf("invalid day count accepted: %q", value)
		}
	}
	m.stats.pruneDays = "30"
	m.key(key("l"))
	m.key(key("d"))
	if m.key(key("enter")) != nil || !strings.Contains(m.stats.err, "Select") {
		t.Fatal("empty prune scope accepted")
	}
	m.key(key("l"))
	m.key(key("enter"))
	old := m.stats.pruneGeneration
	m.key(key("esc"))
	m.openStatsPrune()
	m.key(key("enter"))
	updated, _ := m.Update(statsPruneMsg{request: old, result: stats.PruneResult{Logs: 99}})
	m = updated.(model)
	if m.stats.pruneConfirm || !m.stats.prunePending {
		t.Fatal("cancelled preview changed the new request")
	}
	updated, _ = m.Update(statsPruneMsg{request: m.stats.pruneGeneration, err: errors.New("preview failed")})
	m = updated.(model)
	if m.stats.pruneConfirm || m.stats.prunePending || m.stats.err != "preview failed" {
		t.Fatal("failed preview allowed deletion")
	}
	m.key(key("enter"))
	updated, _ = m.Update(statsPruneMsg{request: m.stats.pruneGeneration, result: stats.PruneResult{Logs: 4}})
	m = updated.(model)
	m.key(key("enter"))
	updated, _ = m.Update(statsPruneMsg{request: m.stats.pruneGeneration, apply: true, err: errors.New("prune failed")})
	m = updated.(model)
	if m.stats.pruneConfirm || m.stats.prunePending || !m.stats.prune || m.stats.err != "prune failed" {
		t.Fatal("failed deletion did not return to preview input")
	}
}
