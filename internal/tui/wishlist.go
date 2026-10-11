package tui

import "charm.land/bubbletea/v2"

// wishlistKey handles keys specific to the Wishlist workspace. It reports
// false for keys left to the shared navigation and global bindings.
func (m *model) wishlistKey(s string) (tea.Cmd, bool) {
	selected := m.cursor >= 0 && m.cursor < len(m.wishlist)
	switch s {
	case "enter":
		if selected {
			m.wishlistCursor = m.cursor
			return m.openWishlist(m.wishlist[m.cursor], false), true
		}
	case "a":
		m.beginEdit()
	case "r":
		if selected {
			m.wishlistCursor = m.cursor
			return m.openWishlist(m.wishlist[m.cursor], true), true
		}
	case "f":
		if selected {
			m.wishlistCursor = m.cursor
			m.editing, m.filterEditing, m.input = true, true, m.wishlist[m.cursor].Filter
			m.inputCursor = len([]rune(m.input))
			m.historyCursor.reset(m.input)
		}
	default:
		return nil, false
	}
	return nil, true
}

func (m model) wishlistHints() []string {
	return []string{"enter open", "a add", "f filter", "r rerun", "x remove"}
}
