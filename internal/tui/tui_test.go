package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
	m.searchTree, m.cursor = buildSearchTree(m.results, treeState{}, 0)
	m.cursor = m.searchTree.cursorForSource(0)
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

func TestTextFieldsEditAtCursor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := model{editing: true, input: "aébc", inputCursor: 4, selected: map[int]bool{}}
	m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	m.editKey(key("X"))
	if m.input != "aéXbc" || m.inputCursor != 3 {
		t.Fatalf("insert at cursor = %q @ %d", m.input, m.inputCursor)
	}
	m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDelete}))
	if m.input != "aéc" || m.inputCursor != 2 {
		t.Fatalf("delete at cursor = %q @ %d", m.input, m.inputCursor)
	}
	m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}))
	m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	updated, _ := m.Update(tea.PasteMsg{Content: "Ω"})
	m = updated.(model)
	if m.input != "aΩéc" || m.inputCursor != 2 || !strings.Contains(renderInput("", m.input, m.inputCursor, false, lipgloss.NewStyle()), "aΩ█éc") {
		t.Fatalf("paste/caret at cursor = %q @ %d", m.input, m.inputCursor)
	}

	setup := model{setup: true, setupVals: [5]string{"abcd"}, inputCursor: 4}
	setup.setupKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	setup.setupKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	setup.setupKey(key("X"))
	if setup.setupVals[0] != "abXcd" || setup.inputCursor != 3 {
		t.Fatalf("setup cursor edit = %q @ %d", setup.setupVals[0], setup.inputCursor)
	}
	masked := renderInput("", "secret", 2, true, lipgloss.NewStyle())
	if strings.Contains(masked, "secret") || !strings.Contains(masked, "••█••••") {
		t.Fatalf("masked cursor rendering = %q", masked)
	}
}

func TestTextFieldWordEditing(t *testing.T) {
	ctrl := func(code rune) tea.KeyPressMsg {
		return tea.KeyPressMsg(tea.Key{Code: code, Mod: tea.ModCtrl})
	}
	m := model{editing: true, input: "alpha beta gamma", inputCursor: 16}
	m.editKey(ctrl(tea.KeyLeft))
	if m.inputCursor != 11 {
		t.Fatalf("ctrl+left cursor = %d", m.inputCursor)
	}
	m.editKey(ctrl(tea.KeyBackspace))
	if m.input != "alpha gamma" || m.inputCursor != 6 {
		t.Fatalf("ctrl+backspace = %q @ %d", m.input, m.inputCursor)
	}
	m.editKey(ctrl(tea.KeyDelete))
	if m.input != "alpha " || m.inputCursor != 6 {
		t.Fatalf("ctrl+delete = %q @ %d", m.input, m.inputCursor)
	}
	m.editKey(ctrl('u'))
	if m.input != "" || m.inputCursor != 0 {
		t.Fatalf("ctrl+u = %q @ %d", m.input, m.inputCursor)
	}

	setup := model{setup: true, setupVals: [5]string{"one two"}, inputCursor: 7}
	setup.setupKey(ctrl(tea.KeyLeft))
	setup.setupKey(ctrl(tea.KeyBackspace))
	if setup.setupVals[0] != "two" || setup.inputCursor != 0 {
		t.Fatalf("setup word edit = %q @ %d", setup.setupVals[0], setup.inputCursor)
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
	if !strings.Contains(m.renderSearch(100, 10), "tab complete: in:") {
		t.Fatal("filter keywords were not shown")
	}
	m.editKey(key("tab"))
	if m.input != "in:" || m.workspace != 0 {
		t.Fatal("tab did not complete the first filter field")
	}
	m.editKey(key("tab"))
	if m.input != "out:" {
		t.Fatal("repeated tab did not cycle filter fields")
	}
	m.input = "ty"
	m.editKey(key("tab"))
	m.editKey(key("tab"))
	if m.input != "type:audio" {
		t.Fatalf("type completion = %q", m.input)
	}
	m.input = "type:!a"
	m.editKey(key("tab"))
	if m.input != "type:!audio" {
		t.Fatalf("excluded type completion = %q", m.input)
	}
	m.input = "free:"
	m.editKey(key("tab"))
	if m.input != "free:true" {
		t.Fatalf("boolean completion = %q", m.input)
	}
	m.input = "size:"
	m.editKey(key("tab"))
	if m.input != "size:>=" {
		t.Fatalf("comparison completion = %q", m.input)
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
	m.results = []result{{user: "peer", path: `music\album\song.flac`, size: 1024, bitrate: 320, duration: 125, vbr: true}}
	m.searchTree, m.cursor = buildSearchTree(m.results, treeState{}, 0)
	m.cursor = m.searchTree.cursorForSource(0)
	view := m.renderSearch(100, 10)
	for _, want := range []string{"1 loaded / 1 filtered / 4 found", "FILE", "SIZE", `SOURCE  peer  •  music\album`, "song.flac", "320kv", "2:05", "private"} {
		if !strings.Contains(view, want) {
			t.Fatalf("search metadata missing %q in %q", want, view)
		}
	}
	for _, line := range strings.Split(view, "\n") {
		if lipgloss.Width(line) > 100 {
			t.Fatalf("search row exceeds width: %d %q", lipgloss.Width(line), line)
		}
	}
	if !strings.Contains(view, "› ○ ·       song.flac") {
		t.Fatalf("filename was not left-aligned: %q", view)
	}
	t.Setenv("NO_COLOR", "")
	if selected := searchResultRow("row", false, true); !strings.Contains(selected, "\x1b[") {
		t.Fatalf("selected result was not highlighted: %q", selected)
	}
	if narrow := m.renderSearch(60, 10); strings.Contains(narrow, "RATE") || !strings.Contains(narrow, "song.flac") {
		t.Fatalf("narrow search columns did not collapse: %q", narrow)
	}
	if wide := m.renderSearch(140, 10); !strings.Contains(wide, "USER") {
		t.Fatalf("wide search columns missing user: %q", wide)
	}
	updated, _ := m.Update(searchMsg{append: true, page: daemon.SearchPage{ID: "s", Results: []daemon.SearchResult{{Path: "next.flac", Public: true}}, Total: 2, FoundTotal: 4}})
	m = updated.(model)
	if len(m.results) != 2 || m.searchTotal != 2 || m.searchFound != 4 {
		t.Fatal("filtered page was not appended with its totals")
	}
}

func TestSearchResultTabs(t *testing.T) {
	m := model{workspace: 0, searchFilter: "type:audio", selected: map[int]bool{}}
	if cmd := m.openSearch("first query"); cmd == nil {
		t.Fatal("first search tab did not start")
	}
	firstRequest, firstOperation := m.searchTabs[0].request, m.searchTabs[0].operation
	m.openSearch("second query")
	secondRequest, secondOperation := m.searchTabs[1].request, m.searchTabs[1].operation
	m.searchFilter = "free:true"
	if len(m.searchTabs) != 2 || !strings.Contains(m.renderSearch(100, 10), "first query") {
		t.Fatalf("search tabs not rendered: tabs=%d", len(m.searchTabs))
	}

	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModCtrl}))
	if m.query != "first query" || m.searchFilter != "type:audio" {
		t.Fatalf("first search tab state = %q %q", m.query, m.searchFilter)
	}
	updated, _ := m.Update(searchMsg{request: secondRequest, operation: secondOperation, filter: "free:true", page: daemon.SearchPage{ID: "second", Query: "second query", Results: []daemon.SearchResult{{Path: "second.flac", Public: true}}, Total: 1, FoundTotal: 2}})
	m = updated.(model)
	if m.query != "first query" || len(m.results) != 0 {
		t.Fatalf("background search switched active tab: query=%q results=%d", m.query, len(m.results))
	}
	updated, _ = m.Update(searchMsg{request: firstRequest, operation: firstOperation, filter: "type:audio", page: daemon.SearchPage{ID: "first", Query: "first query", Results: []daemon.SearchResult{{Path: "first.flac", Public: true}}, Total: 1, FoundTotal: 3}})
	m = updated.(model)
	m.selected[0] = true
	firstRoot := m.searchTree.nodes[m.searchTree.roots[0]].id
	m.searchTree.expanded[firstRoot] = false
	m.searchTree.rebuildVisible()
	m.cursor = 0

	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown, Mod: tea.ModCtrl}))
	if m.query != "second query" || m.searchFilter != "free:true" || len(m.results) != 1 || m.results[0].path != "second.flac" {
		t.Fatalf("second search tab state = %q %q %+v", m.query, m.searchFilter, m.results)
	}
	oldOperation := m.searchTabs[m.searchTabIndex].operation
	m.loadingMore = true
	m.filterSearch("public:true")
	updated, _ = m.Update(searchMsg{request: secondRequest, operation: oldOperation, append: true, page: daemon.SearchPage{ID: "second", Results: []daemon.SearchResult{{Path: "stale.flac", Public: true}}}})
	m = updated.(model)
	if len(m.results) != 1 || m.results[0].path != "second.flac" || m.loadingMore {
		t.Fatalf("stale pagination changed filtered tab: results=%+v loadingMore=%v", m.results, m.loadingMore)
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModCtrl}))
	if m.query != "first query" || !m.selected[0] || m.searchTree.expandedNode(m.searchTree.nodes[m.searchTree.roots[0]]) {
		t.Fatalf("first search selection was not restored: query=%q selected=%v", m.query, m.selected)
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: 'w', Mod: tea.ModCtrl}))
	if len(m.searchTabs) != 1 || m.query != "second query" {
		t.Fatalf("ctrl+w did not close search tab: query=%q tabs=%d", m.query, len(m.searchTabs))
	}
}

func TestFilterEnteredDuringSearchAppliesAfterCompletion(t *testing.T) {
	m := model{
		workspace: 0, searchTabIndex: 0, searchOperation: 1,
		query: "avi8", searchFilter: "in:outta", loading: true, selected: map[int]bool{},
		searchTabs: []searchTab{{query: "avi8", loading: true, selected: map[int]bool{}, request: 1, operation: 1}},
	}
	updated, cmd := m.Update(searchMsg{
		request: 1, operation: 1,
		page: daemon.SearchPage{ID: "cached", FoundTotal: 12, Results: []daemon.SearchResult{{Path: "unfiltered.flac"}}},
	})
	m = updated.(model)
	if cmd == nil || m.searchID != "cached" || m.searchFilter != "in:outta" || !m.loading || len(m.results) != 0 {
		t.Fatalf("pending filter was not scheduled: id=%q filter=%q loading=%v results=%v", m.searchID, m.searchFilter, m.loading, m.results)
	}

	operation := m.searchTabs[0].operation
	updated, _ = m.Update(searchMsg{
		request: 1, operation: operation, filter: "in:outta", filterChange: true,
		page: daemon.SearchPage{ID: "cached", Total: 1, FoundTotal: 12, Results: []daemon.SearchResult{{Path: "outta.flac"}}},
	})
	m = updated.(model)
	if m.loading || len(m.results) != 1 || m.results[0].path != "outta.flac" || m.searchFilter != "in:outta" {
		t.Fatalf("pending filter result was not applied: loading=%v filter=%q results=%v", m.loading, m.searchFilter, m.results)
	}
}

func TestTransferDirectionTabsProgressAndSpinner(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := model{workspace: 2, selected: map[int]bool{}, transfers: []transfer{
		{id: "d1", filename: "album.flac", direction: "download", state: "running", done: 25, total: 100, speed: 1536, user: "alice"},
		{id: "d2", filename: `folder\queued.mp3`, direction: "download", state: "queued", total: 100, queue: 2, user: "alice"},
		{id: "u1", filename: "shared.wav", direction: "upload", state: "completed", done: 100, total: 100, user: "bob"},
	}}
	if got := m.workspaceNames()[2]; got != "Transfers 1↓ 0↑" {
		t.Fatalf("transfer activity tab = %q", got)
	}
	m.transferTrees[0], m.transferCursors[0] = buildTransferTree(m.transfers, "download", treeState{}, 0)
	m.transferTrees[1], m.transferCursors[1] = buildTransferTree(m.transfers, "upload", treeState{}, 0)
	m.cursor = 0
	ids := m.transferActionIDs()
	if len(ids) != 2 || (ids[0] != "d1" && ids[1] != "d1") || (ids[0] != "d2" && ids[1] != "d2") {
		t.Fatalf("recursive transfer action IDs = %v", ids)
	}
	m.cursor = m.transferTrees[0].cursorForSource(0)

	downloads := m.renderTransfers(100, 10)
	if !strings.Contains(downloads, "[↓ DOWNLOADS 2]") || !strings.Contains(downloads, "███░░░░░░░░░░░  25%") || !strings.Contains(downloads, "1.5 KiB/s  ETA 0:01") || !strings.Contains(downloads, "⠋") || strings.Contains(downloads, "shared.wav") {
		t.Fatalf("download tab did not render progress and spinner correctly: %q", downloads)
	}
	for _, width := range []int{40, 100} {
		for _, line := range strings.Split(m.renderTransfers(width, 10), "\n") {
			if lipgloss.Width(line) > width {
				t.Fatalf("transfer tree exceeds width %d: %q", width, line)
			}
		}
	}
	m.transfers[1].state, m.transfers[1].err = "failed", "File not shared."
	if failed := m.renderTransfers(140, 10); !strings.Contains(failed, "failed: File not shared.") {
		t.Fatalf("transfer error was not rendered: %q", failed)
	}
	m.transfers[1].state, m.transfers[1].err = "queued", ""
	downloadCursor := m.transferTrees[0].cursorForSource(1)
	m.cursor = downloadCursor
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown, Mod: tea.ModCtrl}))
	uploads := m.renderTransfers(100, 10)
	if m.rows() != 2 || !strings.Contains(uploads, "[↑ UPLOADS 1]") || !strings.Contains(uploads, "shared.wav") || strings.Contains(uploads, "album.flac") {
		t.Fatalf("upload tab did not isolate uploads: rows=%d view=%q", m.rows(), uploads)
	}
	if _, node := m.transferTrees[1].node(m.transferTrees[1].cursorForSource(2)); node == nil || node.source != 2 {
		t.Fatal("upload tree did not map to source transfer")
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModCtrl}))
	if m.cursor != downloadCursor {
		t.Fatalf("download cursor was not restored: %d", m.cursor)
	}
	updated, _ := m.Update(spinnerTickMsg{})
	if next := updated.(model).renderTransfers(100, 10); !strings.Contains(next, "⠙") {
		t.Fatalf("spinner did not advance: %q", next)
	}

	next := []transfer{{id: "d1", state: "running", done: 1049}, {id: "d2", state: "failed", done: 50}}
	setTransferSpeeds(next, []transfer{{id: "d1", done: 25}, {id: "d2", done: 25}}, time.Second)
	if next[0].speed != 1024 || next[1].speed != 0 {
		t.Fatalf("transfer speeds = %d, %d", next[0].speed, next[1].speed)
	}

	t.Setenv("NO_COLOR", "")
	normal := transferResultRow("row", false, false, false)
	failedDownload := transferResultRow("row", false, false, true)
	failedUpload := transferResultRow("row", false, true, true)
	if normal == failedDownload || failedDownload != failedUpload {
		t.Fatalf("failed transfer color was not distinct: normal=%q download=%q upload=%q", normal, failedDownload, failedUpload)
	}
}

func TestFileDetails(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := model{workspace: 0, width: 100, height: 30, selected: map[int]bool{}, results: []result{{
		user: "alice", path: `Music\song.flac`, extension: "flac", size: 1536, free: true,
		bitrate: 320, duration: 125, vbr: true, sampleRate: 44100, bitDepth: 24, public: true,
	}}}
	m.searchTree, m.cursor = buildSearchTree(m.results, treeState{}, 0)
	m.cursor = m.searchTree.cursorForSource(0)
	m.key(key("i"))
	view := m.View().Content
	for _, want := range []string{"File details", `Music\song.flac`, "alice", "1.5 KiB", "public", "flac", "320 kbps VBR", "2:05", "44100 Hz", "24-bit", "free slot"} {
		if !strings.Contains(view, want) {
			t.Fatalf("file details missing %q: %q", want, view)
		}
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.details {
		t.Fatal("escape did not close file details")
	}

	m.workspace, m.browseUser = 1, "bob"
	m.entries = []entry{{name: `Private\demo.wav`, extension: "wav", size: 42, private: true, bitrate: 1411}}
	m.browseTree, m.cursor = buildBrowseTree(m.entries, treeState{}, 0)
	m.cursor = m.browseTree.cursorForSource(0)
	m.key(key("i"))
	view = m.View().Content
	for _, want := range []string{"bob", "private", "1411 kbps CBR"} {
		if !strings.Contains(view, want) {
			t.Fatalf("browse details missing %q: %q", want, view)
		}
	}
}

func TestBrowseResultFolderAndUserTabs(t *testing.T) {
	m := model{
		workspace: 0,
		results:   []result{{user: "nss", path: `audio\Hardstyle_320\song.mp3`}},
		selected:  map[int]bool{},
	}
	m.searchTree, m.cursor = buildSearchTree(m.results, treeState{}, 0)
	m.cursor = m.searchTree.cursorForSource(0)
	if cmd := m.key(key("b")); cmd == nil || m.workspace != 1 || m.browseUser != "nss" || len(m.browseTabs) != 1 {
		t.Fatalf("browse result did not open user tab: workspace=%d user=%q tabs=%d", m.workspace, m.browseUser, len(m.browseTabs))
	}
	updated, _ := m.Update(browseMsg{user: "nss", request: m.browseTabs[0].request, entries: []entry{
		{name: "audio", directory: true},
		{name: `audio\Hardstyle_320`, directory: true},
		{name: `audio\Hardstyle_320\song.mp3`, private: true, bitrate: 320, duration: 125, vbr: true},
	}})
	m = updated.(model)
	if m.cursor != 1 {
		t.Fatalf("browse folder cursor = %d", m.cursor)
	}
	view := m.renderBrowse(100, 10)
	for _, want := range []string{"FOLDER  audio\\Hardstyle_320", "FILE", "SIZE", "RATE", "TIME", "STATUS", "song.mp3", "320kv", "2:05", "private"} {
		if !strings.Contains(view, want) {
			t.Fatalf("browse result UI missing %q in %q", want, view)
		}
	}
	m.selected[2] = true
	folderID := treeID("browse-dir", `audio\Hardstyle_320`)
	m.browseTree.expanded[folderID] = false
	m.browseTree.rebuildVisible()
	m.openBrowse("LittleDeng", "", false)
	if len(m.browseTabs) != 2 || m.browseUser != "LittleDeng" || !strings.Contains(m.renderBrowse(100, 10), "nss") {
		t.Fatalf("second user tab not retained: user=%q tabs=%d", m.browseUser, len(m.browseTabs))
	}
	staleRequest := m.browseTabs[1].request
	m.openBrowse("LittleDeng", "", true)
	updated, _ = m.Update(browseMsg{user: "LittleDeng", request: staleRequest, entries: []entry{{name: "stale"}}})
	m = updated.(model)
	if len(m.entries) != 0 || !m.loading {
		t.Fatalf("stale browse response replaced newer request: entries=%d loading=%v", len(m.entries), m.loading)
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModCtrl}))
	if m.browseUser != "nss" || m.cursor != 1 || !m.selected[2] || m.browseTree.expandedNode(m.browseTree.nodes[m.browseTree.byID[folderID]]) {
		t.Fatalf("user tab state was not restored: user=%q cursor=%d", m.browseUser, m.cursor)
	}
	updated, _ = m.Update(browseMsg{user: "LittleDeng", request: m.browseTabs[1].request, entries: []entry{{name: "EDM", directory: true}}})
	m = updated.(model)
	if m.browseUser != "nss" {
		t.Fatalf("background browse response switched tabs to %q", m.browseUser)
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown, Mod: tea.ModCtrl}))
	if m.browseUser != "LittleDeng" || len(m.entries) != 1 {
		t.Fatalf("background user tab was not populated: user=%q entries=%d", m.browseUser, len(m.entries))
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModCtrl}))
	m.key(tea.KeyPressMsg(tea.Key{Code: 'w', Mod: tea.ModCtrl}))
	if len(m.browseTabs) != 1 || m.browseUser != "LittleDeng" {
		t.Fatalf("ctrl+w did not close active user tab: user=%q tabs=%d", m.browseUser, len(m.browseTabs))
	}
}

func TestFilterCompletionCyclesBackward(t *testing.T) {
	if got := completeFilter("", true); got != "public:" {
		t.Fatalf("backward field completion = %q", got)
	}
	if got := completeFilter("type:a", true); got != "type:archive" {
		t.Fatalf("backward type completion = %q", got)
	}
	m := model{workspace: 0, editing: true, filterEditing: true}
	m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	if m.input != "public:" || m.workspace != 0 {
		t.Fatalf("shift+tab completion = %q in workspace %d", m.input, m.workspace)
	}
}

func TestTreeNavigationGroupingAndRecursiveSelection(t *testing.T) {
	results := []result{
		{user: "peer", path: `music/album/a.flac`},
		{user: "peer", path: `music\album\b.flac`},
		{user: "other", path: `music\album\a.flac`},
		{user: "peer", path: `music\album\a.flac`},
	}
	tree, _ := buildSearchTree(results, treeState{}, 0)
	if tree.nodes[tree.roots[0]].label != "peer" || len(tree.visible) != 10 {
		t.Fatalf("search tree grouping/order: roots=%v visible=%d", tree.roots, len(tree.visible))
	}
	albumID := treeID("search-dir", "peer", `music\album`)
	albumIndex := tree.byID[albumID]
	albumCursor := 0
	for i, index := range tree.visible {
		if index == albumIndex {
			albumCursor = i
		}
	}
	m := model{workspace: 0, results: results, searchTree: tree, cursor: albumCursor, selected: map[int]bool{}}
	m.toggle()
	if !m.selected[0] || !m.selected[1] || !m.selected[3] || m.selected[2] || treeSelection(&m.searchTree, albumIndex, m.selected) != "●" {
		t.Fatalf("recursive folder selection = %v", m.selected)
	}
	m.selected[1] = false
	if treeSelection(&m.searchTree, albumIndex, m.selected) != "◐" {
		t.Fatal("partial folder selection was not shown")
	}
	m.cursor = 0
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	if m.workspace != 0 || len(m.searchTree.visible) >= len(tree.visible) {
		t.Fatal("left did not collapse the tree in place")
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	if m.cursor != 1 {
		t.Fatalf("right did not enter first child: %d", m.cursor)
	}

	m.cursor = m.searchTree.cursorForSource(0)
	oldID := m.searchTree.cursorID(m.cursor)
	updated, updatedCursor := buildSearchTree(append(results, result{user: "peer", path: `music\new\c.flac`}), m.searchTree, m.cursor)
	if updated.cursorID(updatedCursor) != oldID {
		t.Fatal("pagination did not preserve the cursor by node identity")
	}
	updated.expanded[albumID] = false
	updated, _ = buildSearchTree(append(results, result{user: "peer", path: `music\other\d.flac`}), updated, updatedCursor)
	if updated.expanded[albumID] {
		t.Fatal("pagination discarded an explicit collapsed state")
	}
	if _, ok := updated.byID[oldID]; !ok {
		t.Fatal("pagination lost the stable result identity")
	}
}

func TestSharesTreeIgnoresStaleBrowseResponses(t *testing.T) {
	m := model{workspace: 3, selected: map[int]bool{}, shares: []share{{name: "Music", path: "/music"}}, shareGeneration: 2}
	m.shareTree, m.cursor = buildShareRoots(m.shares, treeState{}, 0, true)
	root := &m.shareTree.nodes[m.shareTree.roots[0]]
	root.loading, root.request = true, 7
	m.shareTree.expanded[root.id] = true
	stale, _ := m.Update(shareBrowseMsg{nodeID: root.id, generation: 1, request: 7, entries: []entry{{name: "stale.mp3"}}})
	m = stale.(model)
	if len(m.shareTree.nodes) != 1 {
		t.Fatal("stale share response populated the tree")
	}
	current, _ := m.Update(shareBrowseMsg{nodeID: root.id, generation: 2, request: 7, entries: []entry{{name: "Album", directory: true}, {name: "song.flac", size: 42}}})
	m = current.(model)
	if len(m.shareTree.nodes) != 3 || !strings.Contains(m.renderShares(80, 10), "song.flac") {
		t.Fatalf("current share response was not rendered: %q", m.renderShares(80, 10))
	}
	for _, line := range strings.Split(m.renderShares(40, 10), "\n") {
		if lipgloss.Width(line) > 40 {
			t.Fatalf("share tree exceeds width: %q", line)
		}
	}
	polled, _ := m.Update(sharesMsg{shares: m.shares})
	m = polled.(model)
	if len(m.shareTree.nodes) != 3 {
		t.Fatal("ordinary share polling discarded loaded children")
	}
	changed, _ := m.Update(sharesMsg{shares: append(m.shares, share{name: "Other", path: "/other"})})
	m = changed.(model)
	if len(m.shareTree.nodes) != 4 {
		t.Fatal("adding a root discarded an unchanged loaded subtree")
	}
	removed, _ := m.Update(sharesMsg{shares: []share{{name: "Other", path: "/other"}}})
	m = removed.(model)
	if len(m.shareTree.nodes) != 1 || m.shareTree.nodes[m.shareTree.roots[0]].label != "Other" {
		t.Fatal("removed share root remained in the tree")
	}
	reset, _ := m.Update(sharesMsg{shares: m.shares, reset: true})
	m = reset.(model)
	if len(m.shareTree.nodes) != 1 || m.shareTree.expandedNode(m.shareTree.nodes[m.shareTree.roots[0]]) {
		t.Fatal("rescan did not invalidate and collapse share children")
	}
}
