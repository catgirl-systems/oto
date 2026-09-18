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
	failIf(t, e == nil || e.busy || e.err != "" || e.values[0] != "off" || e.values[3] != "off", e)
	press := func(code rune) { drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: code}))) }
	press(tea.KeyRight)
	press(tea.KeyTab)
	e.values[1] = ""
	e.cursor = 0
	m.pasteReceivingSettings(`["Alice","猫"]`)
	before := e.values[1]
	m.pasteReceivingSettings("\ns")
	failIf(t, e.values[1] != before || e.err == "", "unsafe paste accepted")
	press(tea.KeyEnter)
	failIf(t, e.dialog != "save" || e.confirm, "missing default-cancel")
	press(tea.KeyEnter)
	server, err := m.client.ReceivingSettings(context.Background(), e.loaded.CommunityIdentity)
	failIf(t, err != nil || server.Settings.Mode != "", "implicit receiving enabled", err)
	press(tea.KeyEnter)
	press(tea.KeyRight)
	press(tea.KeyEnter)
	failIf(t, e.dirty || e.err != "" || e.loaded.Settings.Mode != "users" || len(e.loaded.Settings.Users) != 2 || e.loaded.Settings.CompletionHooks, "save failed", e)
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}, {10, 3}} {
		m.width, m.height = size[0], size[1]
		e.dialog = "save"
		e.confirm = false
		view := m.receivingSettingsView()
		failIf(t, lipgloss.Width(view) > m.width || lipgloss.Height(view) > m.height, size, view)
		failIf(t, m.width >= 20 && (!strings.Contains(view, "[Cancel]") || !strings.Contains(view, "Command hooks: off")), "hidden consent", view)
	}
	e.dialog = ""
	e.dirty = true
	m.width, m.height = 80, 24
	press(tea.KeyEscape)
	press(tea.KeyEnter)
	failIf(t, m.receivingEditor == nil, "discarded without confirmation")
	m.closeReceivingSettings()
	drainChat(t, &m, m.openReceivingSettings())
	current := m.receivingEditor
	m.applyReceivingSettings(receivingSettingsMsg{owner: e, value: e.loaded})
	failIf(t, m.receivingEditor != current, "stale owner applied")
	m.community.summary.Session++
	press(tea.KeyRight)
	failIf(t, !strings.Contains(current.err, "Session changed"), "stale editor active")
}
