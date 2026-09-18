package tui

import (
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/stats"
)

func TestSelectedFilteredFilesAndScopeConfirmation(t *testing.T) {
	m := model{cfg: config.Default(), workspace: workspaceTransfers, transferTab: transferDownloads, width: 80, height: 24}
	m.transfers = []transfer{{id: "d-1", user: "Alice Smith", filename: "Music/a.exe", direction: "download", state: "filtered"}, {id: "d-2", user: "Alice Smith", filename: "Music/b.exe", direction: "download", state: "filtered"}}
	m.transferTrees[transferDownloads], m.cursor = buildTransferTree(m.transfers, "download", treeState{}, 0)
	m.confirmForceDownloads()
	failIf(t, m.uploadConfirm, "folder implicitly bypassed")
	for i := range m.transfers {
		m.cursor = m.transferTrees[transferDownloads].cursorForSource(i)
		m.toggleDownloadSelection()
	}
	m.confirmForceDownloads()
	failIf(t, !m.uploadConfirm || m.uploadConfirmChoice != 0 || len(m.forcePending) != 2, "missing explicit multi-file confirmation")
	if cmd := m.uploadConfirmKey(key("enter")); cmd != nil || m.forcePending != nil {
		t.Fatal("default confirmation was not No")
	}
	m.openSearchScope()
	failIfFmt(t, m.searchScope == nil || len(m.searchScope.users) != 1 || m.searchScope.users[0] != "Alice Smith", "target scope: %+v", m.searchScope)
	if cmd := m.searchScopeKey(key("esc")); cmd != nil || m.searchScope != nil {
		t.Fatal("cancelled scope produced work")
	}
	m.openSearchScope()
	m.searchScope.value = "song"
	if cmd := m.searchScopeKey(key("enter")); cmd == nil {
		t.Fatal("explicit search did not submit")
	}
	failIf(t, len(m.searchUsers()) != 1 || m.searchUsers()[0] != "Alice Smith", "tab lost target scope")
	if cmd := m.key(key("w")); cmd != nil || !strings.Contains(m.notice, "global") {
		t.Fatal("targeted wishlist silently became global")
	}
}

func TestStatsPageNavigation(t *testing.T) {
	for action, step := range map[string]int{"ctrl+pgup": len(statsPages) - 1, "ctrl+pgdown": 1} {
		for page := range len(statsPages) {
			m := model{workspace: workspaceStats, cursor: 5}
			m.stats.page = page
			m.stats.filter = stats.Filter{Direction: "upload", Kinds: []string{"failed"}, Cursor: "next"}
			want := (page + step) % len(statsPages)
			if cmd := m.statsKey(key(action)); cmd == nil || m.stats.page != want || m.cursor != 0 || m.stats.filter.Cursor != "" {
				t.Fatalf("%s from page %d: page=%d cursor=%d filter=%+v", action, page, m.stats.page, m.cursor, m.stats.filter)
			}
			failIfFmt(t, (len(m.stats.filter.Kinds) > 0) != (want == 3) || (m.stats.filter.Direction == "") != (want < 2), "%s to page %d retained inappropriate filters: %+v", action, want, m.stats.filter)
		}
	}
}
