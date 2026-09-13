package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestTextToolsEditorWorkflowAndFences(t *testing.T) {
	m, _ := privateChatModel(t)
	press := func(code rune) { drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: code}))) }
	text := func(value string) { drainChat(t, &m, m.key(chatPress(value))) }
	drainChat(t, &m, m.openTextTools())
	e := m.textTools
	if e == nil || e.busy || e.err != "" {
		t.Fatal(e)
	}
	text("n")
	m.pasteTextTools("keyword 世界")
	press(tea.KeyEnter)
	press(tea.KeyTab)
	text("n")
	text("foo")
	press(tea.KeyTab)
	text("bar")
	press(tea.KeyEnter)
	press(tea.KeyTab)
	text("n")
	text("bad*")
	press(tea.KeyEnter)
	press(tea.KeyTab)
	press(tea.KeyEnter)
	if !e.dirty || !e.value.Settings.CTCPVersion || len(e.value.Settings.Substitutions) != 1 || m.chatDraftCount() != 1 {
		t.Fatal(e)
	}
	text("s")
	press(tea.KeyEnter)
	if !e.dirty || e.busy {
		t.Fatal("default cancel saved")
	}
	text("s")
	press(tea.KeyRight)
	press(tea.KeyEnter)
	if e.dirty || e.err != "" {
		t.Fatal("save", e.err)
	}
	drainChat(t, &m, m.openTextTools())
	e = m.textTools
	if !e.value.Settings.CTCPVersion || e.value.Settings.Keywords[0] != "keyword 世界" {
		t.Fatal("reload", e)
	}
	text("d")
	press(tea.KeyEnter)
	if len(e.value.Settings.Keywords) != 1 {
		t.Fatal("default delete")
	}
	text("n")
	m.pasteTextTools(strings.Repeat("x", 1025))
	if e.form.values[0] != "" || e.err == "" {
		t.Fatal("oversized paste accepted")
	}
	press(tea.KeyEscape)
	old := textToolsMsg{owner: e, value: e.value}
	drainChat(t, &m, m.openTextTools())
	current := m.textTools
	old.value.Settings.Keywords = []string{"stale"}
	m.applyTextTools(old)
	if current.value.Settings.Keywords[0] == "stale" {
		t.Fatal("old editor response applied")
	}
	m.community.summary.Session++
	text("n")
	if current.form != nil || !strings.Contains(m.textToolsView(), "Session changed") {
		t.Fatal("session fence")
	}
}
func TestTextToolsLayouts(t *testing.T) {
	m, _ := privateChatModel(t)
	drainChat(t, &m, m.openTextTools())
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}, {10, 3}} {
		m.width, m.height = size[0], size[1]
		for _, dialog := range []string{"", "save", "remove", "close"} {
			m.textTools.dialog = dialog
			view := m.textToolsView()
			if lipgloss.Width(view) > m.width || lipgloss.Height(view) > m.height {
				t.Fatal(size, view)
			}
		}
	}
}
