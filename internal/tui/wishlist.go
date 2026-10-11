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

func (m *model) applyWishlist(x wishlistMsg) tea.Cmd {
	m.err = errText(x.err)
	if x.err == nil {
		if m.wishlistNotified == nil {
			m.wishlistNotified = map[string]uint64{}
		}
		bell := false
		for _, item := range x.items {
			if item.Unread && item.NotificationSequence > m.wishlistNotified[item.ID] && m.cfg.Search.WishlistNotifications {
				bell = true
			}
			m.wishlistNotified[item.ID] = item.NotificationSequence
		}
		if m.workspace == workspaceWishlist {
			m.wishlistCursor = m.cursor
		}
		m.wishlist = x.items
		m.wishlistCursor = max(0, min(m.wishlistCursor, len(m.wishlist)-1))
		if m.workspace == workspaceWishlist {
			m.cursor = m.wishlistCursor
		}
		if bell {
			return ringBell()
		}
	}
	return nil
}

func (m *model) applyWishlistAction(x wishlistActionMsg) tea.Cmd {
	m.err = errText(x.err)
	if x.err == nil {
		m.wishlist = x.items
		m.wishlistCursor = max(0, min(m.wishlistCursor, len(m.wishlist)-1))
		if x.notice != "" {
			m.setNotice(x.notice)
		}
		if x.page != nil {
			m.openSearchPage(x.query, x.filter, *x.page)
		} else if m.workspace == workspaceWishlist {
			m.cursor = m.wishlistCursor
		}
	}
	return nil
}
