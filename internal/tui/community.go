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
	if c.summary.CommunityIdentity != x.summary.CommunityIdentity {
		c.resetUser()
		m.resetCommunityChats(c.summary.Account != x.summary.Account)
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
	return tea.Batch(user, m.loadCommunityChats(false))
}

func (c communityModel) supports(capability string) bool {
	return c.ready && c.err == "" && slices.Contains(c.summary.Capabilities, capability)
}

func (c *communityModel) resetUser() {
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
	return m.loadCommunityUser()
}

func (m *model) communityKey(k tea.KeyPressMsg) tea.Cmd {
	c := &m.community
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
	if handled, cmd := m.chatKeyPress(k); handled {
		return cmd
	}
	switch k.String() {
	case "ctrl+pgup":
		c.view = (c.view + len(communityViews) - 1) % len(communityViews)
	case "ctrl+pgdown":
		c.view = (c.view + 1) % len(communityViews)
	case "f6":
		c.pane = (c.pane + 1) % len(communityPanes)
	case "shift+f6":
		c.pane = (c.pane + len(communityPanes) - 1) % len(communityPanes)
	case "esc", "left":
		c.pane = max(0, c.pane-1)
	case "right", "enter":
		c.pane = min(len(communityPanes)-1, c.pane+1)
	case "/":
		c.inspectEditing, c.input, c.inputCursor, c.inputErr = true, "", 0, ""
	case "r":
		return tea.Batch(m.loadCommunitySummary(), m.loadCommunityUser())
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
	return append(lines, "", "Partial server information.", "U user actions · r refresh")
}

// Width-aware tabs keep the active label visible without relying on color.
func visibleTabs(names []string, active, width int) string {
	active = max(0, min(active, len(names)-1))
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
		return strings.Join(parts, " ")
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
	state := "Online"
	if !c.summary.Connected {
		state = "Offline · live data is stale"
	}
	if !c.ready {
		state = "Loading Community…"
	}
	if c.err != "" {
		state = "! Community unavailable: " + browseErrorText(c.err) + " · r retry / restart daemon"
	}
	lines = append(lines, ansi.Truncate(state, width, "…"))
	remaining := max(0, height-len(lines))
	if c.inspectEditing {
		body := []string{"Inspect user · Esc back", renderInputWindow(c.input, c.inputCursor, width), c.inputErr, "Enter inspect · paste never submits"}
		return strings.Join(append(lines, communityPane(body, width, remaining, 0)...), "\n")
	}
	if c.chats.form != "" && c.view == 0 {
		return strings.Join(append(lines, m.chatFormView(width, remaining)...), "\n")
	}
	list := []string{"No " + strings.ToLower(communityViews[c.view]) + " loaded.", "", "/ inspect a user", "U user actions"}
	content := []string{communityViews[c.view], "", "This daemon does not advertise", "this service yet.", "", "User details remain available", "with / or U from file lists."}
	panes := [][]string{list, content, c.inspectorLines()}
	if m.width < 80 || m.width < 110 && c.pane == 2 {
		breadcrumb := communityViews[c.view] + " / " + communityPanes[c.pane] + " · Esc back"
		if c.view == 0 && c.supports("private-chat") {
			panes[c.pane] = m.chatPane(c.pane, width, max(0, remaining-1))
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
		if i == 2 {
			scroll = c.inspectorScroll
		}
		if c.view == 0 && c.supports("private-chat") && i < 2 {
			panes[i] = m.chatPane(i, size, max(0, remaining-1))
		}
		body := append([]string{accent(label)}, communityPane(panes[i], size, max(0, remaining-1), scroll)...)
		columns[i] = communityPane(body, size, remaining, 0)
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
		lines = append(lines, strings.Join(cells, " │ "))
	}
	return strings.Join(lines[:min(len(lines), height)], "\n")
}

func (c communityModel) paneScroll() int {
	if c.pane == 2 {
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
	lines := []string{strong("User actions"), trunc(fmt.Sprintf("%q", d.username), max(1, width-4)), ""}
	for i, label := range userActionNames {
		if i == 0 && !m.community.supports("users") {
			label += " (unavailable)"
		}
		lines = append(lines, selectedRow(label, d.row == i))
	}
	lines = append(lines, "", browseErrorText(d.err), "↑↓ choose · Enter act · Esc back")
	body := strings.Join(communityPane(lines, max(1, width-4), max(1, m.height-4), 0), "\n")
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, panelStyle().Width(width).Padding(0, 1).Render(body))
}
