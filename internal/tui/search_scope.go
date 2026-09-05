package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

type searchScope struct {
	users   []string
	query   string
	global  bool
	row     int
	editing bool
	value   string
	cursor  int
	err     string
}

func (m model) searchUsers() []string {
	if m.searchTabIndex < 0 || m.searchTabIndex >= len(m.searchTabs) {
		return nil
	}
	return m.searchTabs[m.searchTabIndex].usernames
}
func (m *model) openSearchScope() {
	if m.workspace != workspaceSearch && m.workspace != workspaceBrowse && m.workspace != workspaceTransfers {
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
	m.searchScope = &searchScope{users: users, query: query, global: len(users) == 0, row: 1, editing: true, value: query, cursor: len([]rune(query))}
}
func (m *model) submitSearchScope() tea.Cmd {
	d := m.searchScope
	users := d.users
	if d.global {
		users = nil
	}
	normalized, err := soulseek.NormalizeSearchUsers(users)
	if err != nil {
		d.err = err.Error()
		return nil
	}
	if !d.global && len(normalized) == 0 {
		d.err = "Add at least one username"
		return nil
	}
	if strings.TrimSpace(d.query) == "" {
		d.err = "Enter a query"
		return nil
	}
	m.searchScope = nil
	return m.openSearch(d.query, normalized...)
}
func (m *model) searchScopeKey(k tea.KeyPressMsg) tea.Cmd {
	d := m.searchScope
	if k.String() == "esc" {
		m.searchScope = nil
		return nil
	}
	if d.editing {
		if k.String() == "enter" || k.String() == "tab" {
			if d.row == 1 {
				d.query = d.value
			} else {
				users := slices.Clone(d.users)
				users[d.row-2] = d.value
				normalized, err := soulseek.NormalizeSearchUsers(users)
				if err != nil {
					d.err = err.Error()
					return nil
				}
				d.users = normalized
				d.row = min(d.row, len(d.users)+1)
			}
			d.editing = false
			d.err = ""
			if k.String() == "enter" && d.row == 1 {
				return m.submitSearchScope()
			}
			return nil
		}
		d.value, d.cursor, _ = editText(d.value, d.cursor, k)
		return nil
	}
	switch k.String() {
	case "up", "k":
		d.row = max(0, d.row-1)
	case "down", "j":
		d.row = min(len(d.users)+1, d.row+1)
	case "g":
		d.global = !d.global
	case "a":
		if len(d.users) >= 32 {
			d.err = "Maximum 32 users"
			return nil
		}
		d.users = append(d.users, "")
		d.global = false
		d.row = len(d.users) + 1
		d.value = ""
		d.cursor = 0
		d.editing = true
	case "delete", "d":
		if d.row >= 2 {
			d.users = slices.Delete(d.users, d.row-2, d.row-1)
			d.row = min(d.row, len(d.users)+1)
		}
	case "s":
		return m.submitSearchScope()
	case "enter":
		if d.row == 0 {
			d.global = !d.global
			return nil
		}
		d.editing = true
		d.value = d.query
		if d.row >= 2 {
			d.value = d.users[d.row-2]
		}
		d.cursor = len([]rune(d.value))
	}
	return nil
}
func (m model) searchScopeView() string {
	d := m.searchScope
	scope := "Global"
	if !d.global {
		scope = "Specific users"
	}
	lines := []string{"Scope: " + scope, "Query: " + d.query}
	for _, user := range d.users {
		lines = append(lines, "User: "+user)
	}
	width := max(1, min(76, m.width-6))
	body := []string{strong("Search scope"), ""}
	start, end := visibleRange(len(lines), d.row, max(2, m.height-12))
	for i := start; i < end; i++ {
		line := lines[i]
		if d.editing && d.row == i {
			line = "> " + d.value + "▏"
		}
		marker := "  "
		if d.row == i {
			marker = "> "
		}
		body = append(body, trunc(marker+line, max(1, width-4)))
	}
	body = append(body, "", trunc(d.err, max(1, width-4)), trunc("enter edit / search · tab finish edit · g scope", max(1, width-4)), trunc("a add user · d remove · s search · esc cancel", max(1, width-4)), fmt.Sprintf("%d / 32 targets", len(d.users)))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, panelStyle().Width(width).Padding(1, 1).Render(strings.Join(body, "\n")))
}
