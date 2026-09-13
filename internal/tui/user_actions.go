package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

// Only actions backed by implemented APIs are offered. Later capabilities add
// their actions here, rather than giving each workspace its own menu.
var userActionNames = []string{"Inspect user", "Browse shared files", "Search user's files", "Message user", "Buddy / note / trust / priority", "Privacy / ignore / ban", "Gift supporter privileges"}

type userActions struct {
	username string
	identity daemon.CommunityIdentity
	row      int
	err      string
}

func (m *model) contextualUser() string {
	if m.workspace == workspaceCommunity {
		c := m.community.chats
		if m.community.view == 0 {
			if m.community.pane == 0 && c.listRow < len(c.conversations) {
				return c.conversations[c.listRow].Target
			}
			if m.community.pane == 1 && c.conversation.Target != "" {
				return c.conversation.Target
			}
		}
		if m.community.view == 2 && m.community.pane < 2 {
			if buddy, ok := m.selectedBuddy(); ok {
				return buddy.Username
			}
			return m.community.buddies.selected
		}
		if m.community.view == 3 && m.community.pane == 1 {
			return m.discoverTarget()
		}
		return m.community.target
	}
	if m.workspace == workspaceBrowse {
		if m.browseUser != "" {
			return m.browseUser
		}
		if m.cursor >= 0 && m.cursor < len(m.savedBrowses) {
			return m.savedBrowses[m.cursor].Username
		}
		return ""
	}
	if m.workspace == workspaceSearch || m.workspace == workspaceTransfers {
		if tree := m.currentTree(); tree != nil {
			if _, node := tree.node(m.cursor); node != nil {
				return node.user
			}
		}
	}
	return ""
}

func (m *model) openUserActions() {
	username := m.contextualUser()
	if username == "" {
		m.setNotice("Select a user first; Community / can inspect a username")
		return
	}
	if err := soulseek.ValidateUsername(username); err != nil {
		m.setNotice(err.Error())
		return
	}
	m.userActions = &userActions{username: username, identity: m.community.summary.CommunityIdentity}
}

func (m *model) userActionsKey(k tea.KeyPressMsg) tea.Cmd {
	d := m.userActions
	switch k.String() {
	case "esc", "U":
		m.userActions = nil
	case "up", "k":
		d.row = max(0, d.row-1)
	case "down", "j":
		d.row = min(len(userActionNames)-1, d.row+1)
	case "enter":
		if d.identity != m.community.summary.CommunityIdentity {
			d.err = "Account/session changed. Esc, then reopen User actions."
			return nil
		}
		switch d.row {
		case 0:
			if !m.community.supports("users") || !m.community.supports("watches") {
				d.err = "User details unavailable; refresh Community / restart daemon."
				return nil
			}
			m.userActions = nil
			return m.openUserInspector(d.username)
		case 1:
			m.userActions = nil
			return m.openBrowse(d.username, "", false)
		case 2:
			m.userActions = nil
			m.searchScope = &searchScope{users: []string{d.username}, row: 1, editing: true}
		case 3:
			if !m.community.supports("private-chat") {
				d.err = "Private messaging unavailable; refresh Community / restart daemon."
				return nil
			}
			m.userActions = nil
			return m.openCommunityChat(d.username, true)
		case 4:
			if !m.community.supports("buddies") {
				d.err = "Buddies unavailable; refresh Community / restart daemon."
				return nil
			}
			m.userActions = nil
			m.switchWorkspace(workspaceCommunity)
			m.community.view, m.community.pane = 2, 1
			return tea.Batch(m.openBuddyEditor(d.username, false), m.loadCommunityBuddies(true))
		case 5:
			m.userActions = nil
			return m.openPrivacyRules("ignore", d.username)
		case 6:
			m.userActions = nil
			return m.openPrivileges(d.username)
		}
	}
	return nil
}
