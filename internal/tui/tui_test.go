package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestWishlistWorkspaceKeysBadgeAndBell(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := config.Default()
	m := model{workspace: workspaceWishlist, width: 100, height: 24, cfg: cfg, activeSearch: cfg.Search, selected: map[int]bool{}, wishlistNotified: map[string]uint64{}, wishlist: []daemon.WishlistItem{{ID: "w-1", Query: "rare album", Filter: "type:flac", ResultCount: 2, Unread: true, NotificationSequence: 1}}}
	if names := m.workspaceNames(); names[workspaceWishlist] != "Wishlist 2" {
		t.Fatalf("wishlist badge: %v", names)
	}
	view := m.renderWishlist(90, 12)
	for _, want := range []string{"rare album", "type:flac", "2 results"} {
		if !strings.Contains(view, want) {
			t.Fatalf("wishlist view %q missing %q", view, want)
		}
	}
	if cmd := m.key(key("f")); cmd != nil || !m.editing || !m.filterEditing || m.input != "type:flac" {
		t.Fatal("wishlist filter edit did not open")
	}
	m.editing, m.filterEditing = false, false
	for _, action := range []string{"enter", "r", "d"} {
		if cmd := m.key(key(action)); cmd == nil {
			t.Fatalf("wishlist %s returned no command", action)
		}
	}
	m.workspace, m.query, m.searchFilter = workspaceSearch, "rare album", "type:flac"
	if cmd := m.key(key("w")); cmd == nil {
		t.Fatal("search w returned no wishlist command")
	}
	m.workspace = workspaceWishlist
	m.key(key("/"))
	m.input = "another"
	if cmd := m.editKey(key("enter")); cmd == nil {
		t.Fatal("wishlist add returned no command")
	}
	cursorModel := model{workspace: workspaceWishlist, cursor: 1, cfg: cfg, wishlistNotified: map[string]uint64{}, wishlist: []daemon.WishlistItem{{ID: "w-1"}, {ID: "w-2"}}}
	updated, _ := cursorModel.Update(wishlistMsg{items: cursorModel.wishlist})
	if updated.(model).cursor != 1 {
		t.Fatal("wishlist polling reset the cursor")
	}

	m = model{cfg: cfg, wishlistNotified: map[string]uint64{}}
	updated, cmd := m.Update(wishlistMsg{items: []daemon.WishlistItem{{ID: "w-1", Unread: true, ResultCount: 2, NotificationSequence: 1}}})
	m = updated.(model)
	if cmd == nil {
		t.Fatal("new wishlist notification did not ring")
	}
	_, cmd = m.Update(wishlistMsg{items: m.wishlist})
	if cmd != nil {
		t.Fatal("same wishlist notification rang twice")
	}

	m.workspace, m.settingsSection, m.cursor = workspaceSettings, settingsSearch, 7
	m.cfg.Search.WishlistIntervalMinutes = 0
	if !strings.Contains(m.renderSettings(80, 12), "Off") {
		t.Fatal("zero wishlist interval was not shown as Off")
	}
	m.key(key("enter"))
	m.input = "30"
	m.editKey(key("enter"))
	if m.cfg.Search.WishlistIntervalMinutes != 30 {
		t.Fatal("wishlist interval setting was not edited")
	}
	m.cursor = 8
	m.key(key("enter"))
	if m.cfg.Search.WishlistNotifications {
		t.Fatal("wishlist notification setting did not toggle")
	}
}

func TestOnDemandSearchAndBrowseActivityFooter(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := model{width: 80, height: 24, selected: map[int]bool{}}
	if footer := m.footerView(); !strings.Contains(footer, "? all controls") || strings.Contains(footer, "Searching ") || strings.Contains(footer, "q quit") {
		t.Fatalf("idle footer: %q", footer)
	}

	if cmd := m.openSearch("a long query"); cmd == nil {
		t.Fatal("search did not start")
	}
	searchRequest, searchOperation := m.searchTabs[0].request, m.searchTabs[0].operation
	if footer := m.footerView(); !strings.Contains(footer, "Searching a long query") || strings.Contains(footer, "? all controls") {
		t.Fatalf("search footer: %q", footer)
	}
	if compact := m.compactView(); !strings.Contains(compact, "Searching a long query") {
		t.Fatalf("compact activity: %q", compact)
	}
	if animated, cmd := m.Update(activityTickMsg{}); cmd == nil || animated.(model).activityFrame != 1 {
		t.Fatal("search activity tick did not continue")
	} else {
		m = animated.(model)
	}
	updated, _ := m.Update(searchMsg{request: searchRequest, operation: searchOperation})
	m = updated.(model)
	if footer := m.footerView(); !strings.Contains(footer, "? all controls") || strings.Contains(footer, "Searching ") {
		t.Fatalf("completed search footer: %q", footer)
	}
	stopped, cmd := m.Update(activityTickMsg{})
	m = stopped.(model)
	if cmd != nil || m.activityRunning {
		t.Fatal("idle activity tick did not stop")
	}

	filtering := model{width: 80, workspace: workspaceSearch, searchTabIndex: 0, searchTabs: []searchTab{{query: "cached", loading: true, loadingMore: true}}}
	if activity := filtering.activityView(78); activity != "" {
		t.Fatalf("filtering activated footer: %q", activity)
	}

	m = model{width: 80, height: 24, selected: map[int]bool{}}
	if cmd := m.openBrowse("peer", "", false); cmd == nil {
		t.Fatal("browse did not start")
	}
	request := m.browseTabs[0].request
	if footer := m.footerView(); !strings.Contains(footer, "Browsing @peer") || strings.Contains(footer, "%") {
		t.Fatalf("initial browse footer: %q", footer)
	}
	updated, _ = m.Update(browseProgressMsg{user: "peer", request: request + 1, progress: &daemon.BrowseProgress{Received: 75, Total: 100}})
	m = updated.(model)
	if m.browseTabs[0].total != 0 {
		t.Fatal("stale browse progress was applied")
	}
	updated, _ = m.Update(browseProgressMsg{user: "peer", request: request, progress: &daemon.BrowseProgress{Received: 25, Total: 100}})
	m = updated.(model)
	if footer := m.footerView(); !strings.Contains(footer, " 25%") {
		t.Fatalf("determinate browse footer: %q", footer)
	}
	updated, _ = m.Update(browseProgressMsg{user: "peer", request: request, progress: &daemon.BrowseProgress{Received: 100, Total: 100}})
	m = updated.(model)
	if footer := m.footerView(); !strings.Contains(footer, "Finishing @peer") || !strings.Contains(footer, "100%") {
		t.Fatalf("finishing browse footer: %q", footer)
	}
	updated, _ = m.Update(browseMsg{user: "peer", request: request})
	m = updated.(model)
	if footer := m.footerView(); !strings.Contains(footer, "? all controls") || strings.Contains(footer, "Browsing @") {
		t.Fatalf("completed browse footer: %q", footer)
	}
	m.openBrowse("peer", "", true)
	request = m.browseTabs[0].request
	updated, _ = m.Update(browseMsg{user: "peer", request: request, err: context.Canceled})
	m = updated.(model)
	if footer := m.footerView(); !strings.Contains(footer, "? all controls") {
		t.Fatalf("failed browse footer: %q", footer)
	}
}

func TestActivityPriority(t *testing.T) {
	m := model{
		workspace: workspaceSearch, searchTabIndex: 0, browseTabIndex: 0,
		searchTabs: []searchTab{{query: "selected search", searching: true, request: 3}},
		browseTabs: []browseTab{
			{user: "older", loading: true, request: 1},
			{user: "bytes", loading: true, request: 2, received: 50, total: 100},
		},
	}
	if activity, _ := m.currentActivity(); activity.kind != activitySearch || activity.label != "selected search" {
		t.Fatalf("selected activity: %+v", activity)
	}
	m.workspace = workspaceTransfers
	if activity, _ := m.currentActivity(); activity.kind != activityBrowse || activity.label != "bytes" {
		t.Fatalf("byte browse priority: %+v", activity)
	}
	m.workspace, m.browseTabIndex = workspaceBrowse, 0
	if activity, _ := m.currentActivity(); activity.label != "older" {
		t.Fatalf("selected browse priority: %+v", activity)
	}
}

func TestNoticeExpiresAfterMinimumDuration(t *testing.T) {
	m := model{selected: map[int]bool{}}
	m.setNotice("saved")
	deadline := m.noticeUntil

	m.key(key("?"))
	if m.notice != "saved" {
		t.Fatal("keypress cleared fresh notice")
	}

	updated, _ := m.Update(tickMsg(deadline.Add(-time.Nanosecond)))
	m = updated.(model)
	if m.notice != "saved" {
		t.Fatal("notice cleared before deadline")
	}

	updated, _ = m.Update(tickMsg(deadline))
	m = updated.(model)
	if m.notice != "" {
		t.Fatal("notice remained after deadline")
	}
}

func TestPageAndBoundaryNavigationAcrossTrees(t *testing.T) {
	for _, current := range []workspace{workspaceSearch, workspaceBrowse, workspaceTransfers, workspaceShares} {
		m := model{workspace: current, height: 20, cursor: 50, selected: map[int]bool{}}
		tree := treeState{visible: make([]int, 100)}
		switch current {
		case workspaceSearch:
			m.searchTree = tree
		case workspaceBrowse:
			m.browseTree, m.browseTabs = tree, []browseTab{{user: "peer"}}
		case workspaceTransfers:
			m.transferTrees[transferDownloads] = tree
		case workspaceShares:
			m.shareTree = tree
		}
		m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}))
		if m.cursor != 38 {
			t.Fatalf("workspace %d page up cursor = %d", current, m.cursor)
		}
		m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown}))
		if m.cursor != 50 {
			t.Fatalf("workspace %d page down cursor = %d", current, m.cursor)
		}
		m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}))
		if m.cursor != 0 {
			t.Fatalf("workspace %d home cursor = %d", current, m.cursor)
		}
		m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
		if m.cursor != 99 {
			t.Fatalf("workspace %d end cursor = %d", current, m.cursor)
		}
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
	m.setupField = 5
	m.setupVals[0] = ""
	m.setupKey(key("enter"))
	if m.setupErr == "" {
		t.Fatal("missing credential accepted")
	}
}

func TestSetupSavesNetworkInterface(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.NetworkInterface = "tun0"
	path := t.TempDir() + "/config.json"
	m := newModel(context.Background(), nil, path, false, cfg)
	m.setup, m.setupField = true, 5
	m.setupVals[0], m.setupVals[1], m.setupVals[3] = "alice", "secret", " wg0 "
	if !strings.Contains(m.setupView(), "Network interface (optional)") || !strings.Contains(m.setupView(), "wg0") {
		t.Fatal("setup did not show the network interface")
	}
	m.setupKey(key("enter"))
	t.Setenv("OTO_NETWORK_INTERFACE", "")
	loaded, err := config.Load(path)
	if err != nil || loaded.Soulseek.NetworkInterface != "wg0" {
		t.Fatalf("saved network interface = %q, %v", loaded.Soulseek.NetworkInterface, err)
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

	setup := model{setup: true, setupVals: [6]string{"abcd"}, inputCursor: 4}
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

	setup := model{setup: true, setupVals: [6]string{"one two"}, inputCursor: 7}
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
	m.width, m.height, m.workspace = 80, 16, workspaceSettings

	view := m.View().Content
	if !strings.Contains(view, "Settings") || !strings.Contains(view, "Account") || strings.Contains(view, "secret") || strings.Contains(view, "••••••") || !strings.Contains(view, "Change Soulseek password") {
		t.Fatal("settings account sidebar is missing the password action or exposed the password")
	}
	settingsLines := strings.Count(view, "\n")
	m.workspace = workspaceSearch
	searchLines := strings.Count(m.View().Content, "\n")
	m.workspace = workspaceSettings
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
	if view := m.View().Content; m.settingsSection != settingsConnection || !strings.Contains(view, "Listen address") || !strings.Contains(view, "Network interface") || !strings.Contains(view, "Public IP address") || !strings.Contains(view, "Unknown") || !strings.Contains(view, "Connect on startup") || !strings.Contains(view, "NAT-PMP port forwarding") || !strings.Contains(view, "UPnP port forwarding") {
		t.Fatal("settings sidebar did not navigate to connection settings")
	}
	m.status = snapshot{status: daemon.StatusConnected, publicIP: "1.2.3.4", publicPort: 61000}
	m.cursor = 3
	view = m.View().Content
	for _, want := range []string{
		"Server                  server.slsknet.org:2242",
		"Listen address          0.0.0.0:50300",
		"Network interface       ‹ Automatic ›",
		"Public IP address       1.2.3.4",
		"Listening port status   Press Enter",
		"Connect on startup      On",
		"NAT-PMP port forwarding On",
		"UPnP port forwarding    On",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("connection settings missing aligned row %q", want)
		}
	}
	m.key(key("enter"))
	if m.editing || strings.Contains(strings.Join(m.footerHints(), " | "), "enter") {
		t.Fatal("public IP address row was editable")
	}
	m.cursor = 5
	m.key(key("enter"))
	if m.cfg.Soulseek.ConnectOnStartup {
		t.Fatal("connect-on-startup setting was not staged")
	}
	m.cursor = 6
	m.key(key("enter"))
	if m.editing || m.cfg.Soulseek.NATPMPPortMapping || !m.cfg.Soulseek.UPnPPortMapping {
		t.Fatal("NAT-PMP setting did not toggle independently")
	}
	m.cursor = 7
	m.key(key("enter"))
	if m.editing || m.cfg.Soulseek.NATPMPPortMapping || m.cfg.Soulseek.UPnPPortMapping {
		t.Fatal("UPnP setting did not toggle independently")
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	if m.settingsSection != settingsSearch || !strings.Contains(m.View().Content, "Remember searches") {
		t.Fatal("settings sidebar did not navigate to search settings")
	}
	m.key(key("tab"))
	if m.workspace != workspaceSearch {
		t.Fatal("settings tab did not wrap to search")
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	if m.workspace != workspaceSettings {
		t.Fatal("search tab did not wrap backward to settings")
	}
}

func TestListeningPortCheck(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newModel(context.Background(), nil, "", false, config.Default())
	m.width, m.height, m.workspace, m.settingsSection, m.cursor = 80, 16, workspaceSettings, settingsConnection, 4

	if cmd := m.key(key("enter")); cmd != nil || !strings.Contains(m.notice, "Connect") {
		t.Fatalf("offline check command=%v notice=%q", cmd, m.notice)
	}
	m.status = snapshot{status: daemon.StatusConnected, publicPort: 61000}
	if !strings.Contains(m.View().Content, "Listening port status   Press Enter") || !strings.Contains(strings.Join(m.footerHints(), " | "), "enter check port") {
		t.Fatal("connected port check action missing")
	}
	if cmd := m.key(key("enter")); cmd == nil || !m.portChecking || m.portCheckPort != 61000 {
		t.Fatal("port check did not start")
	}
	if cmd := m.key(key("enter")); cmd != nil {
		t.Fatal("duplicate port check was not suppressed")
	}
	if !strings.Contains(m.View().Content, "Checking 61000/tcp…") {
		t.Fatal("checking state missing")
	}

	updated, _ := m.Update(portCheckMsg{port: 61000, result: daemon.ListeningPortCheck{Port: 61000, Open: true}})
	m = updated.(model)
	if !strings.Contains(m.View().Content, "61000/tcp open") {
		t.Fatal("open result missing")
	}
	updated, _ = m.Update(portCheckMsg{port: 61000, result: daemon.ListeningPortCheck{Port: 61000}})
	m = updated.(model)
	if !strings.Contains(m.View().Content, "61000/tcp closed") {
		t.Fatal("closed result missing")
	}
	updated, _ = m.Update(portCheckMsg{port: 61000, err: errors.New("checker unavailable")})
	m = updated.(model)
	if !strings.Contains(m.View().Content, "Status unknown") || !strings.Contains(m.err, "checker unavailable") {
		t.Fatal("unknown result or error missing")
	}
	m.status.publicPort = 61001
	if !strings.Contains(m.View().Content, "Listening port status   Press Enter") || strings.Contains(m.View().Content, "Status unknown") {
		t.Fatal("stale port result remained visible")
	}
}

func TestNetworkInterfaceChoiceAndCustomEntry(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newModel(context.Background(), nil, "", false, config.Default())
	m.width, m.height, m.workspace, m.settingsSection, m.cursor = 80, 16, workspaceSettings, settingsConnection, 2
	m.networkInterfaces = []string{"eth0", "wg0"}
	if view := m.View().Content; !strings.Contains(view, "‹ Automatic ›") {
		t.Fatalf("automatic interface choice missing: %s", view)
	}

	m.key(key("right"))
	if m.settingsSection != settingsBandwidth {
		t.Fatal("right arrow did not navigate sections outside choice mode")
	}
	m.settingsSection, m.cursor = settingsConnection, 2
	m.key(key("enter"))
	m.key(key("left"))
	if !m.choiceChoosing || m.choiceIndex != 3 || !strings.Contains(m.View().Content, "‹ Custom… ›") {
		t.Fatal("left arrow did not wrap to Custom")
	}
	m.key(key("esc"))
	if m.choiceChoosing || m.cfg.Soulseek.NetworkInterface != "" {
		t.Fatal("escape changed the staged interface")
	}

	m.key(key("enter"))
	m.key(key("right"))
	m.key(key("right"))
	m.key(key("enter"))
	if m.cfg.Soulseek.NetworkInterface != "wg0" || m.choiceChoosing {
		t.Fatal("discovered interface was not staged")
	}

	m.key(key("enter"))
	m.key(key("right"))
	m.key(key("enter"))
	if !m.editing || m.input != "" {
		t.Fatal("Custom did not open a blank text editor")
	}
	m.input = " tun42 "
	m.editKey(key("enter"))
	if m.cfg.Soulseek.NetworkInterface != "tun42" || m.editing || !strings.Contains(m.View().Content, "‹ Custom: tun42 ›") {
		t.Fatal("custom interface was not staged")
	}

	m.key(key("enter"))
	m.key(key("enter"))
	if !m.editing || m.input != "tun42" {
		t.Fatal("existing custom interface was not prefilled")
	}
	m.input = "changed"
	m.editKey(key("esc"))
	if m.cfg.Soulseek.NetworkInterface != "tun42" || m.editing {
		t.Fatal("escape changed the custom interface")
	}

	updated, _ := m.Update(networkInterfacesMsg{err: errors.New("interface lookup failed")})
	m = updated.(model)
	if !strings.Contains(m.err, "interface lookup failed") || m.cfg.Soulseek.NetworkInterface != "tun42" {
		t.Fatal("interface lookup error was not exposed or custom value was lost")
	}
}

func TestUploadSettingsProfilesAndChoices(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newModel(context.Background(), nil, filepath.Join(t.TempDir(), "config.json"), false, config.Default())
	m.width, m.height, m.workspace, m.settingsSection = 90, 18, workspaceSettings, settingsBandwidth
	for _, want := range []string{"Bandwidth", "Active profile", "Unlimited", "Profile name", "Upload speed limit (KiB/s)", "Download speed limit (KiB/s)"} {
		if !strings.Contains(m.View().Content, want) {
			t.Fatalf("missing %q: %s", want, m.View().Content)
		}
	}
	m.key(key("enter"))
	m.key(key("right"))
	if !m.choiceChoosing || !strings.Contains(m.View().Content, "New…") {
		t.Fatal("missing New choice")
	}
	m.key(key("esc"))
	if m.choiceChoosing || len(m.cfg.Bandwidth.Profiles) != 1 {
		t.Fatal("escape changed profiles")
	}
	m.key(key("enter"))
	m.key(key("right"))
	m.key(key("enter"))
	if !m.editing || !m.addingBandwidthProfile {
		t.Fatal("New did not open editor")
	}
	m.input = "Night"
	m.editKey(key("enter"))
	if len(m.cfg.Bandwidth.Profiles) != 2 || m.cfg.Bandwidth.ActiveProfile != "Night" {
		t.Fatal("new profile not staged")
	}
	m.cursor = 1
	m.key(key("enter"))
	m.input = "Unlimited"
	m.editKey(key("enter"))
	if m.err == "" || m.cfg.Bandwidth.ActiveProfile != "Night" {
		t.Fatal("duplicate rename accepted")
	}
	m.key(key("enter"))
	m.input = "Night cap"
	m.editKey(key("enter"))
	if m.cfg.Bandwidth.ActiveProfile != "Night cap" {
		t.Fatal("rename lost selection")
	}
	for i, value := range []string{"64", "128"} {
		m.cursor = i + 2
		m.key(key("enter"))
		m.input = value
		m.editKey(key("enter"))
	}
	profile := m.cfg.Bandwidth.ActiveProfileLimits()
	if profile.UploadSpeedLimitKiB != 64 || profile.DownloadSpeedLimitKiB != 128 {
		t.Fatalf("limits not staged: %+v", profile)
	}
	for _, value := range []string{"-1", "1000001", "oops"} {
		m.cursor = 3
		m.key(key("enter"))
		m.input = value
		m.editKey(key("enter"))
		if m.err == "" || m.cfg.Bandwidth.ActiveProfileLimits() != profile {
			t.Fatal("invalid limit accepted")
		}
	}
	m.settingsSection, m.cursor = settingsUploads, 0
	m.key(key("enter"))
	m.key(key("right"))
	m.key(key("enter"))
	m.cursor = 1
	m.key(key("enter"))
	m.key(key("left"))
	m.key(key("enter"))
	if m.cfg.Uploads.LimitScope != config.UploadLimitPerTransfer || m.cfg.Uploads.Scheduling != config.UploadSchedulingSmallestFirst {
		t.Fatal("upload choices not staged")
	}
	m.settingsSection, m.cursor = settingsBandwidth, 4
	m.key(key("enter"))
	if len(m.cfg.Bandwidth.Profiles) != 1 || m.cfg.Bandwidth.ActiveProfile != "Unlimited" {
		t.Fatal("delete lost adjacent selection")
	}
	m.key(key("enter"))
	if len(m.cfg.Bandwidth.Profiles) != 1 || m.notice == "" {
		t.Fatal("last profile deleted")
	}
	if _, err := os.Stat(m.configPath); !os.IsNotExist(err) {
		t.Fatal("staged settings saved prematurely")
	}
	m.cursor = 0
	m.key(key("left"))
	if m.settingsSection != settingsConnection {
		t.Fatal("Bandwidth not after Connection")
	}
	m.key(key("right"))
	m.key(key("right"))
	if m.settingsSection != settingsDownloads {
		t.Fatal("Downloads not after Bandwidth")
	}
	m.settingsSection, m.width, m.height = settingsBandwidth, 50, 12
	if !strings.Contains(m.View().Content, "Bandwidth") {
		t.Fatal("narrow view lost section")
	}
}

func TestChangePasswordForm(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "alice", "old-secret"
	m := newModel(context.Background(), nil, "", false, cfg)
	m.width, m.height, m.workspace, m.cursor = 80, 20, workspaceSettings, 1

	m.key(key("enter"))
	if m.passwordForm || !strings.Contains(m.err, "Connect") {
		t.Fatal("disconnected password action was not rejected")
	}
	m.status = snapshot{status: daemon.StatusConnected, user: "alice"}
	m.cfg.Soulseek.Username = "staged"
	m.key(key("enter"))
	if m.passwordForm || !strings.Contains(m.err, "Save or revert") {
		t.Fatal("staged username password action was not rejected")
	}
	m.cfg.Soulseek.Username = "alice"
	m.key(key("enter"))
	if !m.passwordForm || m.passwordUser != "alice" || !strings.Contains(m.View().Content, "Username") {
		t.Fatal("password form did not open for the authenticated user")
	}

	updated, _ := m.Update(tea.PasteMsg{Content: " new secret \n"})
	m = updated.(model)
	if strings.Contains(m.View().Content, "new secret") || !strings.Contains(m.View().Content, "••••••••••••") {
		t.Fatal("new password paste was not masked")
	}
	m.passwordFormKey(key("enter"))
	m.passwordVals[1] = "different"
	if cmd := m.passwordFormKey(key("enter")); cmd != nil || !strings.Contains(m.passwordErr, "match") {
		t.Fatal("password confirmation mismatch was accepted")
	}
	m.passwordVals[1] = m.passwordVals[0]
	if cmd := m.passwordFormKey(key("enter")); cmd == nil || !m.passwordChanging {
		t.Fatal("matching passwords were not submitted")
	}

	updated, _ = m.Update(passwordMsg{password: m.passwordVals[0], result: daemon.PasswordChangeResult{Changed: true, Saved: true}})
	m = updated.(model)
	if m.passwordForm || m.cfg.Soulseek.Password != " new secret " || m.notice != "Password changed" || m.passwordVals != ([2]string{}) {
		t.Fatal("successful password change did not update and clear form state")
	}

	m.openPasswordForm()
	m.passwordVals = [2]string{"again", "again"}
	updated, _ = m.Update(passwordMsg{password: "again", result: daemon.PasswordChangeResult{Changed: true, Warning: "password changed but config was not saved"}})
	m = updated.(model)
	if m.cfg.Soulseek.Password != "again" || !strings.Contains(m.err, "not saved") {
		t.Fatal("partial password change did not retain the credential and warning")
	}

	m.openPasswordForm()
	m.passwordVals = [2]string{"temporary", "temporary"}
	m.passwordFormKey(key("esc"))
	if m.passwordForm || m.passwordVals != ([2]string{}) {
		t.Fatal("escape did not clear password form secrets")
	}
}

func TestSearchFilterEditingAndMetadata(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := model{width: 100, height: 16, workspace: workspaceSearch, selected: map[int]bool{}}
	m.key(key("f"))
	if !m.editing || !m.filterEditing {
		t.Fatal("f did not open filter editing")
	}
	if !strings.Contains(m.renderSearch(100, 10), "tab complete: in:") {
		t.Fatal("filter keywords were not shown")
	}
	m.editKey(key("tab"))
	if m.input != "in:" || m.workspace != workspaceSearch {
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
	m.input = "cou"
	m.editKey(key("tab"))
	if m.input != "country:" {
		t.Fatalf("country completion = %q", m.input)
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
	updated, _ := m.Update(searchMsg{append: true, page: daemon.SearchPage{ID: "s", Results: []daemon.SearchResult{{Path: "next.flac", CountryCode: "CA", Public: true}}, Total: 2, FoundTotal: 4}})
	m = updated.(model)
	if len(m.results) != 2 || m.results[1].country != "CA" || m.searchTotal != 2 || m.searchFound != 4 {
		t.Fatal("filtered page was not appended with its totals")
	}
}

func TestSearchResultTabs(t *testing.T) {
	m := model{workspace: workspaceSearch, acceptedSearchDefault: "type:audio", selected: map[int]bool{}}
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
		workspace: workspaceSearch, searchTabIndex: 0, searchOperation: 1,
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
	m := model{workspace: workspaceTransfers, selected: map[int]bool{}, transfers: []transfer{
		{id: "d1", filename: "album.flac", direction: "download", state: "running", done: 25, total: 100, speed: 1536, etaSeconds: timingValue(1), elapsedMS: timingValue(1500), user: "alice"},
		{id: "d2", filename: `folder\queued.mp3`, direction: "download", state: "queued", total: 100, queue: 2, user: "alice"},
		{id: "u1", filename: "shared.wav", direction: "upload", state: "completed", done: 100, total: 100, user: "bob"},
	}}
	if got := m.workspaceNames()[workspaceTransfers]; got != "Transfers 1↓ 0↑" {
		t.Fatalf("transfer activity tab = %q", got)
	}
	m.transferTrees[transferDownloads], m.transferCursors[transferDownloads] = buildTransferTree(m.transfers, "download", treeState{}, 0)
	m.transferTrees[transferUploads], m.transferCursors[transferUploads] = buildTransferTree(m.transfers, "upload", treeState{}, 0)
	m.cursor = 0
	ids := m.transferActionIDs()
	if len(ids) != 2 || (ids[0] != "d1" && ids[1] != "d1") || (ids[0] != "d2" && ids[1] != "d2") {
		t.Fatalf("recursive transfer action IDs = %v", ids)
	}
	m.cursor = m.transferTrees[transferDownloads].cursorForSource(0)

	downloads := m.renderTransfers(100, 10)
	if !strings.Contains(downloads, "[↓ DOWNLOADS 2]") || !strings.Contains(downloads, "███░░░░░░░░░░░  25%") || !strings.Contains(downloads, "1.5 KiB/s") || !strings.Contains(downloads, "Elapsed 0:01  ETA 0:01") || !strings.Contains(downloads, "⠋") || strings.Contains(downloads, "shared.wav") {
		t.Fatalf("download tab did not render progress and spinner correctly: %q", downloads)
	}
	barColumn, bars := -1, 0
	for _, line := range strings.Split(downloads, "\n") {
		if i := strings.IndexAny(line, "█░"); i >= 0 {
			column := lipgloss.Width(line[:i])
			if barColumn >= 0 && column != barColumn {
				t.Fatalf("progress bars are not aligned: columns %d and %d", barColumn, column)
			}
			barColumn, bars = column, bars+1
		}
	}
	if bars < 2 {
		t.Fatalf("expected multiple progress bars: %q", downloads)
	}
	for _, width := range []int{40, 100} {
		for _, line := range strings.Split(m.renderTransfers(width, 10), "\n") {
			if lipgloss.Width(line) > width {
				t.Fatalf("transfer tree exceeds width %d: %q", width, line)
			}
		}
	}
	m.transfers[1].state, m.transfers[1].err = "failed", "File not shared."
	if failed := m.renderTransfers(140, 10); !strings.Contains(failed, "failed: File not shar") {
		t.Fatalf("transfer error was not rendered: %q", failed)
	}
	m.transfers[1].state, m.transfers[1].err = "queued", ""
	downloadCursor := m.transferTrees[transferDownloads].cursorForSource(1)
	m.cursor = downloadCursor
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown, Mod: tea.ModCtrl}))
	uploads := m.renderTransfers(100, 10)
	if m.rows() != 2 || !strings.Contains(uploads, "[↑ UPLOADS 1]") || !strings.Contains(uploads, "shared.wav") || strings.Contains(uploads, "album.flac") {
		t.Fatalf("upload tab did not isolate uploads: rows=%d view=%q", m.rows(), uploads)
	}
	if _, node := m.transferTrees[transferUploads].node(m.transferTrees[transferUploads].cursorForSource(2)); node == nil || node.source != 2 {
		t.Fatal("upload tree did not map to source transfer")
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModCtrl}))
	if m.cursor != downloadCursor {
		t.Fatalf("download cursor was not restored: %d", m.cursor)
	}
	updated, _ := m.Update(tickMsg{})
	if next := updated.(model).renderTransfers(100, 10); !strings.Contains(next, "⠙") {
		t.Fatalf("spinner did not advance: %q", next)
	}

	next := toTransfers([]daemon.Transfer{{ID: "d1", SpeedBPS: 1024, ElapsedMS: timingValue(1500), ETASeconds: timingValue(1)}})
	if next[0].speed != 1024 || *next[0].elapsedMS != 1500 || *next[0].etaSeconds != 1 {
		t.Fatalf("daemon timing lost: %+v", next)
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
	m := model{workspace: workspaceSearch, width: 100, height: 30, selected: map[int]bool{}, results: []result{{
		user: "alice", path: `Music\song.flac`, extension: "flac", country: "US", size: 1536, free: true,
		bitrate: 320, duration: 125, vbr: true, sampleRate: 44100, bitDepth: 24, public: true,
	}}}
	m.searchTree, m.cursor = buildSearchTree(m.results, treeState{}, 0)
	m.cursor = m.searchTree.cursorForSource(0)
	m.key(key("i"))
	view := m.View().Content
	for _, want := range []string{"File details", `Music\song.flac`, "alice", "Country       US", "1.5 KiB", "public", "flac", "320 kbps VBR", "2:05", "44100 Hz", "24-bit", "free slot"} {
		if !strings.Contains(view, want) {
			t.Fatalf("file details missing %q: %q", want, view)
		}
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.details {
		t.Fatal("escape did not close file details")
	}

	m.workspace, m.browseUser = workspaceBrowse, "bob"
	m.entries = []entry{{name: `Private\demo.wav`, extension: "wav", size: 42, private: true, bitrate: 1411, vbrKnown: true}}
	m.browseTree, m.cursor = buildBrowseTree(m.entries, "", treeState{}, 0)
	m.cursor = m.browseTree.cursorForSource(0)
	m.key(key("i"))
	view = m.View().Content
	for _, want := range []string{"bob", "private", "1411 kbps CBR"} {
		if !strings.Contains(view, want) {
			t.Fatalf("browse details missing %q: %q", want, view)
		}
	}
	if strings.Contains(view, "Country") {
		t.Fatalf("unknown country was shown: %q", view)
	}
}

func TestBrowseResultFolderAndUserTabs(t *testing.T) {
	m := model{
		workspace: workspaceSearch,
		results:   []result{{user: "nss", path: `audio\Hardstyle_320\song.mp3`}},
		selected:  map[int]bool{},
	}
	m.searchTree, m.cursor = buildSearchTree(m.results, treeState{}, 0)
	m.cursor = m.searchTree.cursorForSource(0)
	if cmd := m.key(key("b")); cmd == nil || m.workspace != workspaceBrowse || m.browseUser != "nss" || len(m.browseTabs) != 1 {
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

func TestBrowseFindTree(t *testing.T) {
	entries := []entry{
		{name: `Music\Album`, directory: true},
		{name: `Music\Album\Song.FLAC`},
		{name: `Music\Album\Disc`, directory: true},
		{name: `Music\Album\Disc\mix.mp3`},
		{name: `Other`, directory: true},
		{name: `Other\SONG.FLAC`},
	}
	filtered, _ := buildBrowseTree(entries, "ALBUM", treeState{}, 0)
	_, hasMusic := filtered.byID[treeID("browse-dir", "Music")]
	_, hasDisc := filtered.byID[treeID("browse-dir", `Music\Album\Disc`)]
	if browseMatchCount(entries, "ALBUM") != 4 || !hasMusic || !hasDisc {
		t.Fatalf("folder query did not retain its subtree and ancestors: %+v", filtered.byID)
	}
	if cursor := filtered.cursorForSource(3); filtered.nodes[filtered.visible[cursor]].source != 3 {
		t.Fatal("filtered tree lost the original source index")
	}
	filesOnly, _ := buildBrowseTree(entries, "song.flac", treeState{}, 0)
	found := map[int]bool{}
	for _, node := range filesOnly.nodes {
		if node.kind == treeFile {
			found[node.source] = true
		}
	}
	if !found[1] || !found[5] || len(found) != 2 {
		t.Fatalf("case-insensitive file matches = %+v", found)
	}
	none, _ := buildBrowseTree(entries, "missing", treeState{}, 0)
	all, _ := buildBrowseTree(entries, "", treeState{}, 0)
	if len(none.visible) != 0 || browseMatchCount(entries, "") != len(entries) || len(all.visible) <= len(filtered.visible) {
		t.Fatal("zero-match or cleared Browse find was not represented correctly")
	}
}

func TestBrowseFindInputTabsRefreshAndTarget(t *testing.T) {
	entries := []entry{
		{name: `Music\Album`, directory: true},
		{name: `Music\Album\Song.FLAC`},
		{name: `Music\Album\Disc`, directory: true},
		{name: `Music\Album\Disc\mix.mp3`},
		{name: `Other`, directory: true},
		{name: `Other\notes.txt`},
	}
	full, _ := buildBrowseTree(entries, "", treeState{}, 0)
	m := model{workspace: workspaceBrowse, browseTabs: []browseTab{{user: "peer", entries: entries, selected: map[int]bool{}, loaded: true, tree: full}}, browseTabIndex: 0, width: 100, height: 20}
	m.loadBrowseTab(0)
	m.selected[1] = true
	if cmd := m.key(key("f")); cmd != nil || !m.editing || !m.browseFindEditing {
		t.Fatal("f did not open local Browse find")
	}
	m.input = "ignored"
	m.editKey(key("esc"))
	if m.browseFilter != "" || !m.selected[1] {
		t.Fatal("Escape applied the Browse find or cleared selection")
	}
	m.key(key("f"))
	m.input = "missing"
	m.editKey(key("enter"))
	if !strings.Contains(m.renderBrowse(100, 15), "No matching shared files") {
		t.Fatal("zero-match Browse find message was not rendered")
	}
	m.key(key("f"))
	m.input = " album "
	if cmd := m.editKey(key("enter")); cmd != nil || m.browseFilter != "album" || len(m.selected) != 0 || m.browseTabs[0].filter != "album" {
		t.Fatalf("Browse find was not applied locally: filter=%q selected=%v", m.browseFilter, m.selected)
	}
	view := m.renderBrowse(100, 15)
	if !strings.Contains(view, "4 matches / 6 items") || !strings.Contains(view, "f  album") {
		t.Fatalf("Browse find UI missing count or query: %q", view)
	}
	album := treeID("browse-dir", `Music\Album`)
	m.cursor = 0
	for cursor, index := range m.browseTree.visible {
		if m.browseTree.nodes[index].id == album {
			m.cursor = cursor
			break
		}
	}
	m.toggle()
	if !m.selected[1] || !m.selected[3] || len(m.selected) != 2 {
		t.Fatalf("filtered folder selected hidden files: %+v", m.selected)
	}

	if cmd := m.openBrowse("peer", "", true); cmd == nil {
		t.Fatal("refresh did not start")
	}
	request := m.browseTabs[0].request
	refreshed := []entry{{name: `Music\Album`, directory: true}, {name: `Music\Album\new.flac`}, {name: `Other`, directory: true}}
	updated, _ := m.Update(browseMsg{user: "peer", request: request, entries: refreshed})
	m = updated.(model)
	if m.browseFilter != "album" || m.browseTabs[0].filter != "album" || browseMatchCount(m.entries, m.browseFilter) != 2 {
		t.Fatal("refresh did not reapply the tab's Browse find")
	}
	m.openBrowse("other", "", false)
	if m.browseFilter != "" || len(m.browseTabs) != 2 {
		t.Fatal("new Browse tab inherited another tab's find")
	}
	m.switchBrowseTab(-1)
	if m.browseUser != "peer" || m.browseFilter != "album" {
		t.Fatal("Browse find was not restored with its tab")
	}
	if cmd := m.openBrowse("peer", `Music\Album`, false); cmd != nil || m.browseFilter != "" || m.browseTabs[0].filter != "" {
		t.Fatal("targeted Browse did not clear the local find")
	}
	_, node := m.browseTree.node(m.cursor)
	if node == nil || node.path != `Music\Album` {
		t.Fatalf("target folder remained hidden: %+v", node)
	}
}

func TestSavedBrowsePickerAndCacheActions(t *testing.T) {
	savedAt := time.Date(2026, 3, 13, 12, 30, 0, 0, time.UTC)
	m := model{
		workspace:    workspaceBrowse,
		width:        100,
		height:       20,
		selected:     map[int]bool{},
		savedBrowses: []daemon.SavedBrowse{{Username: "alice", SavedAt: savedAt}, {Username: "bob", SavedAt: savedAt}},
	}
	view := m.renderBrowse(100, 12)
	for _, want := range []string{"2 saved users", "SAVED USER", "alice", "bob"} {
		if !strings.Contains(view, want) {
			t.Fatalf("saved browse picker missing %q: %q", want, view)
		}
	}
	m.key(key("j"))
	if m.cursor != 1 {
		t.Fatalf("saved browse cursor = %d", m.cursor)
	}
	if cmd := m.key(key("enter")); cmd == nil || m.browseUser != "bob" || len(m.browseTabs) != 1 || !m.loading {
		t.Fatalf("saved browse did not open: user=%q tabs=%d loading=%v", m.browseUser, len(m.browseTabs), m.loading)
	}

	request := m.browseTabs[0].request
	updated, _ := m.Update(browseMsg{user: "bob", request: request, cached: true, savedAt: savedAt, revision: 7})
	m = updated.(model)
	view = m.renderBrowse(100, 12)
	if !m.browseLoaded || !m.browseCached || !strings.Contains(view, "bob (cached)") || !strings.Contains(view, "No shared files") {
		t.Fatalf("cached empty browse not rendered: %q", view)
	}
	if cmd := m.key(key("s")); cmd == nil {
		t.Fatal("loaded browse could not be saved")
	}
	updated, _ = m.Update(saveBrowseMsg{saved: daemon.SavedBrowse{Username: "bob", SavedAt: savedAt.Add(time.Minute)}})
	m = updated.(model)
	if !strings.Contains(m.notice, "Saved share list for bob") {
		t.Fatalf("save notice = %q", m.notice)
	}

	if cmd := m.key(key("r")); cmd == nil || !m.loading {
		t.Fatalf("cached browse refresh not started: loading=%v", m.loading)
	}
	request = m.browseTabs[0].request
	updated, _ = m.Update(browseMsg{user: "bob", request: request, revision: 8, entries: []entry{{name: "Music", directory: true}}})
	m = updated.(model)
	if m.browseCached || strings.Contains(m.renderBrowse(100, 12), "bob (cached)") {
		t.Fatal("live refresh kept cached marker")
	}
	m.closeBrowseTab()
	if len(m.browseTabs) != 0 || m.browseUser != "" || !strings.Contains(m.renderBrowse(100, 12), "SAVED USER") {
		t.Fatal("closing final browse tab did not return to picker")
	}
	if cmd := m.key(key("r")); cmd == nil || !m.savedBrowseLoading {
		t.Fatal("saved browse picker refresh not started")
	}
}

func TestFilterCompletionCyclesBackward(t *testing.T) {
	if got := completeFilter("", true); got != "public:" {
		t.Fatalf("backward field completion = %q", got)
	}
	if got := completeFilter("type:a", true); got != "type:archive" {
		t.Fatalf("backward type completion = %q", got)
	}
	m := model{workspace: workspaceSearch, editing: true, filterEditing: true}
	m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	if m.input != "public:" || m.workspace != workspaceSearch {
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
	m := model{workspace: workspaceSearch, results: results, searchTree: tree, cursor: albumCursor, selected: map[int]bool{}}
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
	if m.workspace != workspaceSearch || len(m.searchTree.visible) >= len(tree.visible) {
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

func TestFolderDownloadMenu(t *testing.T) {
	results := []result{{user: "peer", path: `Music\Album\song.flac`, size: 5}, {user: "peer", path: `Music\Album\Disc\two.flac`, size: 6}}
	tree, _ := buildSearchTree(results, treeState{}, 0)
	folderID := treeID("search-dir", "peer", `Music\Album`)
	folderCursor := 0
	for cursor, index := range tree.visible {
		if index == tree.byID[folderID] {
			folderCursor = cursor
			break
		}
	}
	m := model{workspace: workspaceSearch, cfg: config.Config{DownloadDir: "/downloads"}, results: results, searchTree: tree, cursor: folderCursor, selected: map[int]bool{}, width: 80, height: 24}
	if cmd := m.key(key("d")); cmd != nil || !m.folderMenu || m.folderMenuUser != "peer" || m.folderMenuPath != `Music\Album` {
		t.Fatalf("folder menu did not open: %+v", m)
	}
	if view := m.View().Content; !strings.Contains(view, "Download folder + subfolders") || !strings.Contains(view, "/downloads") {
		t.Fatal("folder menu was not rendered")
	}
	m.folderMenuKey(key("/"))
	m.folderMenuKey(key("x"))
	m.folderMenuKey(key("enter"))
	if req := m.folderMenuRequest(); m.folderMenuEditing || req.DownloadDir != "/downloadsx" {
		t.Fatalf("download path was not edited: %+v", req)
	}
	m.folderMenuKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	if req := m.folderMenuRequest(); !req.Recursive || len(req.Subfolders) != 1 || req.Subfolders[0] != `Music\Album\Disc` || len(req.Files) != 2 {
		t.Fatalf("recursive option request: %+v", req)
	}
	m.folderMenuKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if req := m.folderMenuRequest(); req.Recursive || len(req.Subfolders) != 0 || len(req.Files) != 1 || req.Files[0].Filename != `Music\Album\song.flac` {
		t.Fatalf("folder-only option request: %+v", req)
	}
	m.folderMenuKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.folderMenu {
		t.Fatal("escape did not close folder menu")
	}
	m.key(key("d"))
	m.folderMenuKey(key("j"))
	if cmd := m.folderMenuKey(key("enter")); cmd == nil || m.folderMenu {
		t.Fatal("enter did not dispatch and close folder menu")
	}

	m.selected[0] = true
	if cmd := m.key(key("d")); cmd == nil || m.folderMenu {
		t.Fatal("selected file did not keep immediate download behavior")
	}
	m.selected[0] = false
	m.cursor = m.searchTree.cursorForSource(0)
	if cmd := m.key(key("d")); cmd == nil || m.folderMenu {
		t.Fatal("file cursor did not keep immediate download behavior")
	}

	browseEntries := []entry{{name: `Music\Album`, directory: true}, {name: `Music\Album\song.flac`, size: 5}, {name: `Music\Album\Disc`, directory: true}, {name: `Music\Album\Disc\two.flac`, size: 6}}
	browseTree, _ := buildBrowseTree(browseEntries, "song", treeState{}, 0)
	m = model{workspace: workspaceBrowse, cfg: config.Config{DownloadDir: "/downloads"}, browseUser: "peer", browseFilter: "song", entries: browseEntries, browseTree: browseTree, selected: map[int]bool{}}
	for cursor, index := range m.browseTree.visible {
		if m.browseTree.nodes[index].id == treeID("browse-dir", `Music\Album`) {
			m.cursor = cursor
			break
		}
	}
	if cmd := m.key(key("d")); cmd != nil || !m.folderMenu || m.folderMenuUser != "peer" || len(m.folderMenuSubfolders) != 1 || len(m.folderMenuFiles[1]) != 2 {
		t.Fatalf("filtered Browse folder menu used partial contents: %+v", m)
	}
}

func TestSharesTreeIgnoresStaleBrowseResponses(t *testing.T) {
	m := model{workspace: workspaceShares, selected: map[int]bool{}, shares: []share{{name: "Music", path: "/music"}}, shareGeneration: 2}
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

func TestDownloadSettingsCommandsAndStates(t *testing.T) {
	cfg := config.Default()
	cfg.Downloads.AfterFileCommand, cfg.Downloads.AfterFolderCommand = `echo "$1"`, `echo folder "$1"`
	m := model{workspace: workspaceSettings, settingsSection: settingsDownloads, cfg: cfg}
	fields := m.settingFields()
	if len(fields) < 6 || fields[1].value != cfg.Downloads.AfterFileCommand || fields[2].value != cfg.Downloads.AfterFolderCommand {
		t.Fatalf("download settings not rendered: %+v", fields)
	}
	m.cursor = 1
	if err := m.setSettingValue("new-file-hook"); err != nil || m.cfg.Downloads.AfterFileCommand != "new-file-hook" {
		t.Fatalf("file hook not editable: %v", err)
	}
	m.cursor = 2
	if err := m.setSettingValue("new-folder-hook"); err != nil || m.cfg.Downloads.AfterFolderCommand != "new-folder-hook" {
		t.Fatalf("folder hook not editable: %v", err)
	}
	m.workspace, m.transferTab = workspaceTransfers, transferDownloads
	hints := strings.Join(m.footerHints(), " ")
	if !strings.Contains(hints, "p pause") || !strings.Contains(hints, "r resume") {
		t.Fatalf("download transfer hints missing: %q", hints)
	}
	m.transfers = []transfer{{direction: "download", state: "paused"}}
	if m.active() {
		t.Fatal("paused downloads must not trigger the active-transfer quit warning")
	}
	m.transfers[0].state = "retrying"
	if !m.active() {
		t.Fatal("retrying downloads should remain active")
	}
}
