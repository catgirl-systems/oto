package tui

import (
	"strings"

	"charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
)

// searchKey handles keys specific to the Search workspace. It reports false
// for keys left to the shared navigation and global bindings.
func (m *model) searchKey(s string) (tea.Cmd, bool) {
	switch s {
	case "ctrl+w":
		return m.closeSearchTab(), true
	case "down", "j", "pgdown":
		// Moving past the last row loads the next page of results.
		if m.cursor+1 >= m.rows() && !m.loadingMore && m.searchNext > 0 {
			m.loadingMore = true
			return m.loadSearchPage(), true
		}
		return nil, false
	case "b":
		_, node := m.searchTree.node(m.cursor)
		if node != nil {
			folder := node.path
			if node.kind == treeFile {
				folder, _ = resultPath(folder)
			}
			return m.openBrowse(node.user, folder, false), true
		}
	case "i":
		m.showFileDetails()
	case "d":
		if m.openFolderMenu() {
			return nil, true
		}
		return m.queueResult(), true
	case "c":
		filter := m.searchFilterUndo
		if m.searchFilter != "" {
			filter = ""
		}
		if m.searchID == "" {
			m.searchFilter, m.searchFilterUndo = filter, m.searchFilter
			if len(m.searchTabs) == 0 {
				m.searchPreFilterSet = true
			} else if m.searchTabIndex >= 0 && m.searchTabIndex < len(m.searchTabs) {
				m.searchTabs[m.searchTabIndex].filter = filter
			}
			return nil, true
		}
		m.loading = true
		return m.filterSearch(filter), true
	case "D":
		m.openDownloadAs()
	case "f":
		m.editing, m.filterEditing, m.input = true, true, m.searchFilter
		m.inputCursor = len([]rune(m.input))
		m.historyCursor.reset(m.input)
	case "w":
		if strings.TrimSpace(m.query) == "" {
			return nil, true
		}
		if len(m.searchUsers()) > 0 || m.searchTabIndex >= 0 && m.searchTabIndex < len(m.searchTabs) && m.searchTabs[m.searchTabIndex].scope != "" && m.searchTabs[m.searchTabIndex].scope != "global" {
			m.setNotice("Wishlist searches are global; targeted searches cannot be saved")
			return nil, true
		}
		return m.putWishlist(m.query, m.searchFilter, "Saved search to Wishlist"), true
	default:
		return nil, false
	}
	return nil, true
}

// showFileDetails opens the info panel for the file under the cursor.
func (m *model) showFileDetails() {
	_, node := m.currentTree().node(m.cursor)
	m.details = node != nil && node.kind == treeFile && node.source >= 0
}

func (m model) searchHints() []string {
	var hints []string
	if _, node := m.searchTree.node(m.cursor); node != nil {
		if node.kind == treeFile {
			hints = []string{"enter download", "space mark", "D save as", "i info", "b browse"}
		} else if node.kind == treeFolder {
			hints = []string{"enter expand", "d folder download", "space mark", "b browse"}
		} else {
			hints = []string{"enter expand", "d download", "space mark", "b browse"}
		}
	}
	hints = append(hints, "/ search", "f filter", "w wishlist", "U user")
	if len(m.searchTabs) > 1 {
		hints = append(hints, "[/] tab", "ctrl+w close")
	}
	return hints
}

func (m *model) applySearch(x searchMsg) tea.Cmd {
	if x.request != 0 {
		if m.workspace == workspaceSearch {
			m.saveSearchTab()
		}
		for i := range m.searchTabs {
			tab := &m.searchTabs[i]
			if tab.request != x.request || tab.operation != x.operation {
				continue
			}
			if x.err == nil && x.page.CommunityIdentity != (daemon.CommunityIdentity{}) && x.page.CommunityIdentity != m.community.summary.CommunityIdentity {
				x.err = daemon.ErrCommunitySession
				x.page = daemon.SearchPage{}
			}
			if !x.append && !x.filterChange && x.err == nil && tab.filter != x.filter {
				cmd := m.refilterPendingSearch(tab, x.page)
				if m.workspace == workspaceSearch && i == m.searchTabIndex {
					m.loadSearchTab(i)
				}
				return cmd
			}
			applySearchMsg(tab, x)
			if m.workspace == workspaceSearch && i == m.searchTabIndex {
				m.loadSearchTab(i)
			}
			if x.err == nil && x.page.Searching {
				return searchRefreshAfter(tab.request, tab.operation)
			}
			break
		}
		return nil
	}
	m.err = errText(x.err)
	if x.append {
		m.loadingMore = false
	} else {
		m.loading = false
	}
	if x.err != nil {
		return nil
	}
	results := toResults(x.page.Results)
	if x.append {
		oldLen := len(m.results)
		m.results = append(m.results, results...)
		if m.cursor == oldLen-1 && len(m.results) > oldLen {
			m.cursor++
		}
	} else {
		m.results, m.cursor, m.selected = results, 0, map[int]bool{}
	}
	if x.filterChange {
		if x.filter == "" && m.searchFilter != "" {
			m.searchFilterUndo = m.searchFilter
		} else if m.searchFilter == "" && x.filter == m.searchFilterUndo {
			m.searchFilterUndo = ""
		}
		m.searchFilter = x.filter
	}
	m.searchID, m.searchTotal, m.searchFound, m.searchNext = x.page.ID, x.page.Total, x.page.FoundTotal, x.page.NextCursor
	m.searchTree, m.cursor = buildSearchTree(m.results, m.searchTree, m.cursor)
	return nil
}
