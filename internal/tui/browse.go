package tui

import (
	"strings"

	"charm.land/bubbletea/v2"
)

// browseKey handles keys specific to the Browse workspace. It reports false
// for keys left to the shared navigation and global bindings.
func (m *model) browseKey(s string) (tea.Cmd, bool) {
	switch s {
	case "ctrl+w":
		m.closeBrowseTab()
	case "enter":
		if len(m.browseTabs) > 0 {
			return nil, false
		}
		if m.cursor >= 0 && m.cursor < len(m.savedBrowses) {
			return m.openBrowse(m.savedBrowses[m.cursor].Username, "", false), true
		}
	case "i":
		m.showFileDetails()
	case "d":
		if m.openFolderMenu() {
			return nil, true
		}
		return m.queueBrowse(), true
	case "r":
		if m.browseUser != "" {
			return m.openBrowse(m.browseUser, "", true), true
		}
		m.savedBrowseCursor, m.savedBrowseLoading = m.cursor, true
		return m.loadSavedBrowses(), true
	case "s":
		if m.browseLoaded && !m.loading && m.browseRevision != 0 {
			return m.saveBrowse(), true
		}
	case "D":
		m.openDownloadAs()
	case "f":
		if m.browseLoaded && len(m.browseTabs) > 0 {
			m.editing, m.filterEditing, m.browseFindEditing = true, false, true
			m.input = m.browseFilter
			m.inputCursor = len([]rune(m.input))
		}
	default:
		return nil, false
	}
	return nil, true
}

func (m model) browseHints() []string {
	if len(m.browseTabs) == 0 {
		return []string{"enter open", "/ browse user", "r refresh"}
	}
	var hints []string
	if _, node := m.browseTree.node(m.cursor); node != nil {
		if node.kind == treeFile {
			hints = []string{"enter download", "space mark", "D save as", "i info"}
		} else if node.kind == treeFolder {
			hints = []string{"enter expand", "d folder download", "space mark"}
		} else {
			hints = []string{"enter expand"}
		}
	}
	if m.browseLoaded {
		hints = append(hints, "f find")
	}
	hints = append(hints, "s save list", "r refresh", "/ user", "U user")
	if len(m.browseTabs) > 1 {
		hints = append(hints, "[/] tab", "ctrl+w close")
	}
	return hints
}

func (m *model) applyBrowsePageMsg(x browsePageMsg) tea.Cmd {
	if m.workspace == workspaceBrowse {
		m.saveBrowseTab()
	}
	for i := range m.browseTabs {
		tab := &m.browseTabs[i]
		key := browsePageKey(x.folder, x.query)
		state, exists := tab.pages[key]
		if !strings.EqualFold(tab.user, x.user) || tab.revision != x.revision || !exists || state.request != x.request {
			continue
		}
		state.loading, state.err = false, errText(x.err)
		tab.pages[key] = state
		if x.err == nil && x.page.Revision == tab.revision {
			applyBrowsePage(tab, x)
		}
		if m.workspace == workspaceBrowse && i == m.browseTabIndex {
			m.loadBrowseTab(i)
		}
		break
	}
	return nil
}

func (m *model) applyBrowse(x browseMsg) tea.Cmd {
	if x.page.Revision != 0 {
		for i := range m.browseTabs {
			tab := &m.browseTabs[i]
			if !strings.EqualFold(tab.user, x.user) || tab.request != x.request {
				continue
			}
			if x.err != nil {
				tab.loading, tab.err = false, errText(x.err)
			} else {
				applyBrowsePage(tab, browsePageMsg{user: x.user, page: x.page, folder: x.page.Folder, query: x.page.Query, request: x.request, revision: x.page.Revision})
			}
			if m.workspace == workspaceBrowse && i == m.browseTabIndex {
				m.loadBrowseTab(i)
			}
			break
		}
		return nil
	}
	user := x.user
	if user == "" {
		user = m.browseUser
	}
	for i := range m.browseTabs {
		tab := &m.browseTabs[i]
		if !strings.EqualFold(tab.user, user) || tab.request != x.request {
			continue
		}
		tab.loading, tab.err = false, errText(x.err)
		if x.err == nil {
			target := browseTargetCursor(x.entries, tab.target)
			tab.entries, tab.selected = x.entries, map[int]bool{}
			tab.loaded, tab.cached, tab.revision, tab.savedAt = true, x.cached, x.revision, x.savedAt
			tab.tree, tab.cursor = buildBrowseTree(tab.entries, tab.filter, tab.tree, tab.cursor)
			if tab.target != "" {
				tab.cursor = tab.tree.cursorForSource(target)
			}
		}
		tab.target = ""
		if m.workspace == workspaceBrowse && i == m.browseTabIndex {
			m.loadBrowseTab(i)
			if x.err == nil && x.cached {
				m.setNotice("Showing saved share list; press r to retry")
			}
		}
		break
	}
	return nil
}

func (m *model) applyBrowseProgress(x browseProgressMsg) tea.Cmd {
	for i := range m.browseTabs {
		tab := &m.browseTabs[i]
		if !tab.loading || tab.request != x.request || !strings.EqualFold(tab.user, x.user) {
			continue
		}
		if x.err == nil {
			if x.progress == nil {
				tab.received, tab.total = 0, 0
			} else {
				tab.received, tab.total = x.progress.Received, x.progress.Total
			}
		}
		break
	}
	if _, ok := m.currentActivity(); !ok {
		m.activityRunning = false
		return nil
	}
	return activityTick()
}

func (m *model) applySavedBrowses(x savedBrowsesMsg) tea.Cmd {
	m.savedBrowseLoading = false
	m.err = errText(x.err)
	if x.err == nil {
		m.savedBrowses = x.saved
		m.savedBrowseCursor = max(0, min(m.savedBrowseCursor, len(m.savedBrowses)-1))
		if m.workspace == workspaceBrowse && len(m.browseTabs) == 0 {
			m.cursor = m.savedBrowseCursor
		}
	}
	return nil
}

func (m *model) applySaveBrowse(x saveBrowseMsg) tea.Cmd {
	m.err = errText(x.err)
	if x.err == nil {
		m.upsertSavedBrowse(x.saved)
		for i := range m.browseTabs {
			tab := &m.browseTabs[i]
			if strings.EqualFold(tab.user, x.user) && tab.revision == x.revision {
				tab.savedAt = x.saved.SavedAt
				if m.workspace == workspaceBrowse && i == m.browseTabIndex {
					m.browseSavedAt = x.saved.SavedAt
				}
				break
			}
		}
		m.setNotice("Saved share list for " + x.saved.Username)
	}
	return nil
}
