package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestReceivingEditorExplicitConsentAndStaleOwner(t *testing.T) {
	m, _ := privateChatModel(t)
	drainChat(t, &m, m.openReceivingSettings())
	e := m.receivingEditor
	if e == nil || e.busy || e.err != "" || e.values[0] != "off" || e.values[3] != "off" {
		t.Fatal(e)
	}
	press := func(code rune) { drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: code}))) }
	press(tea.KeyRight)
	press(tea.KeyTab)
	e.values[1] = ""
	e.cursor = 0
	m.pasteReceivingSettings(`["Alice","猫"]`)
	before := e.values[1]
	m.pasteReceivingSettings("\ns")
	if e.values[1] != before || e.err == "" {
		t.Fatal("unsafe paste accepted")
	}
	press(tea.KeyEnter)
	if e.dialog != "save" || e.confirm {
		t.Fatal("missing default-cancel")
	}
	press(tea.KeyEnter)
	server, err := m.client.ReceivingSettings(context.Background(), e.loaded.CommunityIdentity)
	if err != nil || server.Settings.Mode != "" {
		t.Fatal("implicit receiving enabled", err)
	}
	press(tea.KeyEnter)
	press(tea.KeyRight)
	press(tea.KeyEnter)
	if e.dirty || e.err != "" || e.loaded.Settings.Mode != "users" || len(e.loaded.Settings.Users) != 2 || e.loaded.Settings.CompletionHooks {
		t.Fatal("save failed", e)
	}
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}, {10, 3}} {
		m.width, m.height = size[0], size[1]
		e.dialog = "save"
		e.confirm = false
		view := m.receivingSettingsView()
		if lipgloss.Width(view) > m.width || lipgloss.Height(view) > m.height {
			t.Fatal(size, view)
		}
		if m.width >= 20 && (!strings.Contains(view, "[Cancel]") || !strings.Contains(view, "Command hooks: off")) {
			t.Fatal("hidden consent", view)
		}
	}
	e.dialog = ""
	e.dirty = true
	m.width, m.height = 80, 24
	press(tea.KeyEscape)
	press(tea.KeyEnter)
	if m.receivingEditor == nil {
		t.Fatal("discarded without confirmation")
	}
	m.closeReceivingSettings()
	drainChat(t, &m, m.openReceivingSettings())
	current := m.receivingEditor
	m.applyReceivingSettings(receivingSettingsMsg{owner: e, value: e.loaded})
	if m.receivingEditor != current {
		t.Fatal("stale owner applied")
	}
	m.community.summary.Session++
	press(tea.KeyRight)
	if !strings.Contains(current.err, "Session changed") {
		t.Fatal("stale editor active")
	}
}
