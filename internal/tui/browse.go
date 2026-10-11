package tui

import "charm.land/bubbletea/v2"

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
