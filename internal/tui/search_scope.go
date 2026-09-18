package tui

import (
	"github.com/catgirl-systems/oto/internal/daemon"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

type searchScope struct {
	users, rooms []string
	identity     daemon.CommunityIdentity
	query, scope string
	row          int
	editing      bool
	value        string
	cursor       int
	err          string
}

func (d *searchScope) mode() string {
	if d.scope != "" {
		return d.scope
	}
	return "users"
}
func (d *searchScope) setMode(mode string) {
	d.scope = mode
}
func (d *searchScope) cycleMode() {
	modes := []string{"global", "users", "buddies", "rooms"}
	d.setMode(modes[(slices.Index(modes, d.mode())+1)%len(modes)])
}
func (d *searchScope) setInput(value string, cursor int) {
	limit := 4096
	if d.row >= 2 {
		limit = 1024
	}
	if d.row >= 2+len(d.users) {
		limit = soulseek.MaxRoomNameBytes
	}
	if len(value) > limit || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		d.err = "Input exceeds the field limit or contains control characters"
		return
	}
	d.value, d.cursor, d.err = value, cursor, ""
}
func (m model) searchUsers() []string {
	if m.searchTabIndex < 0 || m.searchTabIndex >= len(m.searchTabs) {
		return nil
	}
	return m.searchTabs[m.searchTabIndex].usernames
}
func (m *model) openSearchScope() {
	if m.workspace != workspaceSearch && m.workspace != workspaceBrowse && m.workspace != workspaceTransfers && m.workspace != workspaceCommunity {
		return
	}
	user := ""
	if tree := m.currentTree(); tree != nil {
		if _, node := tree.node(m.cursor); node != nil {
			user = node.user
		}
	}
	if m.workspace == workspaceBrowse {
		user = m.browseUser
	}
	users := slices.Clone(m.searchUsers())
	if user != "" {
		users = []string{user}
	}
	query := ""
	if m.workspace == workspaceSearch {
		query = m.query
	}
	d := &searchScope{users: users, query: query, row: 1, editing: true, value: query, cursor: len([]rune(query))}
	d.identity = m.community.summary.CommunityIdentity
	if len(users) > 0 {
		d.setMode("users")
	} else {
		d.setMode("global")
	}
	if m.workspace == workspaceSearch && user == "" && m.searchTabIndex >= 0 && m.searchTabIndex < len(m.searchTabs) {
		tab := &m.searchTabs[m.searchTabIndex]
		if tab.scope != "" {
			d.setMode(tab.scope)
		}
		d.rooms = slices.Clone(tab.rooms)
		if tab.scope == "buddies" || tab.scope == "rooms" {
			d.users = nil
		}
	}
	if m.workspace == workspaceCommunity {
		switch m.community.view {
		case 1:
			d.setMode("rooms")
			if m.community.rooms.selected != "" {
				d.rooms = []string{m.community.rooms.selected}
			}
			if r := &m.community.rooms; m.community.pane == 0 && r.row >= 0 && r.row < len(r.rooms) {
				d.rooms = []string{r.rooms[r.row].Name}
			}
		case 2:
			d.setMode("buddies")
		}
	}
	m.searchScope = d
}
func (m model) joinedSearchRooms() []string {
	var rooms []string
	for _, room := range m.community.rooms.rooms {
		if room.Joined && room.State == "joined" {
			rooms = append(rooms, room.Name)
		}
	}
	slices.Sort(rooms)
	return rooms
}
func (m *model) submitSearchScope() tea.Cmd {
	d := m.searchScope
	if d == nil {
		return nil
	}
	if d.identity != (daemon.CommunityIdentity{}) && d.identity != m.community.summary.CommunityIdentity {
		d.err = daemon.ErrCommunitySession.Error()
		return nil
	}
	mode := d.mode()
	users, rooms := slices.Clone(d.users), slices.Clone(d.rooms)
	if mode == "global" || mode == "buddies" {
		users, rooms = nil, nil
	}
	if mode == "users" {
		rooms = nil
		var err error
		users, err = soulseek.NormalizeSearchUsers(users)
		if err != nil {
			d.err = err.Error()
			return nil
		}
		if len(users) == 0 {
			d.err = "Add at least one username"
			return nil
		}
	}
	if mode == "rooms" {
		users = nil
		if len(rooms) == 0 {
			d.err = "Select at least one joined room"
			return nil
		}
		for _, room := range rooms {
			if err := soulseek.ValidateRoomName(room); err != nil {
				d.err = err.Error()
				return nil
			}
		}
	}
	if strings.TrimSpace(d.query) == "" {
		d.err = "Enter a query"
		return nil
	}
	m.searchScope = nil
	return m.openScopedSearch(d.query, mode, users, rooms)
}
func (m *model) searchScopeKey(k tea.KeyPressMsg) tea.Cmd {
	d := m.searchScope
	if d == nil {
		return nil
	}
	if k.String() == "esc" {
		m.searchScope = nil
		return nil
	}
	if d.editing {
		if k.String() == "enter" || k.String() == "tab" {
			if d.row == 1 {
				d.query = d.value
			} else if d.row >= 2 && d.row < 2+len(d.users) {
				d.users[d.row-2] = d.value
			} else if d.row >= 2+len(d.users) {
				d.rooms[d.row-2-len(d.users)] = d.value
			}
			d.editing, d.err = false, ""
			if k.String() == "enter" && d.row == 1 {
				return m.submitSearchScope()
			}
			return nil
		}
		value, cursor, _ := editText(d.value, d.cursor, k)
		d.setInput(value, cursor)
		return nil
	}
	maxRow := 1 + len(d.users) + len(d.rooms)
	switch k.String() {
	case "up", "k":
		d.row = max(0, d.row-1)
	case "down", "j":
		d.row = min(maxRow, d.row+1)
	case "g", "b", "r":
		if k.String() == "b" {
			d.setMode("buddies")
		} else if k.String() == "r" {
			d.setMode("rooms")
			if len(d.rooms) == 0 {
				d.rooms = m.joinedSearchRooms()
			}
		} else {
			d.cycleMode()
		}
	case "a":
		if d.mode() == "rooms" {
			if len(d.rooms) >= 200 {
				d.err = "Maximum 200 selected rooms"
				return nil
			}
			d.rooms = append(d.rooms, "")
			d.row = 1 + len(d.users) + len(d.rooms)
		} else {
			if len(d.users) >= 32 {
				d.err = "Maximum 32 users"
				return nil
			}
			d.users = append(d.users, "")
			d.setMode("users")
			d.row = 1 + len(d.users)
		}
		d.value, d.cursor, d.editing = "", 0, true
	case "delete", "d":
		if d.row >= 2 && d.row < 2+len(d.users) {
			d.users = slices.Delete(d.users, d.row-2, d.row-1)
		} else if d.row >= 2+len(d.users) {
			i := d.row - 2 - len(d.users)
			if i < len(d.rooms) {
				d.rooms = slices.Delete(d.rooms, i, i+1)
			}
		}
		d.row = min(d.row, 1+len(d.users)+len(d.rooms))
	case "s":
		return m.submitSearchScope()
	case "enter":
		if d.row == 0 {
			d.cycleMode()
			return nil
		}
		d.editing, d.value = true, d.query
		if d.row >= 2 && d.row < 2+len(d.users) {
			d.value = d.users[d.row-2]
		} else if d.row >= 2+len(d.users) {
			d.value = d.rooms[d.row-2-len(d.users)]
		}
		d.cursor = len([]rune(d.value))
	}
	return nil
}
func (m model) searchScopeView() string {
	if m.width < 24 || m.height < 12 {
		return trunc("Search scope: enlarge terminal; Esc cancels", max(1, m.width))
	}
	d := m.searchScope
	labels := map[string]string{"global": "Global", "users": "Specific users", "buddies": "All buddies", "rooms": "Joined rooms"}
	lines := []string{"Scope: " + labels[d.mode()], "Query: " + d.query}
	for _, user := range d.users {
		lines = append(lines, "User: "+user)
	}
	for _, room := range d.rooms {
		lines = append(lines, "Room: "+room)
	}
	width := max(1, min(76, m.width-6))
	body := []string{strong("Search scope"), ""}
	start, end := visibleRange(len(lines), d.row, max(2, m.height-12))
	for i := start; i < end; i++ {
		line := lines[i]
		if d.editing && d.row == i {
			line = renderInputWindow(d.value, d.cursor, max(1, width-6))
		}
		marker := "  "
		if d.row == i {
			marker = "> "
		}
		body = append(body, trunc(marker+line, max(1, width-4)))
	}
	body = append(body, "", trunc(d.err, max(1, width-4)), trunc("enter edit / search · tab finish edit · g cycle scope", max(1, width-4)), trunc("a add · d remove · b buddies · r rooms · esc cancel", max(1, width-4)))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, panelStyle().Width(width).Padding(1, 1).Render(strings.Join(body, "\n")))
}
