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
	"github.com/charmbracelet/x/ansi"
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
	case "ctrl+pgup", "ctrl+pgdown":
		delta := 1
		if k.String() == "ctrl+pgup" {
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
	case "up", "k":
		if c.pane == 2 {
			c.inspectorScroll = max(0, c.inspectorScroll-1)
		}
	case "down", "j":
		if c.pane == 2 {
			c.inspectorScroll = min(m.communityInspectorEnd(), c.inspectorScroll+1)
		}
	case "pgup":
		if c.pane == 2 {
			c.inspectorScroll = max(0, c.inspectorScroll-m.pageRows())
		}
	case "pgdown":
		if c.pane == 2 {
			c.inspectorScroll = min(m.communityInspectorEnd(), c.inspectorScroll+m.pageRows())
		}
	case "home":
		if c.pane == 2 {
			c.inspectorScroll = 0
		}
	case "end":
		if c.pane == 2 {
			c.inspectorScroll = m.communityInspectorEnd()
		}
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

func (c communityModel) inspectorLines() []string {
	if c.target == "" {
		return []string{"Select a user with U", "or / to enter a username."}
	}
	u := c.user
	lines := []string{fmt.Sprintf("User: %q", c.target)}
	if c.userLoading && u.Username == "" {
		lines = append(lines, "Refreshing user details…")
	}
	if c.userErr != "" {
		lines = append(lines, "! "+browseErrorText(c.userErr), "r retry")
	}
	live := c.summary.Connected && c.err == "" && c.userErr == ""
	status := "unknown"
	if !u.StatusUpdatedAt.IsZero() || u.StatusFresh {
		switch u.Status {
		case soulseek.UserStatusOnline:
			status = "online"
		case soulseek.UserStatusAway:
			status = "away"
		case soulseek.UserStatusOffline:
			status = "offline"
		}
		if !u.Exists && u.StatusFresh {
			status = "not found"
		}
		if !u.StatusFresh || !live {
			status += " (stale)"
		}
	}
	lines = append(lines, "Status: "+status)
	country := "unknown"
	if u.Country != "" {
		country = fmt.Sprintf("%q", u.Country)
		if !u.StatusFresh || !live {
			country += " (stale)"
		}
	}
	lines = append(lines, "Country: "+country)
	supporter := "unknown"
	if u.PrivilegeFresh || !u.PrivilegeUpdatedAt.IsZero() {
		supporter = "no"
		if u.Privileged {
			supporter = "yes"
		}
		if !live || !u.PrivilegeFresh {
			supporter += " (stale)"
		}
	}
	lines = append(lines, "Supporter: "+supporter)
	if u.StatsFresh || !u.StatsUpdatedAt.IsZero() {
		freshness := ""
		if !u.StatsFresh || !live {
			freshness = " (stale)"
		}
		lines = append(lines, fmt.Sprintf("Shares%s: %d files, %d folders", freshness, u.Stats.Files, u.Stats.Directories), "Speed"+freshness+": "+formatBytes(uint64(u.Stats.AverageSpeed))+"/s")
	} else {
		lines = append(lines, "Shares/speed: unknown")
	}
	address := "unknown"
	if u.IP != "" {
		address = fmt.Sprintf("%q", u.IP)
		if !u.AddressFresh || !live {
			address += " (stale)"
		}
	}
	lines = append(lines, "IP: "+address)
	if !u.LastSeen.IsZero() {
		lines = append(lines, "Last seen (observed):", u.LastSeen.Local().Format(time.RFC3339))
	}
	lines = append(lines, c.peerLines()...)
	return append(lines, "", "Partial server information.", "U user actions · r refresh")
}

// Width-aware tabs keep the active label visible without relying on color.
func visibleTabs(names []string, active, width int) string {
	active = max(0, min(active, len(names)-1))
	bestSep := " "
	for _, sep := range []string{"   ", "  "} {
		var parts []string
		for i, name := range names {
			label := muted(name)
			if i == active {
				label = accent("[" + name + "]")
			}
			parts = append(parts, label)
		}
		if ansi.StringWidth(strings.Join(parts, sep)) <= width {
			bestSep = sep
			break
		}
	}
	render := func(lo, hi int) string {
		var parts []string
		if lo > 0 {
			parts = append(parts, "‹")
		}
		for i := lo; i <= hi; i++ {
			label := muted(names[i])
			if i == active {
				label = accent("[" + names[i] + "]")
			}
			parts = append(parts, label)
		}
		if hi+1 < len(names) {
			parts = append(parts, "›")
		}
		return strings.Join(parts, bestSep)
	}
	lo, hi := active, active
	for {
		grew := false
		if lo > 0 && ansi.StringWidth(render(lo-1, hi)) <= width {
			lo--
			grew = true
		}
		if hi+1 < len(names) && ansi.StringWidth(render(lo, hi+1)) <= width {
			hi++
			grew = true
		}
		if !grew {
			break
		}
	}
	if ansi.StringWidth(render(lo, hi)) > width {
		return ansi.Truncate(accent("["+names[active]+"]"), max(0, width), "…")
	}
	return ansi.Truncate(render(lo, hi), max(0, width), "…")
}

func (m model) workspaceTabs(width int) string {
	badge := ""
	if m.community.summary.Unread > 0 {
		badge = fmt.Sprintf("Unread:%d", m.community.summary.Unread)
	}
	if m.community.summary.Mentions > 0 {
		badge += fmt.Sprintf(" @%d", m.community.summary.Mentions)
	}
	// Keep badges bounded at narrow widths; exact totals live in the summary.
	if ansi.StringWidth(badge)+len(m.workspaceNames()[m.workspace])+3 > width {
		badge = fmt.Sprintf("U:%d", min(m.community.summary.Unread, 99))
		if m.community.summary.Unread > 99 {
			badge += "+"
		}
		if m.community.summary.Mentions > 0 {
			badge += " @"
		}
	}
	budget := width
	if badge != "" {
		budget -= ansi.StringWidth(badge) + 1
	}
	return spread(visibleTabs(m.workspaceNames(), int(m.workspace), budget), badge, width)
}

func (m model) renderCommunity(width, height int) string {
	c := m.community
	lines := []string{visibleTabs(communityViews, c.view, width)}
	dot := styled("●", lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1")))
	state := dot + " " + strong("Online")
	if !c.summary.Connected {
		dot = styled("○", lipgloss.NewStyle().Foreground(lipgloss.Color("#F9E2AF")))
		state = dot + " " + muted("Offline")
	}
	if !c.ready {
		dot = styled("◌", lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA")))
		state = dot + " " + muted("Loading Community…")
	}
	if c.err != "" {
		state = danger("! Community unavailable: " + browseErrorText(c.err) + " · r retry / restart daemon")
	}
	lines = append(lines, ansi.Truncate(state, width, "…"))
	remaining := max(0, height-len(lines))
	if c.peer.form {
		return strings.Join(append(lines, m.peerPictureForm(width, remaining)...), "\n")
	}
	if c.inspectEditing {
		body := []string{accent("Inspect user") + muted(" · Esc back"), renderInputWindow(c.input, c.inputCursor, width), danger(c.inputErr), muted("Enter inspect · paste never submits")}
		return strings.Join(append(lines, communityPane(body, width, remaining, 0)...), "\n")
	}
	if c.chats.form != "" && (c.view == 0 || c.view == 1) {
		return strings.Join(append(lines, m.chatFormView(width, remaining)...), "\n")
	}
	if c.rooms.form != "" && c.view == 1 {
		return strings.Join(append(lines, m.roomFormView(width, remaining)...), "\n")
	}
	if c.rooms.private.editing() && c.view == 1 {
		return strings.Join(append(lines, m.privateRoomFormView(width, remaining)...), "\n")
	}
	if c.view == 2 && (c.buddies.editor != nil || c.buddies.form != "") {
		return strings.Join(append(lines, m.buddyEditorView(width, remaining)...), "\n")
	}
	if c.view == 3 && c.discover.form != "" {
		return strings.Join(append(lines, m.discoverFormView(width, remaining)...), "\n")
	}
	list := []string{muted("No " + strings.ToLower(communityViews[c.view]) + " loaded."), "", muted("/ inspect a user"), muted("U user actions")}
	content := []string{strong(communityViews[c.view]), "", muted("This daemon does not advertise"), muted("this service yet."), "", muted("User details remain available"), muted("with / or U from file lists.")}
	panes := [][]string{list, content, c.inspectorLines()}
	if m.width < 80 || m.width < 110 && c.pane == 2 {
		breadcrumb := communityViews[c.view] + " / " + communityPanes[c.pane] + " · Esc back"
		if c.view == 0 && c.supports("private-chat") {
			panes[c.pane] = m.chatPane(c.pane, width, max(0, remaining-1))
		} else if c.view == 1 {
			switch c.pane {
			case 0:
				panes[c.pane] = m.roomListPane(width, max(0, remaining-1))
			case 1:
				panes[c.pane] = m.roomContentPane(width, max(0, remaining-1))
			case 2:
				panes[c.pane] = m.roomRosterPane(width, max(0, remaining-1))
			}
		}
		if c.view == 2 && c.supports("buddies") && c.pane < 2 {
			if c.pane == 0 {
				panes[0] = m.buddyListPane(width, max(0, remaining-1))
			} else {
				panes[1] = m.buddyDetailPane(width, max(0, remaining-1))
			}
		}
		if c.view == 3 {
			switch c.pane {
			case 0:
				panes[0] = m.discoverSidebar(width, max(0, remaining-1))
			case 1:
				panes[1] = m.discoverRowsPane(width, max(0, remaining-1))
			case 2:
				panes[2] = c.inspectorLines()
			}
		}
		body := append([]string{accent(breadcrumb)}, communityPane(panes[c.pane], width, max(0, remaining-1), c.paneScroll())...)
		return strings.Join(append(lines, communityPane(body, width, remaining, 0)...), "\n")
	}
	sizes := m.communityColumnSizes(width)
	columns := make([][]string, len(sizes))
	for i, size := range sizes {
		label := communityPanes[i]
		if i == c.pane {
			label = "[" + label + "]"
		}
		scroll := 0
		if i == 2 && c.view != 1 {
			scroll = c.inspectorScroll
		}
		if c.view == 0 && c.supports("private-chat") && i < 2 {
			panes[i] = m.chatPane(i, size, max(0, remaining-1))
		} else if c.view == 1 {
			switch i {
			case 0:
				panes[i] = m.roomListPane(size, max(0, remaining-1))
			case 1:
				panes[i] = m.roomContentPane(size, max(0, remaining-1))
			case 2:
				panes[i] = m.roomRosterPane(size, max(0, remaining-1))
			}
		}
		if c.view == 2 && c.supports("buddies") && i < 2 {
			if i == 0 {
				panes[0] = m.buddyListPane(size, max(0, remaining-1))
			} else {
				panes[1] = m.buddyDetailPane(size, max(0, remaining-1))
			}
		}
		if c.view == 3 {
			switch i {
			case 0:
				panes[0] = m.discoverSidebar(size, max(0, remaining-1))
			case 1:
				panes[1] = m.discoverRowsPane(size, max(0, remaining-1))
			case 2:
				panes[2] = c.inspectorLines()
			}
		}
		styledHeader := accent(label)
		if i != c.pane {
			styledHeader = muted(label)
		}
		body := append([]string{styledHeader}, communityPane(panes[i], size, max(0, remaining-1), scroll)...)
		columns[i] = communityPane(body, size, remaining, 0)
	}
	sep := styled(" │ ", lipgloss.NewStyle().Foreground(lipgloss.Color("#45475A")))
	if !colorsEnabled() {
		sep = " │ "
	}
	for row := 0; row < remaining; row++ {
		var cells []string
		for i := range columns {
			cell := ""
			if row < len(columns[i]) {
				cell = columns[i][row]
			}
			cells = append(cells, cell+strings.Repeat(" ", max(0, sizes[i]-ansi.StringWidth(cell))))
		}
		lines = append(lines, strings.Join(cells, sep))
	}
	return strings.Join(lines[:min(len(lines), height)], "\n")
}

func (c communityModel) paneScroll() int {
	if c.pane == 2 && c.view != 1 {
		return c.inspectorScroll
	}
	return 0
}

func (m model) communityInspectorEnd() int {
	width := max(1, m.width-4)
	if m.width >= 110 {
		width = 27
	}
	lines := strings.Split(ansi.Wrap(strings.Join(m.community.inspectorLines(), "\n"), width, ""), "\n")
	return max(0, len(lines)-max(1, m.height-9))
}

func communityPane(lines []string, width, height, scroll int) []string {
	var wrapped []string
	for _, line := range lines {
		wrapped = append(wrapped, strings.Split(ansi.Wrap(line, max(1, width), ""), "\n")...)
	}
	start := max(0, min(scroll, len(wrapped)-height))
	out := wrapped[start:min(len(wrapped), start+max(0, height))]
	for i := range out {
		out[i] = ansi.Truncate(out[i], max(0, width), "…")
	}
	return out
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
