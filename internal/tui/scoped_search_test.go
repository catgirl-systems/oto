package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestScopedSearchRejectsLateSessionBeforeRefilter(t *testing.T) {
	old := daemon.CommunityIdentity{Account: "account", Daemon: "daemon", Session: 1}
	current := old
	current.Session++
	m := model{workspace: workspaceSearch, searchTabIndex: 0, searchFilter: "type:audio", loading: true}
	m.community.summary.CommunityIdentity = current
	m.searchTabs = []searchTab{{identity: old, scope: "rooms", request: 1, operation: 1, loading: true, searching: true, filter: "type:audio"}}
	updated, cmd := m.Update(searchMsg{request: 1, operation: 1, page: daemon.SearchPage{SearchContext: daemon.SearchContext{CommunityIdentity: old, Scope: "rooms"}}})
	got := updated.(model)
	failIf(t, cmd != nil || got.searchTabs[0].loading || got.searchTabs[0].searching || !strings.Contains(got.searchTabs[0].err, "session"), "late response retained or refiltered", got.searchTabs[0], cmd != nil)
}

func TestScopedSearchContextPasteAndWishlist(t *testing.T) {
	for _, view := range []int{1, 2} {
		m := model{workspace: workspaceCommunity, width: 80, height: 24}
		m.community.view = view
		m.community.summary.CommunityIdentity = daemon.CommunityIdentity{Account: "account", Daemon: "daemon", Session: 1}
		m.community.rooms.rooms = []daemon.CommunityRoom{{Name: "lounge", Joined: true, State: "joined"}}
		m.key(key("u"))
		failIf(t, m.searchScope == nil, "missing contextual scope")
		want := "rooms"
		if view == 2 {
			want = "buddies"
		}
		failIf(t, m.searchScope.mode() != want, m.searchScope.mode())
		updated, cmd := m.Update(tea.PasteMsg{Content: "song"})
		m = updated.(model)
		failIf(t, cmd != nil || m.searchScope.value != "song" || len(m.searchTabs) != 0, "paste submitted or disappeared")
		failIf(t, m.searchScopeKey(key("enter")) == nil, "search not submitted")
		if cmd := m.key(key("w")); cmd != nil || !strings.Contains(m.notice, "global") {
			t.Fatal("scoped wishlist became global")
		}
		m.openSearchScope()
		failIf(t, m.searchScope.mode() != want, "scope lost on reopen")
		m.community.summary.Session++
		failIf(t, m.submitSearchScope() != nil || !strings.Contains(m.searchScope.err, "session"), "stale editor submitted")
	}
}
func TestScopedSearchInputAndSmallLayout(t *testing.T) {
	d := &searchScope{scope: "global", row: 0}
	m := model{searchScope: d, width: 20, height: 6}
	for _, want := range []string{"users", "buddies", "rooms", "global"} {
		m.searchScopeKey(key("enter"))
		failIf(t, d.mode() != want, d.mode(), want)
	}
	d.row = 2
	d.rooms = []string{"lounge"}
	d.value = "lounge"
	d.setInput(strings.Repeat("x", 25), 25)
	failIf(t, d.value != "lounge" || d.err == "", "room input silently renamed")
	failIf(t, lipgloss.Width(m.searchScopeView()) > 20 || lipgloss.Height(m.searchScopeView()) > 6, "small terminal overflow")
}
