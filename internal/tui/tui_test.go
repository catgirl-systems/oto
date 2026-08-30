package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
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
	m.setupField = 0
	m.setupKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	m.setupKey(key("tab"))
	m.setupKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	m.setupKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	if m.setupField != 0 {
		t.Fatal("setup field navigation did not handle arrows and tab")
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

func TestFullScreenLayoutAndEditing(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := model{width: 80, height: 14, workspace: 0, selected: map[int]bool{}}
	view := m.View()
	if !view.AltScreen || !strings.Contains(view.Content, "SEARCH") || !strings.Contains(view.Content, "╭") {
		t.Fatal("main view is not a full-screen workspace")
	}

	m.editing = true
	if cmd := m.key(key("q")); cmd != nil || m.input != "q" || !m.editing {
		t.Fatal("text input was handled as a global shortcut")
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.editing || m.input != "" {
		t.Fatal("escape did not cancel text input")
	}

	if got := formatBytes(1536); got != "1.5 KiB" {
		t.Fatalf("formatBytes(1536) = %q", got)
	}
	if start, end := visibleRange(100, 50, 10); start != 45 || end != 55 {
		t.Fatalf("visibleRange = %d:%d", start, end)
	}
}

func TestErrorRowIsStableAndTracksDaemonState(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := model{width: 80, height: 14, selected: map[int]bool{}}
	cleanLines := strings.Count(m.View().Content, "\n")

	updated, _ := m.Update(statusMsg{snapshot: daemon.Snapshot{Status: daemon.StatusReconnecting, Error: "listen tcp 0.0.0.0:50300: bind: address already in use"}})
	m = updated.(model)
	updated, _ = m.Update(sharesMsg{})
	m = updated.(model)
	if !strings.Contains(m.View().Content, "Error: listen tcp") {
		t.Fatal("daemon error did not persist across unrelated refreshes")
	}
	if got := strings.Count(m.View().Content, "\n"); got != cleanLines {
		t.Fatalf("error shifted layout: clean=%d error=%d", cleanLines, got)
	}

	updated, _ = m.Update(statusMsg{snapshot: daemon.Snapshot{Status: daemon.StatusConnected}})
	m = updated.(model)
	if m.status.err != "" || strings.Contains(m.View().Content, "No errors") {
		t.Fatal("resolved daemon error was not cleared to a blank row")
	}
}

func TestSettingsSidebarEditsAccountWithoutLeakingPassword(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "alice", "secret"
	m := newModel(context.Background(), nil, "", false, cfg)
	m.width, m.height, m.workspace = 80, 10, 4

	view := m.View().Content
	if !strings.Contains(view, "Settings") || !strings.Contains(view, "Account") || strings.Contains(view, "secret") || !strings.Contains(view, "••••••") {
		t.Fatal("settings account sidebar is missing or exposed the password")
	}
	settingsLines := strings.Count(view, "\n")
	m.workspace = 0
	searchLines := strings.Count(m.View().Content, "\n")
	m.workspace = 4
	if settingsLines != searchLines {
		t.Fatalf("settings shifted layout: search=%d settings=%d", searchLines, settingsLines)
	}
	m.key(key("enter"))
	m.input = "bob"
	m.editKey(key("enter"))
	if m.cfg.Soulseek.Username != "bob" {
		t.Fatal("account username was not edited")
	}

	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	if m.settingsSection != 1 || !strings.Contains(m.View().Content, "Listen address") {
		t.Fatal("settings sidebar did not navigate to connection settings")
	}
	m.key(key("tab"))
	if m.workspace != 0 {
		t.Fatal("settings tab did not wrap to search")
	}
}

func TestSearchFilterEditingAndMetadata(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := model{width: 100, height: 16, workspace: 0, selected: map[int]bool{}}
	m.key(key("f"))
	if !m.editing || !m.filterEditing {
		t.Fatal("f did not open filter editing")
	}
	m.input = `type:audio bitrate:!0`
	if cmd := m.editKey(key("enter")); cmd != nil || m.searchFilter != `type:audio bitrate:!0` {
		t.Fatal("filter was not applied without a cached search")
	}
	m.key(key("f"))
	m.input = "size:nope"
	m.editKey(key("enter"))
	if m.searchFilter != `type:audio bitrate:!0` || m.err == "" {
		t.Fatal("invalid filter replaced the active filter")
	}
	m.key(key("f"))
	m.input = "public:true"
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.searchFilter != `type:audio bitrate:!0` {
		t.Fatal("escape changed the active filter")
	}
	m.key(key("c"))
	if m.searchFilter != "" || m.searchFilterUndo == "" {
		t.Fatal("c did not clear and remember the filter")
	}
	m.key(key("c"))
	if m.searchFilter != `type:audio bitrate:!0` || m.searchFilterUndo != "" {
		t.Fatal("c did not restore the filter")
	}

	m.loading = false
	m.searchTotal, m.searchFound = 1, 4
	m.results = []result{{user: "peer", path: "song.flac", size: 1024, bitrate: 320, duration: 125, vbr: true}}
	view := m.renderSearch(100, 10)
	for _, want := range []string{"1 loaded / 1 filtered / 4 found", "320kv", "2:05", "private"} {
		if !strings.Contains(view, want) {
			t.Fatalf("search metadata missing %q in %q", want, view)
		}
	}
	updated, _ := m.Update(searchMsg{append: true, page: daemon.SearchPage{ID: "s", Results: []daemon.SearchResult{{Path: "next.flac", Public: true}}, Total: 2, FoundTotal: 4}})
	m = updated.(model)
	if len(m.results) != 2 || m.searchTotal != 2 || m.searchFound != 4 {
		t.Fatal("filtered page was not appended with its totals")
	}
}
