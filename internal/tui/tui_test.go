package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
		failIfFmt(t, !strings.Contains(view, want), "wishlist view %q missing %q", view, want)
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
	failIf(t, updated.(model).cursor != 1, "wishlist polling reset the cursor")

	m = model{cfg: cfg, wishlistNotified: map[string]uint64{}}
	updated, cmd := m.Update(wishlistMsg{items: []daemon.WishlistItem{{ID: "w-1", Unread: true, ResultCount: 2, NotificationSequence: 1}}})
	m = updated.(model)
	failIf(t, cmd == nil, "new wishlist notification did not ring")
	_, cmd = m.Update(wishlistMsg{items: m.wishlist})
	failIf(t, cmd != nil, "same wishlist notification rang twice")

	m.workspace, m.settingsSection, m.cursor = workspaceSettings, settingsSearch, 7
	m.cfg.Search.WishlistIntervalMinutes = 0
	failIf(t, !strings.Contains(m.renderSettings(80, 12), "Off"), "zero wishlist interval was not shown as Off")
	m.key(key("enter"))
	m.input = "30"
	m.editKey(key("enter"))
	failIf(t, m.cfg.Search.WishlistIntervalMinutes != 30, "wishlist interval setting was not edited")
	m.cursor = 8
	m.key(key("enter"))
	failIf(t, m.cfg.Search.WishlistNotifications, "wishlist notification setting did not toggle")
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
	failIf(t, cmd != nil || m.activityRunning, "idle activity tick did not stop")

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
	failIf(t, m.browseTabs[0].total != 0, "stale browse progress was applied")
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
	failIf(t, m.notice != "saved", "keypress cleared fresh notice")

	updated, _ := m.Update(tickMsg(deadline.Add(-time.Nanosecond)))
	m = updated.(model)
	failIf(t, m.notice != "saved", "notice cleared before deadline")

	updated, _ = m.Update(tickMsg(deadline))
	m = updated.(model)
	failIf(t, m.notice != "", "notice remained after deadline")
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
		failIfFmt(t, m.cursor != 38, "workspace %d page up cursor = %d", current, m.cursor)
		m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown}))
		failIfFmt(t, m.cursor != 50, "workspace %d page down cursor = %d", current, m.cursor)
		m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}))
		failIfFmt(t, m.cursor != 0, "workspace %d home cursor = %d", current, m.cursor)
		m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
		failIfFmt(t, m.cursor != 99, "workspace %d end cursor = %d", current, m.cursor)
	}
}

func TestPasswordPaste(t *testing.T) {
	m := newModel(context.Background(), nil, "", false, config.Default())
	m.setup, m.setupField = true, 1
	updated, _ := m.Update(tea.PasteMsg{Content: "pasted-secret\n"})
	m = updated.(model)
	failIf(t, m.setupVals[1] != "pasted-secret" || strings.Contains(m.setupView(), "pasted-secret"), "password paste was not accepted and masked")
}
func TestSetupMasksAndValidates(t *testing.T) {
	m := newModel(context.Background(), nil, "", false, config.Default())
	m.setup = true
	m.setupVals[0] = "u"
	m.setupVals[1] = "secret"
	failIf(t, !strings.Contains(m.setupView(), "••••••"), "password not masked")
	m.setupField = 0
	m.setupKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	m.setupKey(key("tab"))
	m.setupKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	m.setupKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	failIf(t, m.setupField != 0, "setup field navigation did not handle arrows and tab")
	m.setupField = 5
	m.setupVals[0] = ""
	m.setupKey(key("enter"))
	failIf(t, m.setupErr == "", "missing credential accepted")
}

func TestSetupSavesNetworkInterface(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.NetworkInterface = "tun0"
	path := t.TempDir() + "/config.json"
	m := newModel(context.Background(), nil, path, false, cfg)
	m.setup, m.setupField = true, 5
	m.setupVals[0], m.setupVals[1], m.setupVals[3] = "alice", "secret", " wg0 "
	failIf(t, !strings.Contains(m.setupView(), "Network interface (optional)") || !strings.Contains(m.setupView(), "wg0"), "setup did not show the network interface")
	m.setupKey(key("enter"))
	t.Setenv("OTO_NETWORK_INTERFACE", "")
	loaded, err := config.Load(path)
	failIfFmt(t, err != nil || loaded.Soulseek.NetworkInterface != "wg0", "saved network interface = %q, %v", loaded.Soulseek.NetworkInterface, err)
}
func TestTextFieldsEditAtCursor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := model{editing: true, input: "aébc", inputCursor: 4, selected: map[int]bool{}}
	m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	m.editKey(key("X"))
	failIfFmt(t, m.input != "aéXbc" || m.inputCursor != 3, "insert at cursor = %q @ %d", m.input, m.inputCursor)
	m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDelete}))
	failIfFmt(t, m.input != "aéc" || m.inputCursor != 2, "delete at cursor = %q @ %d", m.input, m.inputCursor)
	m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}))
	m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	updated, _ := m.Update(tea.PasteMsg{Content: "Ω"})
	m = updated.(model)
	failIfFmt(t, m.input != "aΩéc" || m.inputCursor != 2 || !strings.Contains(renderInput("", m.input, m.inputCursor, false, lipgloss.NewStyle()), "aΩ█éc"), "paste/caret at cursor = %q @ %d", m.input, m.inputCursor)

	setup := model{setup: true, setupVals: [6]string{"abcd"}, inputCursor: 4}
	setup.setupKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	setup.setupKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	setup.setupKey(key("X"))
	failIfFmt(t, setup.setupVals[0] != "abXcd" || setup.inputCursor != 3, "setup cursor edit = %q @ %d", setup.setupVals[0], setup.inputCursor)
	masked := renderInput("", "secret", 2, true, lipgloss.NewStyle())
	failIfFmt(t, strings.Contains(masked, "secret") || !strings.Contains(masked, "••█••••"), "masked cursor rendering = %q", masked)
}

func TestTextFieldWordEditing(t *testing.T) {
	ctrl := func(code rune) tea.KeyPressMsg {
		return tea.KeyPressMsg(tea.Key{Code: code, Mod: tea.ModCtrl})
	}
	m := model{editing: true, input: "alpha beta gamma", inputCursor: 16}
	m.editKey(ctrl(tea.KeyLeft))
	failIfFmt(t, m.inputCursor != 11, "ctrl+left cursor = %d", m.inputCursor)
	m.editKey(ctrl(tea.KeyBackspace))
	failIfFmt(t, m.input != "alpha gamma" || m.inputCursor != 6, "ctrl+backspace = %q @ %d", m.input, m.inputCursor)
	m.editKey(ctrl(tea.KeyDelete))
	failIfFmt(t, m.input != "alpha " || m.inputCursor != 6, "ctrl+delete = %q @ %d", m.input, m.inputCursor)
	m.editKey(ctrl('u'))
	failIfFmt(t, m.input != "" || m.inputCursor != 0, "ctrl+u = %q @ %d", m.input, m.inputCursor)

	setup := model{setup: true, setupVals: [6]string{"one two"}, inputCursor: 7}
	setup.setupKey(ctrl(tea.KeyLeft))
	setup.setupKey(ctrl(tea.KeyBackspace))
	failIfFmt(t, setup.setupVals[0] != "two" || setup.inputCursor != 0, "setup word edit = %q @ %d", setup.setupVals[0], setup.inputCursor)
}

func TestErrorRowIsStableAndTracksDaemonState(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := model{width: 80, height: 14, selected: map[int]bool{}}
	cleanLines := strings.Count(m.View().Content, "\n")

	updated, _ := m.Update(statusMsg{snapshot: daemon.Snapshot{Status: daemon.StatusReconnecting, Error: "listen tcp 0.0.0.0:50300: bind: address already in use"}})
	m = updated.(model)
	updated, _ = m.Update(sharesMsg{})
	m = updated.(model)
	failIf(t, !strings.Contains(m.View().Content, "Error: listen tcp"), "daemon error did not persist across unrelated refreshes")
	if got := strings.Count(m.View().Content, "\n"); got != cleanLines {
		t.Fatalf("error shifted layout: clean=%d error=%d", cleanLines, got)
	}

	updated, _ = m.Update(statusMsg{snapshot: daemon.Snapshot{Status: daemon.StatusConnected}})
	m = updated.(model)
	failIf(t, m.status.err != "" || strings.Contains(m.View().Content, "No errors"), "resolved daemon error was not cleared to a blank row")
}

func TestSettingsSidebarEditsAccountWithoutLeakingPassword(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "alice", "secret"
	m := newModel(context.Background(), nil, "", false, cfg)
	m.width, m.height, m.workspace = 80, 16, workspaceSettings

	view := m.View().Content
	failIf(t, !strings.Contains(view, "Settings") || !strings.Contains(view, "Account") || strings.Contains(view, "secret") || strings.Contains(view, "••••••") || !strings.Contains(view, "Change Soulseek password"), "settings account sidebar is missing the password action or exposed the password")
	settingsLines := strings.Count(view, "\n")
	m.workspace = workspaceSearch
	searchLines := strings.Count(m.View().Content, "\n")
	m.workspace = workspaceSettings
	failIfFmt(t, settingsLines != searchLines, "settings shifted layout: search=%d settings=%d", searchLines, settingsLines)
	m.key(key("enter"))
	m.input = "bob"
	m.editKey(key("enter"))
	failIf(t, m.cfg.Soulseek.Username != "bob", "account username was not edited")

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
		failIfFmt(t, !strings.Contains(view, want), "connection settings missing aligned row %q", want)
	}
	m.key(key("enter"))
	failIf(t, m.editing || strings.Contains(strings.Join(m.footerHints(), " | "), "enter"), "public IP address row was editable")
	m.cursor = 5
	m.key(key("enter"))
	failIf(t, m.cfg.Soulseek.ConnectOnStartup, "connect-on-startup setting was not staged")
	m.cursor = 6
	m.key(key("enter"))
	failIf(t, m.editing || m.cfg.Soulseek.NATPMPPortMapping || !m.cfg.Soulseek.UPnPPortMapping, "NAT-PMP setting did not toggle independently")
	m.cursor = 7
	m.key(key("enter"))
	failIf(t, m.editing || m.cfg.Soulseek.NATPMPPortMapping || m.cfg.Soulseek.UPnPPortMapping, "UPnP setting did not toggle independently")
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	failIf(t, m.settingsSection != settingsSearch || !strings.Contains(m.View().Content, "Remember searches"), "settings sidebar did not navigate to search settings")
	m.key(key("tab"))
	failIf(t, m.workspace != workspaceSearch, "settings tab did not wrap to search")
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	failIf(t, m.workspace != workspaceSettings, "search tab did not wrap backward to settings")
}

func TestListeningPortCheck(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newModel(context.Background(), nil, "", false, config.Default())
	m.width, m.height, m.workspace, m.settingsSection, m.cursor = 80, 16, workspaceSettings, settingsConnection, 4

	if cmd := m.key(key("enter")); cmd != nil || !strings.Contains(m.notice, "Connect") {
		t.Fatalf("offline check command=%v notice=%q", cmd, m.notice)
	}
	m.status = snapshot{status: daemon.StatusConnected, publicPort: 61000}
	failIf(t, !strings.Contains(m.View().Content, "Listening port status   Press Enter") || !strings.Contains(strings.Join(m.footerHints(), " | "), "enter check port"), "connected port check action missing")
	if cmd := m.key(key("enter")); cmd == nil || !m.portChecking || m.portCheckPort != 61000 {
		t.Fatal("port check did not start")
	}
	if cmd := m.key(key("enter")); cmd != nil {
		t.Fatal("duplicate port check was not suppressed")
	}
	failIf(t, !strings.Contains(m.View().Content, "Checking 61000/tcp…"), "checking state missing")

	updated, _ := m.Update(portCheckMsg{port: 61000, result: daemon.ListeningPortCheck{Port: 61000, Open: true}})
	m = updated.(model)
	failIf(t, !strings.Contains(m.View().Content, "61000/tcp open"), "open result missing")
	updated, _ = m.Update(portCheckMsg{port: 61000, result: daemon.ListeningPortCheck{Port: 61000}})
	m = updated.(model)
	failIf(t, !strings.Contains(m.View().Content, "61000/tcp closed"), "closed result missing")
	updated, _ = m.Update(portCheckMsg{port: 61000, err: errors.New("checker unavailable")})
	m = updated.(model)
	failIf(t, !strings.Contains(m.View().Content, "Status unknown") || !strings.Contains(m.err, "checker unavailable"), "unknown result or error missing")
	m.status.publicPort = 61001
	failIf(t, !strings.Contains(m.View().Content, "Listening port status   Press Enter") || strings.Contains(m.View().Content, "Status unknown"), "stale port result remained visible")
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
	failIf(t, m.settingsSection != settingsBandwidth, "right arrow did not navigate sections outside choice mode")
	m.settingsSection, m.cursor = settingsConnection, 2
	m.key(key("enter"))
	m.key(key("left"))
	failIf(t, !m.choiceChoosing || m.choiceIndex != 3 || !strings.Contains(m.View().Content, "‹ Custom… ›"), "left arrow did not wrap to Custom")
	m.key(key("esc"))
	failIf(t, m.choiceChoosing || m.cfg.Soulseek.NetworkInterface != "", "escape changed the staged interface")

	m.key(key("enter"))
	m.key(key("right"))
	m.key(key("right"))
	m.key(key("enter"))
	failIf(t, m.cfg.Soulseek.NetworkInterface != "wg0" || m.choiceChoosing, "discovered interface was not staged")

	m.key(key("enter"))
	m.key(key("right"))
	m.key(key("enter"))
	failIf(t, !m.editing || m.input != "", "Custom did not open a blank text editor")
	m.input = " tun42 "
	m.editKey(key("enter"))
	failIf(t, m.cfg.Soulseek.NetworkInterface != "tun42" || m.editing || !strings.Contains(m.View().Content, "‹ Custom: tun42 ›"), "custom interface was not staged")

	m.key(key("enter"))
	m.key(key("enter"))
	failIf(t, !m.editing || m.input != "tun42", "existing custom interface was not prefilled")
	m.input = "changed"
	m.editKey(key("esc"))
	failIf(t, m.cfg.Soulseek.NetworkInterface != "tun42" || m.editing, "escape changed the custom interface")

	updated, _ := m.Update(networkInterfacesMsg{err: errors.New("interface lookup failed")})
	m = updated.(model)
	failIf(t, !strings.Contains(m.err, "interface lookup failed") || m.cfg.Soulseek.NetworkInterface != "tun42", "interface lookup error was not exposed or custom value was lost")
}

func TestUploadSettingsProfilesAndChoices(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newModel(context.Background(), nil, filepath.Join(t.TempDir(), "config.json"), false, config.Default())
	m.width, m.height, m.workspace, m.settingsSection = 90, 18, workspaceSettings, settingsBandwidth
	for _, want := range []string{"Bandwidth", "Active profile", "Unlimited", "Profile name", "Upload speed limit (KiB/s)", "Download speed limit (KiB/s)"} {
		failIfFmt(t, !strings.Contains(m.View().Content, want), "missing %q: %s", want, m.View().Content)
	}
	m.key(key("enter"))
	m.key(key("right"))
	failIf(t, !m.choiceChoosing || !strings.Contains(m.View().Content, "New…"), "missing New choice")
	m.key(key("esc"))
	failIf(t, m.choiceChoosing || len(m.cfg.Bandwidth.Profiles) != 1, "escape changed profiles")
	m.key(key("enter"))
	m.key(key("right"))
	m.key(key("enter"))
	failIf(t, !m.editing || !m.addingBandwidthProfile, "New did not open editor")
	m.input = "Night"
	m.editKey(key("enter"))
	failIf(t, len(m.cfg.Bandwidth.Profiles) != 2 || m.cfg.Bandwidth.ActiveProfile != "Night", "new profile not staged")
	m.cursor = 1
	m.key(key("enter"))
	m.input = "Unlimited"
	m.editKey(key("enter"))
	failIf(t, m.err == "" || m.cfg.Bandwidth.ActiveProfile != "Night", "duplicate rename accepted")
	m.key(key("enter"))
	m.input = "Night cap"
	m.editKey(key("enter"))
	failIf(t, m.cfg.Bandwidth.ActiveProfile != "Night cap", "rename lost selection")
	for i, value := range []string{"64", "128"} {
		m.cursor = i + 2
		m.key(key("enter"))
		m.input = value
		m.editKey(key("enter"))
	}
	profile := m.cfg.Bandwidth.ActiveProfileLimits()
	failIfFmt(t, profile.UploadSpeedLimitKiB != 64 || profile.DownloadSpeedLimitKiB != 128, "limits not staged: %+v", profile)
	for _, value := range []string{"-1", "1000001", "oops"} {
		m.cursor = 3
		m.key(key("enter"))
		m.input = value
		m.editKey(key("enter"))
		failIf(t, m.err == "" || m.cfg.Bandwidth.ActiveProfileLimits() != profile, "invalid limit accepted")
	}
	m.settingsSection, m.cursor = settingsUploads, 0
	m.key(key("enter"))
	m.key(key("right"))
	m.key(key("enter"))
	m.cursor = 1
	m.key(key("enter"))
	m.key(key("left"))
	m.key(key("enter"))
	failIf(t, m.cfg.Uploads.LimitScope != config.UploadLimitPerTransfer || m.cfg.Uploads.Scheduling != config.UploadSchedulingSmallestFirst, "upload choices not staged")
	m.settingsSection, m.cursor = settingsBandwidth, 4
	m.key(key("enter"))
	failIf(t, len(m.cfg.Bandwidth.Profiles) != 1 || m.cfg.Bandwidth.ActiveProfile != "Unlimited", "delete lost adjacent selection")
	m.key(key("enter"))
	failIf(t, len(m.cfg.Bandwidth.Profiles) != 1 || m.notice == "", "last profile deleted")
	if _, err := os.Stat(m.configPath); !os.IsNotExist(err) {
		t.Fatal("staged settings saved prematurely")
	}
	m.cursor = 0
	m.key(key("left"))
	failIf(t, m.settingsSection != settingsConnection, "Bandwidth not after Connection")
	m.key(key("right"))
	m.key(key("right"))
	failIf(t, m.settingsSection != settingsDownloads, "Downloads not after Bandwidth")
	m.settingsSection, m.width, m.height = settingsBandwidth, 50, 12
	failIf(t, !strings.Contains(m.View().Content, "Bandwidth"), "narrow view lost section")
}

func TestChangePasswordForm(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "alice", "old-secret"
	m := newModel(context.Background(), nil, "", false, cfg)
	m.width, m.height, m.workspace, m.cursor = 80, 20, workspaceSettings, 1

	m.key(key("enter"))
	failIf(t, m.passwordForm || !strings.Contains(m.err, "Connect"), "disconnected password action was not rejected")
	m.status = snapshot{status: daemon.StatusConnected, user: "alice"}
	m.cfg.Soulseek.Username = "staged"
	m.key(key("enter"))
	failIf(t, m.passwordForm || !strings.Contains(m.err, "Save or revert"), "staged username password action was not rejected")
	m.cfg.Soulseek.Username = "alice"
	m.key(key("enter"))
	failIf(t, !m.passwordForm || m.passwordUser != "alice" || !strings.Contains(m.View().Content, "Username"), "password form did not open for the authenticated user")

	updated, _ := m.Update(tea.PasteMsg{Content: " new secret \n"})
	m = updated.(model)
	failIf(t, strings.Contains(m.View().Content, "new secret") || !strings.Contains(m.View().Content, "••••••••••••"), "new password paste was not masked")
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
	failIf(t, m.cfg.Soulseek.Password != "again" || !strings.Contains(m.err, "not saved"), "partial password change did not retain the credential and warning")

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
	failIf(t, !m.editing || !m.filterEditing, "f did not open filter editing")
	failIf(t, !strings.Contains(m.renderSearch(100, 10), "tab complete: in:"), "filter keywords were not shown")
	m.editKey(key("tab"))
	failIf(t, m.input != "in:" || m.workspace != workspaceSearch, "tab did not complete the first filter field")
	m.editKey(key("tab"))
	failIf(t, m.input != "out:", "repeated tab did not cycle filter fields")
	m.input = "ty"
	m.editKey(key("tab"))
	m.editKey(key("tab"))
	failIfFmt(t, m.input != "type:audio", "type completion = %q", m.input)
	m.input = "type:!a"
	m.editKey(key("tab"))
	failIfFmt(t, m.input != "type:!audio", "excluded type completion = %q", m.input)
	m.input = "cou"
	m.editKey(key("tab"))
	failIfFmt(t, m.input != "country:", "country completion = %q", m.input)
	m.input = "free:"
	m.editKey(key("tab"))
	failIfFmt(t, m.input != "free:true", "boolean completion = %q", m.input)
	m.input = "size:"
	m.editKey(key("tab"))
	failIfFmt(t, m.input != "size:>=", "comparison completion = %q", m.input)
	m.input = `type:audio bitrate:!0`
	if cmd := m.editKey(key("enter")); cmd != nil || m.searchFilter != `type:audio bitrate:!0` {
		t.Fatal("filter was not applied without a cached search")
	}
	m.key(key("f"))
	m.input = "size:nope"
	m.editKey(key("enter"))
	failIf(t, m.searchFilter != `type:audio bitrate:!0` || m.err == "", "invalid filter replaced the active filter")
	m.key(key("f"))
	m.input = "public:true"
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	failIf(t, m.searchFilter != `type:audio bitrate:!0`, "escape changed the active filter")
	m.key(key("c"))
	failIf(t, m.searchFilter != "" || m.searchFilterUndo == "", "c did not clear and remember the filter")
	m.key(key("c"))
	failIf(t, m.searchFilter != `type:audio bitrate:!0` || m.searchFilterUndo != "", "c did not restore the filter")

	m.loading = false
	m.searchTotal, m.searchFound = 1, 4
	m.results = []result{{user: "peer", path: `music\album\song.flac`, size: 1024, bitrate: 320, duration: 125, vbr: true}}
	m.searchTree, m.cursor = buildSearchTree(m.results, treeState{}, 0)
	m.cursor = m.searchTree.cursorForSource(0)
	view := m.renderSearch(100, 10)
	for _, want := range []string{"1 loaded / 1 filtered / 4 found", "FILE", "SIZE", `SOURCE  peer  •  music\album`, "song.flac", "320kv", "2:05", "private"} {
		failIfFmt(t, !strings.Contains(view, want), "search metadata missing %q in %q", want, view)
	}
	for _, line := range strings.Split(view, "\n") {
		failIfFmt(t, lipgloss.Width(line) > 100, "search row exceeds width: %d %q", lipgloss.Width(line), line)
	}
	failIfFmt(t, !strings.Contains(view, "› ○ ·       song.flac"), "filename was not left-aligned: %q", view)
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
	failIf(t, len(m.results) != 2 || m.results[1].country != "CA" || m.searchTotal != 2 || m.searchFound != 4, "filtered page was not appended with its totals")
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
	failIfFmt(t, len(m.searchTabs) != 2 || !strings.Contains(m.renderSearch(100, 10), "first query"), "search tabs not rendered: tabs=%d", len(m.searchTabs))

	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModCtrl}))
	failIfFmt(t, m.query != "first query" || m.searchFilter != "type:audio", "first search tab state = %q %q", m.query, m.searchFilter)
	updated, _ := m.Update(searchMsg{request: secondRequest, operation: secondOperation, filter: "free:true", page: daemon.SearchPage{ID: "second", Query: "second query", Results: []daemon.SearchResult{{Path: "second.flac", Public: true}}, Total: 1, FoundTotal: 2}})
	m = updated.(model)
	failIfFmt(t, m.query != "first query" || len(m.results) != 0, "background search switched active tab: query=%q results=%d", m.query, len(m.results))
	updated, _ = m.Update(searchMsg{request: firstRequest, operation: firstOperation, filter: "type:audio", page: daemon.SearchPage{ID: "first", Query: "first query", Results: []daemon.SearchResult{{Path: "first.flac", Public: true}}, Total: 1, FoundTotal: 3}})
	m = updated.(model)
	m.selected[0] = true
	firstRoot := m.searchTree.nodes[m.searchTree.roots[0]].id
	m.searchTree.expanded[firstRoot] = false
	m.searchTree.rebuildVisible()
	m.cursor = 0

	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown, Mod: tea.ModCtrl}))
	failIfFmt(t, m.query != "second query" || m.searchFilter != "free:true" || len(m.results) != 1 || m.results[0].path != "second.flac", "second search tab state = %q %q %+v", m.query, m.searchFilter, m.results)
	oldOperation := m.searchTabs[m.searchTabIndex].operation
	m.loadingMore = true
	m.filterSearch("public:true")
	updated, _ = m.Update(searchMsg{request: secondRequest, operation: oldOperation, append: true, page: daemon.SearchPage{ID: "second", Results: []daemon.SearchResult{{Path: "stale.flac", Public: true}}}})
	m = updated.(model)
	failIfFmt(t, len(m.results) != 1 || m.results[0].path != "second.flac" || m.loadingMore, "stale pagination changed filtered tab: results=%+v loadingMore=%v", m.results, m.loadingMore)
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModCtrl}))
	failIfFmt(t, m.query != "first query" || !m.selected[0] || m.searchTree.expandedNode(m.searchTree.nodes[m.searchTree.roots[0]]), "first search selection was not restored: query=%q selected=%v", m.query, m.selected)
	m.key(tea.KeyPressMsg(tea.Key{Code: 'w', Mod: tea.ModCtrl}))
	failIfFmt(t, len(m.searchTabs) != 1 || m.query != "second query", "ctrl+w did not close search tab: query=%q tabs=%d", m.query, len(m.searchTabs))
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
	failIfFmt(t, cmd == nil || m.searchID != "cached" || m.searchFilter != "in:outta" || !m.loading || len(m.results) != 0, "pending filter was not scheduled: id=%q filter=%q loading=%v results=%v", m.searchID, m.searchFilter, m.loading, m.results)

	operation := m.searchTabs[0].operation
	updated, _ = m.Update(searchMsg{
		request: 1, operation: operation, filter: "in:outta", filterChange: true,
		page: daemon.SearchPage{ID: "cached", Total: 1, FoundTotal: 12, Results: []daemon.SearchResult{{Path: "outta.flac"}}},
	})
	m = updated.(model)
	failIfFmt(t, m.loading || len(m.results) != 1 || m.results[0].path != "outta.flac" || m.searchFilter != "in:outta", "pending filter result was not applied: loading=%v filter=%q results=%v", m.loading, m.searchFilter, m.results)
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
	failIfFmt(t, len(ids) != 2 || (ids[0] != "d1" && ids[1] != "d1") || (ids[0] != "d2" && ids[1] != "d2"), "recursive transfer action IDs = %v", ids)
	m.cursor = m.transferTrees[transferDownloads].cursorForSource(0)

	downloads := m.renderTransfers(100, 10)
	failIfFmt(t, !strings.Contains(downloads, "[↓ DOWNLOADS 2]") || !strings.Contains(downloads, "███░░░░░░░░░░░  25%") || !strings.Contains(downloads, "1.5 KiB/s") || !strings.Contains(downloads, "Elapsed 0:01  ETA 0:01") || !strings.Contains(downloads, "⠋") || strings.Contains(downloads, "shared.wav"), "download tab did not render progress and spinner correctly: %q", downloads)
	barColumn, bars := -1, 0
	for _, line := range strings.Split(downloads, "\n") {
		if i := strings.IndexAny(line, "█░"); i >= 0 {
			column := lipgloss.Width(line[:i])
			failIfFmt(t, barColumn >= 0 && column != barColumn, "progress bars are not aligned: columns %d and %d", barColumn, column)
			barColumn, bars = column, bars+1
		}
	}
	failIfFmt(t, bars < 2, "expected multiple progress bars: %q", downloads)
	for _, width := range []int{40, 100} {
		for _, line := range strings.Split(m.renderTransfers(width, 10), "\n") {
			failIfFmt(t, lipgloss.Width(line) > width, "transfer tree exceeds width %d: %q", width, line)
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
	failIfFmt(t, m.rows() != 2 || !strings.Contains(uploads, "[↑ UPLOADS 1]") || !strings.Contains(uploads, "shared.wav") || strings.Contains(uploads, "album.flac"), "upload tab did not isolate uploads: rows=%d view=%q", m.rows(), uploads)
	if _, node := m.transferTrees[transferUploads].node(m.transferTrees[transferUploads].cursorForSource(2)); node == nil || node.source != 2 {
		t.Fatal("upload tree did not map to source transfer")
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModCtrl}))
	failIfFmt(t, m.cursor != downloadCursor, "download cursor was not restored: %d", m.cursor)
	updated, _ := m.Update(tickMsg{})
	if next := updated.(model).renderTransfers(100, 10); !strings.Contains(next, "⠙") {
		t.Fatalf("spinner did not advance: %q", next)
	}

	next := toTransfers([]daemon.Transfer{{ID: "d1", SpeedBPS: 1024, ElapsedMS: timingValue(1500), ETASeconds: timingValue(1)}})
	failIfFmt(t, next[0].speed != 1024 || *next[0].elapsedMS != 1500 || *next[0].etaSeconds != 1, "daemon timing lost: %+v", next)

	t.Setenv("NO_COLOR", "")
	normal := transferResultRow("row", false, false, false)
	failedDownload := transferResultRow("row", false, false, true)
	failedUpload := transferResultRow("row", false, true, true)
	failIfFmt(t, normal == failedDownload || failedDownload != failedUpload, "failed transfer color was not distinct: normal=%q download=%q upload=%q", normal, failedDownload, failedUpload)
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
		failIfFmt(t, !strings.Contains(view, want), "file details missing %q: %q", want, view)
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	failIf(t, m.details, "escape did not close file details")

	m.workspace, m.browseUser = workspaceBrowse, "bob"
	m.entries = []entry{{name: `Private\demo.wav`, extension: "wav", size: 42, private: true, bitrate: 1411, vbrKnown: true}}
	m.browseTree, m.cursor = buildBrowseTree(m.entries, "", treeState{}, 0)
	m.cursor = m.browseTree.cursorForSource(0)
	m.key(key("i"))
	view = m.View().Content
	for _, want := range []string{"bob", "private", "1411 kbps CBR"} {
		failIfFmt(t, !strings.Contains(view, want), "browse details missing %q: %q", want, view)
	}
	failIfFmt(t, strings.Contains(view, "Country"), "unknown country was shown: %q", view)
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
	failIfFmt(t, m.cursor != 1, "browse folder cursor = %d", m.cursor)
	view := m.renderBrowse(100, 10)
	for _, want := range []string{"FOLDER  audio\\Hardstyle_320", "FILE", "SIZE", "RATE", "TIME", "STATUS", "song.mp3", "320kv", "2:05", "private"} {
		failIfFmt(t, !strings.Contains(view, want), "browse result UI missing %q in %q", want, view)
	}
	m.selected[2] = true
	folderID := treeID("browse-dir", `audio\Hardstyle_320`)
	m.browseTree.expanded[folderID] = false
	m.browseTree.rebuildVisible()
	m.openBrowse("LittleDeng", "", false)
	failIfFmt(t, len(m.browseTabs) != 2 || m.browseUser != "LittleDeng" || !strings.Contains(m.renderBrowse(100, 10), "nss"), "second user tab not retained: user=%q tabs=%d", m.browseUser, len(m.browseTabs))
	staleRequest := m.browseTabs[1].request
	m.openBrowse("LittleDeng", "", true)
	updated, _ = m.Update(browseMsg{user: "LittleDeng", request: staleRequest, entries: []entry{{name: "stale"}}})
	m = updated.(model)
	failIfFmt(t, len(m.entries) != 0 || !m.loading, "stale browse response replaced newer request: entries=%d loading=%v", len(m.entries), m.loading)
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModCtrl}))
	failIfFmt(t, m.browseUser != "nss" || m.cursor != 1 || !m.selected[2] || m.browseTree.expandedNode(m.browseTree.nodes[m.browseTree.byID[folderID]]), "user tab state was not restored: user=%q cursor=%d", m.browseUser, m.cursor)
	updated, _ = m.Update(browseMsg{user: "LittleDeng", request: m.browseTabs[1].request, entries: []entry{{name: "EDM", directory: true}}})
	m = updated.(model)
	failIfFmt(t, m.browseUser != "nss", "background browse response switched tabs to %q", m.browseUser)
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown, Mod: tea.ModCtrl}))
	failIfFmt(t, m.browseUser != "LittleDeng" || len(m.entries) != 1, "background user tab was not populated: user=%q entries=%d", m.browseUser, len(m.entries))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModCtrl}))
	m.key(tea.KeyPressMsg(tea.Key{Code: 'w', Mod: tea.ModCtrl}))
	failIfFmt(t, len(m.browseTabs) != 1 || m.browseUser != "LittleDeng", "ctrl+w did not close active user tab: user=%q tabs=%d", m.browseUser, len(m.browseTabs))
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
	failIfFmt(t, browseMatchCount(entries, "ALBUM") != 4 || !hasMusic || !hasDisc, "folder query did not retain its subtree and ancestors: %+v", filtered.byID)
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
	failIfFmt(t, !found[1] || !found[5] || len(found) != 2, "case-insensitive file matches = %+v", found)
	none, _ := buildBrowseTree(entries, "missing", treeState{}, 0)
	all, _ := buildBrowseTree(entries, "", treeState{}, 0)
	failIf(t, len(none.visible) != 0 || browseMatchCount(entries, "") != len(entries) || len(all.visible) <= len(filtered.visible), "zero-match or cleared Browse find was not represented correctly")
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
	failIf(t, m.browseFilter != "" || !m.selected[1], "Escape applied the Browse find or cleared selection")
	m.key(key("f"))
	m.input = "missing"
	m.editKey(key("enter"))
	failIf(t, !strings.Contains(m.renderBrowse(100, 15), "No matching shared files"), "zero-match Browse find message was not rendered")
	m.key(key("f"))
	m.input = " album "
	if cmd := m.editKey(key("enter")); cmd != nil || m.browseFilter != "album" || len(m.selected) != 0 || m.browseTabs[0].filter != "album" {
		t.Fatalf("Browse find was not applied locally: filter=%q selected=%v", m.browseFilter, m.selected)
	}
	view := m.renderBrowse(100, 15)
	failIfFmt(t, !strings.Contains(view, "4 matches / 6 items") || !strings.Contains(view, "f  album"), "Browse find UI missing count or query: %q", view)
	album := treeID("browse-dir", `Music\Album`)
	m.cursor = 0
	for cursor, index := range m.browseTree.visible {
		if m.browseTree.nodes[index].id == album {
			m.cursor = cursor
			break
		}
	}
	m.toggle()
	failIfFmt(t, !m.selected[1] || !m.selected[3] || len(m.selected) != 2, "filtered folder selected hidden files: %+v", m.selected)

	if cmd := m.openBrowse("peer", "", true); cmd == nil {
		t.Fatal("refresh did not start")
	}
	request := m.browseTabs[0].request
	refreshed := []entry{{name: `Music\Album`, directory: true}, {name: `Music\Album\new.flac`}, {name: `Other`, directory: true}}
	updated, _ := m.Update(browseMsg{user: "peer", request: request, entries: refreshed})
	m = updated.(model)
	failIf(t, m.browseFilter != "album" || m.browseTabs[0].filter != "album" || browseMatchCount(m.entries, m.browseFilter) != 2, "refresh did not reapply the tab's Browse find")
	m.openBrowse("other", "", false)
	failIf(t, m.browseFilter != "" || len(m.browseTabs) != 2, "new Browse tab inherited another tab's find")
	m.switchBrowseTab(-1)
	failIf(t, m.browseUser != "peer" || m.browseFilter != "album", "Browse find was not restored with its tab")
	if cmd := m.openBrowse("peer", `Music\Album`, false); cmd != nil || m.browseFilter != "" || m.browseTabs[0].filter != "" {
		t.Fatal("targeted Browse did not clear the local find")
	}
	_, node := m.browseTree.node(m.cursor)
	failIfFmt(t, node == nil || node.path != `Music\Album`, "target folder remained hidden: %+v", node)
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
		failIfFmt(t, !strings.Contains(view, want), "saved browse picker missing %q: %q", want, view)
	}
	m.key(key("j"))
	failIfFmt(t, m.cursor != 1, "saved browse cursor = %d", m.cursor)
	if cmd := m.key(key("enter")); cmd == nil || m.browseUser != "bob" || len(m.browseTabs) != 1 || !m.loading {
		t.Fatalf("saved browse did not open: user=%q tabs=%d loading=%v", m.browseUser, len(m.browseTabs), m.loading)
	}

	request := m.browseTabs[0].request
	updated, _ := m.Update(browseMsg{user: "bob", request: request, cached: true, savedAt: savedAt, revision: 7})
	m = updated.(model)
	view = m.renderBrowse(100, 12)
	failIfFmt(t, !m.browseLoaded || !m.browseCached || !strings.Contains(view, "bob (cached)") || !strings.Contains(view, "No shared files"), "cached empty browse not rendered: %q", view)
	if cmd := m.key(key("s")); cmd == nil {
		t.Fatal("loaded browse could not be saved")
	}
	updated, _ = m.Update(saveBrowseMsg{saved: daemon.SavedBrowse{Username: "bob", SavedAt: savedAt.Add(time.Minute)}})
	m = updated.(model)
	failIfFmt(t, !strings.Contains(m.notice, "Saved share list for bob"), "save notice = %q", m.notice)

	if cmd := m.key(key("r")); cmd == nil || !m.loading {
		t.Fatalf("cached browse refresh not started: loading=%v", m.loading)
	}
	request = m.browseTabs[0].request
	updated, _ = m.Update(browseMsg{user: "bob", request: request, revision: 8, entries: []entry{{name: "Music", directory: true}}})
	m = updated.(model)
	failIf(t, m.browseCached || strings.Contains(m.renderBrowse(100, 12), "bob (cached)"), "live refresh kept cached marker")
	m.closeBrowseTab()
	failIf(t, len(m.browseTabs) != 0 || m.browseUser != "" || !strings.Contains(m.renderBrowse(100, 12), "SAVED USER"), "closing final browse tab did not return to picker")
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
	failIfFmt(t, m.input != "public:" || m.workspace != workspaceSearch, "shift+tab completion = %q in workspace %d", m.input, m.workspace)
}

func TestTreeNavigationGroupingAndRecursiveSelection(t *testing.T) {
	results := []result{
		{user: "peer", path: `music/album/a.flac`},
		{user: "peer", path: `music\album\b.flac`},
		{user: "other", path: `music\album\a.flac`},
		{user: "peer", path: `music\album\a.flac`},
	}
	tree, _ := buildSearchTree(results, treeState{}, 0)
	failIfFmt(t, tree.nodes[tree.roots[0]].label != "peer" || len(tree.visible) != 10, "search tree grouping/order: roots=%v visible=%d", tree.roots, len(tree.visible))
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
	failIfFmt(t, !m.selected[0] || !m.selected[1] || !m.selected[3] || m.selected[2] || treeSelection(&m.searchTree, albumIndex, m.selected) != "●", "recursive folder selection = %v", m.selected)
	m.selected[1] = false
	failIf(t, treeSelection(&m.searchTree, albumIndex, m.selected) != "◐", "partial folder selection was not shown")
	m.cursor = 0
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	failIf(t, m.workspace != workspaceSearch || len(m.searchTree.visible) >= len(tree.visible), "left did not collapse the tree in place")
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	failIfFmt(t, m.cursor != 1, "right did not enter first child: %d", m.cursor)

	m.cursor = m.searchTree.cursorForSource(0)
	oldID := m.searchTree.cursorID(m.cursor)
	updated, updatedCursor := buildSearchTree(append(results, result{user: "peer", path: `music\new\c.flac`}), m.searchTree, m.cursor)
	failIf(t, updated.cursorID(updatedCursor) != oldID, "pagination did not preserve the cursor by node identity")
	updated.expanded[albumID] = false
	updated, _ = buildSearchTree(append(results, result{user: "peer", path: `music\other\d.flac`}), updated, updatedCursor)
	failIf(t, updated.expanded[albumID], "pagination discarded an explicit collapsed state")
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
	failIf(t, m.folderMenu, "escape did not close folder menu")
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
	failIf(t, len(m.shareTree.nodes) != 1, "stale share response populated the tree")
	current, _ := m.Update(shareBrowseMsg{nodeID: root.id, generation: 2, request: 7, entries: []entry{{name: "Album", directory: true}, {name: "song.flac", size: 42}}})
	m = current.(model)
	failIfFmt(t, len(m.shareTree.nodes) != 3 || !strings.Contains(m.renderShares(80, 10), "song.flac"), "current share response was not rendered: %q", m.renderShares(80, 10))
	for _, line := range strings.Split(m.renderShares(40, 10), "\n") {
		failIfFmt(t, lipgloss.Width(line) > 40, "share tree exceeds width: %q", line)
	}
	polled, _ := m.Update(sharesMsg{shares: m.shares})
	m = polled.(model)
	failIf(t, len(m.shareTree.nodes) != 3, "ordinary share polling discarded loaded children")
	changed, _ := m.Update(sharesMsg{shares: append(m.shares, share{name: "Other", path: "/other"})})
	m = changed.(model)
	failIf(t, len(m.shareTree.nodes) != 4, "adding a root discarded an unchanged loaded subtree")
	removed, _ := m.Update(sharesMsg{shares: []share{{name: "Other", path: "/other"}}})
	m = removed.(model)
	failIf(t, len(m.shareTree.nodes) != 1 || m.shareTree.nodes[m.shareTree.roots[0]].label != "Other", "removed share root remained in the tree")
	reset, _ := m.Update(sharesMsg{shares: m.shares, reset: true})
	m = reset.(model)
	failIf(t, len(m.shareTree.nodes) != 1 || m.shareTree.expandedNode(m.shareTree.nodes[m.shareTree.roots[0]]), "rescan did not invalidate and collapse share children")
}

func TestDownloadSettingsCommandsAndStates(t *testing.T) {
	cfg := config.Default()
	cfg.Downloads.AfterFileCommand, cfg.Downloads.AfterFolderCommand = `echo "$1"`, `echo folder "$1"`
	m := model{workspace: workspaceSettings, settingsSection: settingsDownloads, cfg: cfg}
	fields := m.settingFields()
	failIfFmt(t, len(fields) < 6 || fields[1].value != cfg.Downloads.AfterFileCommand || fields[2].value != cfg.Downloads.AfterFolderCommand, "download settings not rendered: %+v", fields)
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
	failIfFmt(t, !strings.Contains(hints, "p pause") || !strings.Contains(hints, "r resume"), "download transfer hints missing: %q", hints)
	m.transfers = []transfer{{direction: "download", state: "paused"}}
	failIf(t, m.active(), "paused downloads must not trigger the active-transfer quit warning")
	m.transfers[0].state = "retrying"
	failIf(t, !m.active(), "retrying downloads should remain active")
}

func TestBrowseSettingsEdits(t *testing.T) {
	m := model{cfg: config.Default(), workspace: workspaceSettings, settingsSection: settingsBrowse}
	fields := m.settingFields()
	failIfFmt(t, len(fields) != 3, "browse fields: %+v", fields)
	for i, value := range []string{"123", "12", "34"} {
		m.cursor = i
		must(t, m.setSettingValue(value))
	}
	if m.cfg.Browse != (config.Browse{MaxEntries: 123, MaxCompressedMiB: 12, MaxDecompressedMiB: 34}) {
		t.Fatalf("browse edits: %+v", m.cfg.Browse)
	}
	for i, ceiling := range []int{10_000_000, 256, 1024} {
		m.cursor = i
		for _, value := range []string{"0", "-1", "no", fmt.Sprint(ceiling + 1)} {
			before := m.cfg.Browse
			if err := m.setSettingValue(value); err == nil || m.cfg.Browse != before {
				t.Fatalf("invalid browse field %d = %q was adopted", i, value)
			}
		}
	}
	if got := m.settingFields()[0].label; got != "Max entries (files + folders)" {
		t.Fatalf("entry label: %q", got)
	}
}

func TestIntegerSettingsValidation(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, test := range []struct {
		section  settingsSection
		field    settingID
		min, max int
	}{
		{settingsStatistics, settingStatsLogRetention, 0, 365000},
		{settingsStatistics, settingStatsDailyRetention, 0, 365000},
		{settingsBrowse, settingBrowseMaxEntries, 1, 10000000},
		{settingsBrowse, settingBrowseMaxCompressedMiB, 1, 256},
		{settingsBrowse, settingBrowseMaxDecompressedMiB, 1, 1024},
		{settingsSearch, settingMinimumIncomingSearchLength, 0, 50},
		{settingsSearch, settingMaximumIncomingSearchResults, 50, 10000},
		{settingsSearch, settingSearchHistoryLimit, 0, maxInt},
		{settingsSearch, settingFilterHistoryLimit, 0, maxInt},
		{settingsSearch, settingWishlistInterval, 0, 525600},
		{settingsBandwidth, settingUploadSpeedLimit, 0, 1000000},
		{settingsBandwidth, settingDownloadSpeedLimit, 0, 1000000},
	} {
		t.Run(fmt.Sprint(test.field), func(t *testing.T) {
			m := model{cfg: config.Default(), settingsSection: test.section, cursor: -1}
			m.cfg.Bandwidth.Profiles = append(m.cfg.Bandwidth.Profiles, config.BandwidthProfile{Name: "Other"})
			m.cfg.Bandwidth.ActiveProfile = "Other"
			for i, field := range m.settingFields() {
				if field.id == test.field {
					m.cursor = i
				}
			}
			failIf(t, m.cursor < 0, "setting not found")
			for _, n := range []int{test.min, test.max} {
				value := fmt.Sprint(n)
				if err := m.setSettingValue(value); err != nil || m.settingFields()[m.cursor].value != value {
					t.Fatalf("valid value %q: %v", value, err)
				}
			}
			before := m.cfg.Redacted()
			for _, value := range []string{"", "no", fmt.Sprint(test.min - 1), fmt.Sprint(uint64(test.max) + 1)} {
				if err := m.setSettingValue(value); err == nil || !reflect.DeepEqual(m.cfg.Redacted(), before) {
					t.Fatalf("invalid value %q accepted or changed config: %v", value, err)
				}
			}
			failIf(t, m.cfg.Bandwidth.Profiles[0] != config.Default().Bandwidth.Profiles[0], "edited the inactive profile")
		})
	}
}
