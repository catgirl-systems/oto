package tui

import "charm.land/bubbletea/v2"

// transfersKey handles keys specific to the Transfers workspace. It reports
// false for keys left to the shared navigation and global bindings.
func (m *model) transfersKey(s string) (tea.Cmd, bool) {
	uploads := m.transferTab == transferUploads
	switch s {
	case "X":
		if uploads {
			return m.beginUploadAction("cancel", true), true
		}
	case "r":
		if uploads {
			return m.beginUploadAction("retry", false), true
		}
		return m.action("resume"), true
	case "p":
		if !uploads {
			return m.action("pause"), true
		}
	case "c":
		if uploads {
			return m.beginUploadAction("clear", true), true
		}
		return m.action("clear"), true
	case "D":
		if uploads {
			return m.beginUploadAction("cancel", true), true
		}
	case "C":
		if uploads {
			m.openUploadStatusMenu()
		}
	case "S":
		return m.prepareTransferSearch(true), true
	case "s", "/":
		return m.prepareTransferSearch(false), true
	default:
		return nil, false
	}
	return nil, true
}

func (m model) transfersHints() []string {
	if m.transferTab == transferDownloads {
		return []string{"space mark", "p pause", "r resume/retry", "x cancel", "c clear", "F download anyway", "/ search", "S folder search", "] uploads", "U user"}
	}
	return []string{"space mark", "r retry", "x abort", "X abort users", "c clear selected", "C clear status", "/ search", "[ downloads", "U user"}
}
