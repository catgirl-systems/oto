package tui

import "charm.land/bubbletea/v2"

// sharesKey handles keys specific to the Shares workspace. It reports false
// for keys left to the shared navigation and global bindings.
func (m *model) sharesKey(s string) (tea.Cmd, bool) {
	switch s {
	case "enter":
		return m.openShareAccess(), true
	case "a":
		m.beginEdit()
	case "r":
		return m.rescanShares(), true
	case "c":
		return m.cancelShareScan(), true
	case "s":
		return m.openSharedSendPrompt(), true
	default:
		return nil, false
	}
	return nil, true
}

func (m model) sharesHints() []string {
	hints := []string{"enter access"}
	if _, node := m.shareTree.node(m.cursor); node != nil && node.kind != treeFile {
		hints = append(hints, "→ expand")
	}
	hints = append(hints, "a add", "s send", "r rescan")
	if scan := m.status.shareScan; scan != nil && scan.State == "scanning" {
		hints = append(hints, "c cancel scan")
	}
	if _, node := m.shareTree.node(m.cursor); node != nil && node.kind == treeShareRoot {
		hints = append(hints, "x remove")
	}
	return hints
}

func (m *model) applyScanCancel(x scanCancelMsg) tea.Cmd {
	m.err = errText(x.err)
	if x.err == nil {
		m.setNotice("Scan cancellation requested")
	}
	return m.loadStatus()
}

func (m *model) applyShares(x sharesMsg) tea.Cmd {
	m.err = errText(x.err)
	if x.err != nil {
		return nil
	}
	m.shares = x.shares
	changed := len(m.shareTree.nodes) == 0 || !sameShareRoots(m.shares, m.shareTree)
	if x.reset || changed {
		m.shareGeneration++
		cursor := m.shareCursor
		if m.workspace == workspaceShares {
			cursor = m.cursor
		}
		m.shareTree, m.shareCursor = buildShareRoots(m.shares, m.shareTree, cursor, x.reset)
		if m.workspace == workspaceShares {
			m.cursor = m.shareCursor
		}
	}
	return nil
}

func (m *model) applyShareBrowse(x shareBrowseMsg) tea.Cmd {
	if x.generation != m.shareGeneration {
		return nil
	}
	index, ok := m.shareTree.byID[x.nodeID]
	if !ok || m.shareTree.nodes[index].request != x.request {
		return nil
	}
	m.shareTree.nodes[index].loading = false
	m.err = errText(x.err)
	cursor := m.shareCursor
	if m.workspace == workspaceShares {
		cursor = m.cursor
	}
	if x.err != nil {
		m.shareTree.expanded[x.nodeID] = false
		m.shareTree.rebuildVisible()
		m.shareCursor = max(0, min(cursor, len(m.shareTree.visible)-1))
	} else {
		m.shareCursor = m.shareTree.addShareChildren(x.nodeID, x.entries, cursor)
	}
	if m.workspace == workspaceShares {
		m.cursor = m.shareCursor
	}
	return nil
}
