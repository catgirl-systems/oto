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
