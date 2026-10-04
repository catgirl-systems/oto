package tui

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

var communityViews = []string{"Chats", "Rooms", "Buddies", "Discover"}
var communityPanes = []string{"List", "Content", "Inspector"}

type communityModel struct {
	view, pane                int
	summary                   daemon.CommunitySummary
	ready, summaryLoading     bool
	summaryRequest            uint64
	err                       string
	frontend, target          string
	user                      daemon.CommunityUser
	userLoading               bool
	userRequest, userRevision uint64
	userCancel                context.CancelFunc
	userErr                   string
	userRefreshed             time.Time
	inspectorScroll           int
	inspectEditing            bool
	input, inputErr           string
	inputCursor               int
	chats                     communityChatsModel
	rooms                     communityRoomsModel
	buddies                   communityBuddiesModel
	discover                  communityDiscoverModel
	peer                      communityPeerModel
}

type communitySummaryMsg struct {
	request uint64
	summary daemon.CommunitySummary
	err     error
}

type communityUserMsg struct {
	request  uint64
	identity daemon.CommunityIdentity
	username string
	page     daemon.CommunityUsersPage
	err      error
}

func (m model) communitySummaryCmd(request uint64) tea.Cmd {
	if m.client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
		defer cancel()
		summary, err := m.client.CommunitySummary(ctx)
		return communitySummaryMsg{request, summary, err}
	}
}

func (m *model) loadCommunitySummary() tea.Cmd {
	c := &m.community
	if c.summaryLoading || m.client == nil {
		return nil
	}
	c.summaryLoading = true
	c.summaryRequest++
	return m.communitySummaryCmd(c.summaryRequest)
}

func (m *model) applyCommunitySummary(x communitySummaryMsg) tea.Cmd {
	c := &m.community
	if x.request != c.summaryRequest {
		return nil
	}
	c.summaryLoading = false
	c.err = errText(x.err)
	if x.err != nil {
		return nil
	}
	if c.summary.CommunityIdentity == x.summary.CommunityIdentity && x.summary.Revision < c.summary.Revision {
		return nil
	}
	m.applyBuddyNotification(x.summary)
	if c.summary.CommunityIdentity != x.summary.CommunityIdentity {
		wallEditing := c.rooms.private.wallForm
		privateView := c.rooms.private.view
		c.resetUser()
		m.resetCommunityChats(c.summary.Account != x.summary.Account)
		c.rooms.reset()
		m.saveBuddyDraft()
		c.buddies.reset(c.summary.Account != x.summary.Account)
		m.saveDiscoverDraft()
		c.discover.reset(c.summary.Account != x.summary.Account)
		if c.summary.Account == x.summary.Account && c.chats.conversation.Kind == "room" {
			c.rooms.selected = c.chats.conversation.Target
			c.rooms.active = daemon.CommunityRoom{Name: c.rooms.selected, State: "offline"}
			c.rooms.private.view = privateView
			if wallEditing {
				p := &c.rooms.private
				d := p.wallDrafts[chatDraftKey(x.summary.Account, c.rooms.selected, "wall")]
				p.wallForm, p.wallInput, p.wallInputCursor = true, d.text, d.cursor
			}
		}
		if c.summary.Account != "" && c.summary.Account != x.summary.Account {
			c.target, c.input = "", ""
			c.inspectEditing = false
		}
	}
	c.summary, c.ready = x.summary, true
	var user tea.Cmd
	if c.target != "" && m.workspace == workspaceCommunity && (c.userRevision != c.summary.Revision || time.Since(c.userRefreshed) >= 30*time.Second) {
		user = m.loadCommunityUser()
	}
	return tea.Batch(user, m.loadCommunityPeer(false), m.loadCommunityChats(false), m.loadCommunityRooms(false), m.loadCommunityMembers(false), m.loadCommunityFeed(false), m.loadCommunityWall(false), m.loadCommunityBuddies(false), m.loadCommunityDiscover(false))
}

func (c communityModel) supports(capability string) bool {
	return c.ready && c.err == "" && slices.Contains(c.summary.Capabilities, capability)
}

func (c *communityModel) resetUser() {
	c.peer.reset()
	if c.userCancel != nil {
		c.userCancel()
		c.userCancel = nil
	}
	c.userRequest++
	c.user, c.userLoading, c.userErr = daemon.CommunityUser{}, false, ""
	c.userRevision, c.userRefreshed = 0, time.Time{}
}

func (m *model) loadCommunityUser() tea.Cmd {
	c := &m.community
	if m.client == nil || c.userLoading || c.target == "" || !c.supports("users") || !c.supports("watches") {
		return nil
	}
	if c.frontend == "" {
		c.frontend = rand.Text()
	}
	if c.userCancel != nil {
		c.userCancel()
	}
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	c.userCancel = cancel
	c.userLoading = true
	c.userRequest++
	request, identity, username, frontend := c.userRequest, c.summary.CommunityIdentity, c.target, c.frontend
	client := m.client
	return func() tea.Msg {
		defer cancel()
		x := communityUserMsg{request: request, identity: identity, username: username}
		x.err = client.WatchCommunityUsers(ctx, daemon.CommunityWatchRequest{CommunityIdentity: identity, Frontend: frontend, Users: []string{username}})
		if x.err == nil {
			x.page, x.err = client.CommunityUsers(ctx, daemon.CommunityUsersRequest{CommunityIdentity: identity, Username: username})
		}
		return x
	}
}

func (m *model) applyCommunityUser(x communityUserMsg) {
	c := &m.community
	if x.request != c.userRequest || x.identity != c.summary.CommunityIdentity || x.username != c.target {
		return
	}
	c.userLoading, c.userErr = false, errText(x.err)
	if x.err != nil {
		return
	}
	if x.page.CommunityIdentity != c.summary.CommunityIdentity {
		c.userErr = "User data changed session; refresh Community"
		return
	}
	for _, user := range x.page.Users {
		if user.Username == c.target {
			c.user = user
			c.userRevision, c.userRefreshed = x.page.Revision, time.Now()
			return
		}
	}
	c.user, c.userRefreshed = daemon.CommunityUser{}, time.Time{}
	c.userErr = "User details missing from daemon response; r retry"
}

func (m *model) openUserInspector(username string) tea.Cmd {
	if err := soulseek.ValidateUsername(username); err != nil {
		m.setNotice(err.Error())
		return nil
	}
	if !m.community.supports("users") || !m.community.supports("watches") {
		m.setNotice("User details unavailable: refresh Community or restart the daemon")
		return nil
	}
	m.switchWorkspace(workspaceCommunity)
	c := &m.community
	if c.target != username {
		c.resetUser()
		c.inspectorScroll = 0
	}
	c.target, c.pane, c.inspectEditing = username, 2, false
	return tea.Batch(m.loadCommunityUser(), m.loadCommunityPeer(false))
}

func (m *model) communityKey(k tea.KeyPressMsg) tea.Cmd {
	c := &m.community
	if c.peer.form || c.peer.dialog {
		if handled, cmd := m.peerKey(k); handled {
			return cmd
		}
	}
	if c.inspectEditing {
		switch k.String() {
		case "esc":
			c.inspectEditing = false
		case "enter":
			if err := soulseek.ValidateUsername(c.input); err != nil {
				c.inputErr = err.Error()
				return nil
			}
			return m.openUserInspector(c.input)
		default:
			value, cursor, _ := editText(c.input, c.inputCursor, k)
			if len(value) <= 1024 && strings.IndexFunc(value, unicode.IsControl) < 0 {
				c.input, c.inputCursor, c.inputErr = value, cursor, ""
			}
		}
		return nil
	}
	if c.view == 1 && c.rooms.form != "" {
		return m.roomFormKey(k)
	}
	if c.view == 1 && c.rooms.private.editing() {
		return m.privateRoomFormKey(k)
	}
	if c.view == 2 && (c.buddies.editor != nil || c.buddies.form != "") {
		return m.buddyKey(k)
	}
	if c.view == 3 && (c.discover.form != "" || c.discover.dialog != nil) {
		return m.discoverKey(k)
	}
	if c.chats.composing || c.chats.form != "" {
		_, cmd := m.chatKeyPress(k)
		return cmd
	}
	if c.pane == 2 {
		if handled, cmd := m.peerKey(k); handled {
			return cmd
		}
	}
	if k.String() == "u" && (c.view == 1 || c.view == 2) {
		m.openSearchScope()
		return nil
	}
	switch k.String() {
	case "[", "]", "ctrl+pgup", "ctrl+pgdown":
		delta := 1
		if k.String() == "[" || k.String() == "ctrl+pgup" {
			delta = -1
		}
		c.chats.navigation++
		c.chats.cancelLoad()
		c.rooms.cancelLoads()
		c.buddies.cancelLoad()
		c.discover.cancelLoad()
		c.view = (c.view + len(communityViews) + delta) % len(communityViews)
		return tea.Batch(m.loadCommunityChats(false), m.loadCommunityRooms(false), m.loadCommunityMembers(false), m.loadCommunityFeed(false), m.loadCommunityWall(false), m.loadCommunityBuddies(false), m.loadCommunityDiscover(false))
	case "f6":
		c.pane = (c.pane + 1) % len(communityPanes)
		return m.loadCommunityMembers(false)
	case "shift+f6":
		c.pane = (c.pane + len(communityPanes) - 1) % len(communityPanes)
		return m.loadCommunityMembers(false)
	case "ctrl+n":
		return m.nextUnreadChat()
	}
	if c.view == 1 {
		return m.roomKey(k)
	}
	if c.view == 2 && c.pane != 2 {
		return m.buddyKey(k)
	}
	if c.view == 3 && c.pane != 2 {
		return m.discoverKey(k)
	}
	if handled, cmd := m.chatKeyPress(k); handled {
		return cmd
	}
	if c.pane == 2 && navKey(k.String(), &c.inspectorScroll, m.communityInspectorEnd(), m.pageRows()) {
		return nil
	}
	switch k.String() {
	case "esc", "left":
		c.pane = max(0, c.pane-1)
	case "right", "enter":
		c.pane = min(len(communityPanes)-1, c.pane+1)
	case "/":
		c.inspectEditing, c.input, c.inputCursor, c.inputErr = true, "", 0, ""
	case "r":
		return tea.Batch(m.loadCommunitySummary(), m.loadCommunityUser(), m.loadCommunityPeer(true))
	case "U":
		m.openUserActions()
	case "ctrl+n":
		m.setNotice("Private messaging unavailable in this daemon")
	}
	return nil
}

func (m *model) pasteCommunityUser(text string) {
	c := &m.community
	value, cursor := insertText(c.input, text, c.inputCursor)
	if len(value) > 1024 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		c.inputErr = "Username must fit 1024 bytes and contain no control characters"
		return
	}
	c.input, c.inputCursor, c.inputErr = value, cursor, ""
}

func (m model) userActionsView() string {
	d := m.userActions
	if m.width < 36 || m.height < 8 {
		return strings.Join([]string{trunc("User actions", m.width), trunc(fmt.Sprintf("%q", d.username), m.width), trunc("> "+userActionNames[d.row], m.width), trunc("Esc back", m.width)}, "\n")
	}
	width := max(1, min(64, m.width-4))
	lines := []string{strong("User actions"), trunc(fmt.Sprintf("%q", d.username), max(1, width-4))}
	reserved := 1
	if d.err != "" {
		reserved++
	}
	start, end := visibleRange(len(userActionNames), d.row, max(1, m.height-4-len(lines)-reserved))
	for i := start; i < end; i++ {
		label := userActionNames[i]
		if i == 0 && !m.community.supports("users") {
			label += " (unavailable)"
		}
		if i == 4 && !m.community.supports("buddies") {
			label += " (unavailable)"
		}
		if i == 5 && !m.community.supports("privacy-rules") {
			label += " (unavailable)"
		}
		lines = append(lines, selectedRow(trunc(label, max(1, width-6)), d.row == i))
	}
	if d.err != "" {
		lines = append(lines, trunc(browseErrorText(d.err), max(1, width-4)))
	}
	lines = append(lines, "↑↓ choose · Enter act · Esc back")
	body := strings.Join(communityPane(lines, max(1, width-4), max(1, m.height-4), 0), "\n")
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, panelStyle().Width(width).Padding(0, 1).Render(body))
}
