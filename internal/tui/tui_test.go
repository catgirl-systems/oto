package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/slsk-tui/internal/config"
)

func key(s string) tea.KeyPressMsg {
	if s == "enter" {
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	}
	if s == "tab" {
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyTab})
	}
	return tea.KeyPressMsg(tea.Key{Text: s, Code: []rune(s)[0]})
}
func TestNavigationSelectionAndHelp(t *testing.T) {
	m := model{selected: map[int]bool{}, width: 20}
	x, _ := m.Update(key("tab"))
	m = x.(model)
	if m.workspace != 1 {
		t.Fatal("tab")
	}
	m.results = []result{{path: "a", size: 1}}
	m.workspace = 0
	x, _ = m.Update(key(" "))
	m = x.(model)
	if !m.selected[0] {
		t.Fatal("space")
	}
	x, _ = m.Update(key("?"))
	m = x.(model)
	if !m.help {
		t.Fatal("help")
	}
	if strings.Contains(m.View().Content, "panic") {
		t.Fatal("view")
	}
}

func TestPasswordPaste(t *testing.T) {
	m := newModel(context.Background(), nil, "", false, config.Default())
	m.setup, m.setupField = true, 1
	updated, _ := m.Update(tea.PasteMsg{Content: "pasted-secret\n"})
	m = updated.(model)
	if m.setupVals[1] != "pasted-secret" || strings.Contains(m.setupView(), "pasted-secret") {
		t.Fatal("password paste was not accepted and masked")
	}
}
func TestSetupMasksAndValidates(t *testing.T) {
	m := newModel(context.Background(), nil, "", false, config.Default())
	m.setup = true
	m.setupVals[0] = "u"
	m.setupVals[1] = "secret"
	if !strings.Contains(m.setupView(), "••••••") {
		t.Fatal("password not masked")
	}
	m.setupField = 4
	m.setupVals[0] = ""
	m.setupKey(key("enter"))
	if m.setupErr == "" {
		t.Fatal("missing credential accepted")
	}
}
func TestNarrowViewAndQuitConfirmation(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := model{width: 3, workspace: 0, selected: map[int]bool{}, transient: true, transfers: []transfer{{state: "running"}}}
	x, _ := m.Update(key("q"))
	m = x.(model)
	if !m.confirm {
		t.Fatal("quit was not confirmed")
	}
	if strings.Contains(m.View().Content, "\x1b") {
		t.Fatal("unexpected color")
	}
}
