package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestAwayEditorSaveCancelAndStaleResponses(t *testing.T) {
	m, _ := privateChatModel(t)
	press := func(code rune) { drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: code}))) }
	drainChat(t, &m, m.openAwaySettings())
	e := m.awayEditor
	if e == nil || e.busy || e.err != "" {
		t.Fatal(e)
	}
	m.pasteAwaySettings("60")
	press(tea.KeyTab)
	m.pasteAwaySettings("back\nlater")
	press(tea.KeyEnter)
	if e.confirm || e.dialog != "save" {
		t.Fatal("unsafe confirmation")
	}
	press(tea.KeyEnter)
	if !e.dirty || e.loaded.Settings.AutoAwaySeconds != 0 {
		t.Fatal("default saved")
	}
	press(tea.KeyEnter)
	press(tea.KeyRight)
	press(tea.KeyEnter)
	if e.err != "" || e.dirty || e.loaded.Settings.AutoAwaySeconds != 60 || e.loaded.Settings.AutoReply != "back later" {
		t.Fatal(e)
	}
	m.pasteAwaySettings(strings.Repeat("x", 1025))
	if e.form.values[1] != "back later" || e.err == "" {
		t.Fatal("oversized paste accepted")
	}
	old := awaySettingsMsg{owner: e, value: e.loaded}
	drainChat(t, &m, m.openAwaySettings())
	current := m.awayEditor
	old.value.Settings.AutoReply = "stale"
	m.applyAwaySettings(old)
	if current.loaded.Settings.AutoReply == "stale" {
		t.Fatal("stale response applied")
	}
	m.community.summary.Session++
	m.pasteAwaySettings("99")
	if current.form.values[0] != "60" {
		t.Fatal("stale paste")
	}
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}, {10, 3}} {
		m.width, m.height = size[0], size[1]
		for _, dialog := range []string{"", "save", "close"} {
			current.dialog = dialog
			v := m.awaySettingsView()
			if lipgloss.Width(v) > m.width || lipgloss.Height(v) > m.height {
				t.Fatal(size, v)
			}
		}
	}
}
