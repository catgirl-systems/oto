package tui

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/ipc"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/charmbracelet/x/ansi"
)

func communityViewModel() model {
	m := newModel(context.Background(), nil, "", false, config.Default())
	m.workspace, m.width, m.height = workspaceCommunity, 120, 40
	m.community.ready = true
	m.community.summary = daemon.CommunitySummary{CommunityIdentity: daemon.CommunityIdentity{Account: "server/User", Daemon: "process", Session: 3}, Connected: true, Revision: 4, Capabilities: []string{"users", "watches"}}
	return m
}

func TestCommunityResponsiveLayout(t *testing.T) {
	for _, color := range []string{"", "1"} {
		t.Setenv("NO_COLOR", color)
		for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {36, 8}, {20, 6}} {
			for pane := range 3 {
				m := communityViewModel()
				m.width, m.height = size[0], size[1]
				m.community.pane, m.community.target = pane, "猫😀"
				m.community.summary.Unread, m.community.summary.Mentions = 3, 1
				view := m.mainView()
				failIfFmt(t, lipgloss.Width(view) > m.width || lipgloss.Height(view) > m.height, "%v pane %d: %dx%d\n%s", size, pane, lipgloss.Width(view), lipgloss.Height(view), view)
				failIfFmt(t, !strings.Contains(ansi.Strip(view), "[Community]"), "active workspace missing: %v\n%s", size, view)
				if m.width < 36 {
					continue
				}
				failIfFmt(t, !strings.Contains(view, "Unread:3"), "unread hidden: %v\n%s", size, view)
				if m.height < 10 {
					continue
				}
				failIf(t, m.width >= 110 && !strings.Contains(view, " │ "), "wide pane separation missing")
				if m.width < 80 || m.width < 110 && pane == 2 {
					failIfFmt(t, !strings.Contains(view, communityPanes[pane]) || !strings.Contains(view, "Esc back"), "focused pane/back hidden: %v\n%s", size, view)
				}
			}
		}
	}
	for width := 34; width < 150; width++ {
		for workspace := workspaceSearch; workspace < workspaceCount; workspace++ {
			m := communityViewModel()
			m.workspace = workspace
			m.community.summary.Unread, m.community.summary.Mentions = 1<<40, 1<<40
			bar := ansi.Strip(m.workspaceTabs(width))
			name := strings.Fields(m.workspaceNames()[workspace])[0]
			failIfFmt(t, ansi.StringWidth(bar) > width || !strings.Contains(bar, "["+name) || (!strings.Contains(bar, "Unread:") && !strings.Contains(bar, "U:")), "%d %s: %q", width, name, bar)
		}
	}
}

func TestCommunityNavigationAndInputPrecedence(t *testing.T) {
	m := communityViewModel()
	m.workspace = workspaceTransfers
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	failIf(t, m.workspace != workspaceCommunity, "Community does not follow Transfers")
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown, Mod: tea.ModCtrl}))
	failIf(t, m.community.view != 1, "room navigation")
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModCtrl}))
	failIf(t, m.community.view != 0, "chat navigation")
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyF6}))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyF6}))
	failIf(t, m.community.pane != 2, "inspector focus")
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyF6, Mod: tea.ModShift}))
	failIf(t, m.community.pane != 1, "reverse pane focus")
	m.key(tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}))
	for _, r := range "q/?猫😀" {
		if cmd := m.key(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)})); cmd != nil {
			t.Fatal("input escaped to a global action")
		}
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	failIf(t, m.workspace != workspaceCommunity || m.help || m.community.input != "q/?猫😀", "editing precedence")
	updated, cmd := m.Update(tea.PasteMsg{Content: "more"})
	m = updated.(model)
	failIf(t, cmd != nil || !m.community.inspectEditing || m.community.input != "q/?猫😀more", "paste submitted or lost input")
	updated, _ = m.Update(tea.PasteMsg{Content: "one\ntwo"})
	m = updated.(model)
	failIf(t, m.community.inputErr == "" || m.community.input != "q/?猫😀more", "multiline username accepted")
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 16})
	m = updated.(model)
	failIf(t, !strings.Contains(m.mainView(), "q/?猫😀more") || !m.community.inspectEditing, "resize lost input")
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	failIf(t, m.workspace != workspaceStats || m.community.pane != 1, "navigation state lost")
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	failIf(t, m.workspace != workspaceCommunity || m.community.pane != 1, "reverse workspace navigation")
}

func TestCommunityStaleResponsesAndPartialState(t *testing.T) {
	m := communityViewModel()
	c := &m.community
	c.summaryRequest, c.userRequest = 7, 9
	c.target, c.pane, c.inspectorScroll, c.input = "Alice", 2, 3, "draft field"
	identity := c.summary.CommunityIdentity
	page := daemon.CommunityUsersPage{CommunityIdentity: identity, Revision: 6, Users: []daemon.CommunityUser{{Username: "Alice", Exists: true, Status: soulseek.UserStatusOnline, StatusFresh: true, StatusUpdatedAt: time.Now()}}}
	m.applyCommunityUser(communityUserMsg{request: 8, identity: identity, username: "Alice", page: page})
	failIf(t, c.user.Username != "", "old request accepted")
	m.applyCommunityUser(communityUserMsg{request: 9, identity: identity, username: "alice", page: page})
	failIf(t, c.user.Username != "", "folded username accepted")
	m.applyCommunityUser(communityUserMsg{request: 9, identity: identity, username: "Alice", page: page})
	failIf(t, c.user.Username != "Alice" || c.pane != 2 || c.inspectorScroll != 3 || c.input != "draft field", "resource refresh lost focus/state")
	m.applyCommunitySummary(communitySummaryMsg{request: 6, err: errors.New("obsolete error")})
	failIf(t, c.err != "", "obsolete error published")
	offline := c.summary
	offline.Connected, offline.Revision = false, 8
	m.applyCommunitySummary(communitySummaryMsg{request: 7, summary: offline})
	if text := strings.Join(c.inspectorLines(), "\n"); !strings.Contains(text, "online (stale)") || !strings.Contains(text, "Shares/speed: unknown") || strings.Contains(text, "Last seen") {
		t.Fatalf("partial/stale data: %s", text)
	}
	m.applyCommunitySummary(communitySummaryMsg{request: 7, summary: daemon.CommunitySummary{CommunityIdentity: identity, Revision: 1}})
	failIf(t, c.summary.Revision != 8, "revision regressed")
	reconnected := offline
	reconnected.Session++
	cancelled := false
	c.userCancel = func() { cancelled = true }
	m.applyCommunitySummary(communitySummaryMsg{request: 7, summary: reconnected})
	m.applyCommunityUser(communityUserMsg{request: c.userRequest, identity: identity, username: "Alice", page: page})
	failIf(t, !cancelled || c.user.Username != "" || c.target != "Alice" || c.pane != 2 || c.input != "draft field", "session invalidation or local state preservation")
	reconnected.Account = "server/another-account"
	m.applyCommunitySummary(communitySummaryMsg{request: 7, summary: reconnected})
	failIf(t, c.target != "" || c.input != "", "cross-account input leaked")
	m.applyCommunitySummary(communitySummaryMsg{request: 7, err: errors.New("404 Not Found")})
	failIf(t, c.supports("users") || !strings.Contains(m.mainView(), "Community unavailable"), "unsupported daemon not actionable")
}

func TestCommunityContextualUserActions(t *testing.T) {
	for _, workspace := range []workspace{workspaceSearch, workspaceBrowse, workspaceTransfers, workspaceCommunity} {
		m := communityViewModel()
		m.workspace = workspace
		username := "Alice猫"
		m.browseUser, m.community.target = username, username
		tree := treeState{nodes: []treeNode{{user: username}}, visible: []int{0}}
		m.searchTree, m.transferTrees[transferDownloads] = tree, tree
		m.cursor, m.selected = 0, map[int]bool{5: true}
		m.key(tea.KeyPressMsg(tea.Key{Code: 'U', Text: "U"}))
		failIfFmt(t, m.userActions == nil || m.userActions.username != username || !strings.Contains(m.userActionsView(), username), "missing context in workspace %v", workspace)
		// Async data and global shortcuts must not change the captured target.
		m.community.target, m.browseUser = "unrelated", "unrelated"
		m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
		m.key(tea.KeyPressMsg(tea.Key{Code: 'q', Text: "q"}))
		failIf(t, m.workspace != workspace || !m.selected[5] || m.userActions.username != username, "modal navigation changed its source")
		m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		failIf(t, m.workspace != workspaceCommunity || m.community.target != username || m.community.pane != 2 || m.userActions != nil, "inspector action failed")
		m.openUserActions()
		m.userActions.row = 2
		m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		if m.searchScope == nil || m.searchScope.mode() == "global" || !reflect.DeepEqual(m.searchScope.users, []string{username}) {
			t.Fatal("search context lost or went global")
		}
	}
	m := communityViewModel()
	m.community.target = "Alice"
	m.openUserActions()
	m.community.summary.Session++
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	failIf(t, m.userActions == nil || !strings.Contains(m.userActions.err, "changed"), "stale menu performed action")
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	m.community.summary.Capabilities = nil
	m.openUserActions()
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	failIf(t, m.userActions == nil || !strings.Contains(m.userActions.err, "unavailable"), "capability gate missing")
	m.userActions = nil
	m.workspace, m.community.target, m.browseUser = workspaceBrowse, "", "Alice"
	m.browseTabs = []browseTab{{user: "Alice", loaded: true}, {user: "alice", loaded: true}}
	m.openBrowse("alice", "", false)
	failIf(t, m.browseTabIndex != 1 || m.browseUser != "alice", "case-folded browse tab became Community identity")
}

func TestCommunityRealIPCRefreshAndFrontendIsolation(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password, cfg.DownloadDir = "local", "local-only", t.TempDir()
	service, err := daemon.New(cfg, filepath.Join(t.TempDir(), "config.json"))
	must(t, err)
	path := filepath.Join(t.TempDir(), "ipc.sock")
	server := ipc.NewServer(service, path)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-done; _ = service.Close() })
	client := ipc.NewClient(path)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err = client.CommunitySummary(ctx); err == nil {
			break
		}
		failIf(t, time.Now().After(deadline), err)
		time.Sleep(time.Millisecond)
	}
	var frontends []string
	for _, username := range []string{"Alice", "alice"} {
		m := newModel(ctx, client, "", false, cfg)
		cmd := m.loadCommunitySummary()
		updated, _ := m.Update(cmd())
		m = updated.(model)
		failIf(t, !m.community.ready, "summary command not applied")
		cmd = m.openUserInspector(username)
		failIf(t, cmd == nil, "user resource command missing")
		if again := m.loadCommunityUser(); again != nil {
			t.Fatal("unbounded concurrent refresh")
		}
		drainChat(t, &m, cmd)
		failIfFmt(t, m.community.userErr != "" || m.community.user.Username != username || m.community.user.StatusFresh, "IPC resource: %+v", m.community)
		frontends = append(frontends, m.community.frontend)
		// Leaving cancels stale work, not the other frontend's lease.
		m.switchWorkspace(workspaceSearch)
	}
	failIf(t, frontends[0] == frontends[1], "frontends share a lease")
	summary, err := client.CommunitySummary(ctx)
	must(t, err)
	users, err := client.CommunityUsers(ctx, daemon.CommunityUsersRequest{CommunityIdentity: summary.CommunityIdentity})
	failIfFmt(t, err != nil || len(users.Users) != 2, "frontend watch isolation: %+v %v", users, err)
}

func TestCommunityInspectorControlSafety(t *testing.T) {
	m := communityViewModel()
	m.community.target = "Alice\x1b[31m\n"
	m.community.userErr = "failure\x1b]52;c;ignored\a\x00\nretry"
	m.community.user.Country = "\x1b[2J"
	text := strings.Join(m.community.inspectorLines(), "\n")
	failIfFmt(t, strings.ContainsAny(text, "\x1b\x00\a\r"), "terminal control in data: %q", text)
	m.community.target = strings.Repeat("猫", 300)
	for _, width := range []int{40, 80, 120} {
		m.width, m.height, m.community.pane = width, 16, 2
		if view := m.mainView(); lipgloss.Width(view) > width || lipgloss.Height(view) > 16 {
			t.Fatalf("long Unicode identity at %d: %s", width, view)
		}
		m.community.userErr = ""
		m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
		if view := m.mainView(); !strings.Contains(view, "user actions") {
			t.Fatalf("cannot reach the end of wrapped inspector at %d:\n%s", width, view)
		}
		m.openUserActions()
		if view := m.userActionsView(); !strings.Contains(view, "Inspect user") || !strings.Contains(view, "Esc back") {
			t.Fatalf("long identity hides menu actions at %d:\n%s", width, view)
		}
		m.userActions = nil
	}
}

func TestCommunityMissingUserResponseDoesNotRefreshOldData(t *testing.T) {
	for _, users := range [][]daemon.CommunityUser{nil, {{Username: "alice"}}} {
		m := communityViewModel()
		c := &m.community
		c.target = "Alice"
		c.user = daemon.CommunityUser{Username: "Alice", Exists: true, StatusFresh: true, Status: soulseek.UserStatusOnline}
		c.userRefreshed = time.Now()
		m.applyCommunityUser(communityUserMsg{identity: c.summary.CommunityIdentity, username: "Alice", page: daemon.CommunityUsersPage{CommunityIdentity: c.summary.CommunityIdentity, Users: users}})
		failIfFmt(t, c.user.Username != "" || c.userErr == "" || !c.userRefreshed.IsZero(), "missing response retained fresh data: %+v", c)
	}
	m := communityViewModel()
	c := &m.community
	c.target = "Alice"
	c.user = daemon.CommunityUser{Username: "Alice", Exists: true, StatusFresh: true, Status: soulseek.UserStatusOnline, StatsFresh: true}
	refreshed := time.Now().Add(-time.Minute)
	c.userRefreshed = refreshed
	wrong := c.summary.CommunityIdentity
	wrong.Session++
	m.applyCommunityUser(communityUserMsg{identity: c.summary.CommunityIdentity, username: "Alice", page: daemon.CommunityUsersPage{CommunityIdentity: wrong}})
	text := strings.Join(c.inspectorLines(), "\n")
	failIfFmt(t, c.user.Username != "Alice" || !c.userRefreshed.Equal(refreshed) || !strings.Contains(c.userErr, "session") || !strings.Contains(text, "online (stale)") || !strings.Contains(text, "Speed (stale)"), "mismatched response freshness: %s", text)
}
