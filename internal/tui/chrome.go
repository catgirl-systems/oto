package tui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/charmbracelet/x/ansi"
)

// The chrome is everything around the active workspace: the header with the
// workspace tabs, the framed panel whose border carries the workspace's own
// tabs, the status line, and the key hint bar.
//
//	 oto   1 Search   2 Wishlist   3 Browse  …          ↓ 1.2 MiB/s   ● online @me
//	╭─  query one   query two ───────────────────────── 12 loaded / 40 found ─╮
//	│ …                                                                        │
//	╰──────────────────────────────────────────────────────────────────────────╯
//	 ✗ Error: …                                         Searching foo  ━━━━─────
//	 enter download   space mark   / search   f filter                  ? help

var workspaceTitles = [workspaceCount]string{"Search", "Wishlist", "Browse", "Transfers", "Community", "Stats", "Shares", "Settings"}

// tabItem is one entry of a tab strip: an optional jump key, a name, and an
// optional coloured badge such as an unread count.
type tabItem struct {
	key, name, badge string
	badgeColor       color.Color
}

func (t tabItem) render(active, keys bool) string {
	name := t.name
	if keys && t.key != "" {
		name = t.key + " " + name
	}
	if active {
		label := name
		if t.badge != "" {
			label += " " + t.badge
		}
		return pill(label, theme.surface, theme.accent)
	}
	s := " "
	if keys && t.key != "" {
		s += faint(t.key) + " " + muted(t.name)
	} else {
		s += muted(name)
	}
	if t.badge != "" {
		s += " " + styled(t.badge, fg(t.badgeColor).Bold(true))
	}
	return s + " "
}

// tabStrip renders items within width. It prefers every tab with its jump key,
// then every tab without keys, then a window around the active tab with ‹ ›
// markers, and finally the truncated active tab alone. It also reports the
// visible index range.
func tabStrip(items []tabItem, active, width int) (string, int, int) {
	if len(items) == 0 || width <= 0 {
		return "", 0, -1
	}
	active = max(0, min(active, len(items)-1))
	join := func(lo, hi int, keys bool) string {
		var b strings.Builder
		if lo > 0 {
			b.WriteString(faint("‹"))
		}
		for i := lo; i <= hi; i++ {
			b.WriteString(items[i].render(i == active, keys))
		}
		if hi+1 < len(items) {
			b.WriteString(faint("›"))
		}
		return b.String()
	}
	for _, keys := range []bool{true, false} {
		if s := join(0, len(items)-1, keys); lipgloss.Width(s) <= width {
			return s, 0, len(items) - 1
		}
	}
	lo, hi := active, active
	for {
		grew := false
		if lo > 0 && lipgloss.Width(join(lo-1, hi, false)) <= width {
			lo, grew = lo-1, true
		}
		if hi+1 < len(items) && lipgloss.Width(join(lo, hi+1, false)) <= width {
			hi, grew = hi+1, true
		}
		if !grew {
			break
		}
	}
	if s := join(lo, hi, false); lipgloss.Width(s) <= width {
		return s, lo, hi
	}
	return ansi.Truncate(items[active].render(true, false), width, "…"), active, active
}

func capCount[T int | int64](n T) string {
	if n > 99 {
		return "99+"
	}
	return fmt.Sprint(n)
}

// communityBadge is the unread chat count plus a mention marker.
func (m model) communityBadge() string {
	s := m.community.summary
	badge := ""
	if s.Unread > 0 {
		badge = capCount(s.Unread)
	}
	if s.Mentions > 0 {
		badge = strings.TrimSpace(badge + " @" + capCount(s.Mentions))
	}
	return badge
}

func (m model) workspaceNames() []string { return workspaceTitles[:] }

// workspaceBadge is the live counter shown beside a workspace tab.
func (m model) workspaceBadge(w workspace) (string, color.Color) {
	switch w {
	case workspaceWishlist:
		unread := 0
		for _, item := range m.wishlist {
			if item.Unread {
				unread += item.ResultCount
			}
		}
		if unread > 0 {
			return capCount(unread), theme.accent
		}
	case workspaceTransfers:
		downloads, uploads := m.runningTransfers()
		var parts []string
		if downloads > 0 {
			parts = append(parts, fmt.Sprintf("%d↓", downloads))
		}
		if uploads > 0 {
			parts = append(parts, fmt.Sprintf("%d↑", uploads))
		}
		return strings.Join(parts, " "), theme.download
	case workspaceCommunity:
		return m.communityBadge(), theme.mention
	}
	return "", nil
}

func (m model) runningTransfers() (downloads, uploads int) {
	for _, t := range m.transfers {
		if t.state != "running" {
			continue
		}
		if t.direction == "upload" {
			uploads++
		} else {
			downloads++
		}
	}
	return downloads, uploads
}

func (m model) workspaceItems() []tabItem {
	items := make([]tabItem, workspaceCount)
	for w := range workspaceCount {
		badge, c := m.workspaceBadge(w)
		items[w] = tabItem{key: fmt.Sprint(int(w) + 1), name: workspaceTitles[w], badge: badge, badgeColor: c}
	}
	return items
}

// workspaceTabs renders the workspace strip within width. When the Community
// tab scrolls out of view its unread badge moves to the right edge so new
// messages are never hidden.
func (m model) workspaceTabs(width int) string {
	items := m.workspaceItems()
	strip, lo, hi := tabStrip(items, int(m.workspace), width)
	badge := m.communityBadge()
	if badge == "" || int(workspaceCommunity) >= lo && int(workspaceCommunity) <= hi && strings.Contains(ansi.Strip(strip), badge) {
		return strip
	}
	note := styled("chat "+badge, fg(theme.mention).Bold(true))
	strip, _, _ = tabStrip(items, int(m.workspace), max(1, width-lipgloss.Width(note)-1))
	return spread(strip, note, width)
}

func (m model) transferSpeeds() (uint64, uint64) {
	var down, up uint64
	for _, t := range m.transfers {
		if t.state != "running" {
			continue
		}
		if t.direction == "upload" {
			up += t.speed
		} else {
			down += t.speed
		}
	}
	return down, up
}

func (m model) statusText() string {
	if m.status.status == daemon.StatusConnected && (m.status.presence == daemon.PresenceOnline || m.status.presence == daemon.PresenceAway) {
		return string(m.status.presence)
	}
	if m.status.status == daemon.StatusStopped {
		return string(daemon.PresenceOffline)
	}
	if m.status.status == "" {
		return "starting"
	}
	return string(m.status.status)
}

// statusView is the presence indicator: a coloured dot, the state, and the
// signed-in user.
func (m model) statusView() string {
	status := m.statusText()
	color := theme.warning
	if status == string(daemon.PresenceOnline) {
		color = theme.success
	} else if status == string(daemon.PresenceOffline) || m.status.status == daemon.StatusError {
		color = theme.danger
	}
	label := styled("●", fg(color)) + " " + subtle(status)
	if m.status.user != "" {
		label += " " + muted("@"+m.status.user)
	}
	return label
}

func (m model) speedView() string {
	down, up := m.transferSpeeds()
	if down == 0 && up == 0 {
		return ""
	}
	return styled("↓ "+formatBytes(down)+"/s", fg(theme.download)) + "  " + styled("↑ "+formatBytes(up)+"/s", fg(theme.upload))
}

func brand() string {
	return styled(" oto ", lipgloss.NewStyle().Bold(true).Background(theme.accent).Foreground(theme.onAccent))
}

// headerLines is the top of the screen: one row when the brand, every tab and
// the status fit, otherwise brand and status above a full-width tab row.
func (m model) headerLines() []string {
	width := max(1, m.width-2)
	right := m.statusView()
	if speeds := m.speedView(); speeds != "" {
		right = speeds + "   " + right
	}
	full, _, _ := tabStrip(m.workspaceItems(), int(m.workspace), 1<<16)
	left := brand() + "  "
	if lipgloss.Width(left)+lipgloss.Width(full)+2+lipgloss.Width(right) <= width {
		return []string{" " + spread(left+full, right, width)}
	}
	left = brand()
	if tagline := "  " + muted("Soulseek for your terminal"); lipgloss.Width(left+tagline)+2+lipgloss.Width(right) <= width {
		left += tagline
	}
	return []string{" " + spread(left, right, width), " " + m.workspaceTabs(width)}
}

// panelTitle returns the workspace's own tab strip (or name) for the panel's
// top border, and a short detail for its right side.
func (m model) panelTitle(width int) (string, string) {
	detail := m.panelDetail()
	budget := max(4, width-lipgloss.Width(detail)-10)
	var items []tabItem
	active := 0
	switch m.workspace {
	case workspaceSearch:
		for _, t := range m.searchTabs {
			items = append(items, tabItem{name: trunc(searchTabLabel(t), 28)})
		}
		active = m.searchTabIndex
	case workspaceBrowse:
		for _, t := range m.browseTabs {
			items = append(items, tabItem{name: trunc(browseTabLabel(t), 28)})
		}
		active = m.browseTabIndex
	case workspaceTransfers:
		downloads, uploads := 0, 0
		for _, t := range m.transfers {
			if t.direction == "upload" {
				uploads++
			} else {
				downloads++
			}
		}
		items = []tabItem{{name: fmt.Sprintf("↓ Downloads %d", downloads)}, {name: fmt.Sprintf("↑ Uploads %d", uploads)}}
		active = int(m.transferTab)
	case workspaceCommunity:
		for _, name := range communityViews {
			items = append(items, tabItem{name: name})
		}
		active = m.community.view
	case workspaceStats:
		if m.stats.prune {
			return accent("Prune statistics"), detail
		}
		for _, name := range statsPages {
			items = append(items, tabItem{name: name})
		}
		active = m.stats.page
	}
	if len(items) == 0 {
		title := workspaceTitles[m.workspace]
		if m.workspace == workspaceBrowse {
			title = "Saved share lists"
		}
		return accent(title), detail
	}
	// The workspace's own tabs matter more than the border detail: shorten
	// the detail, then drop it, until every tab fits.
	for _, candidate := range []string{detail, m.panelDetailShort(), ""} {
		strip, lo, hi := tabStrip(items, active, max(4, width-lipgloss.Width(candidate)-10))
		if lo == 0 && hi == len(items)-1 || candidate == "" {
			return strip, candidate
		}
	}
	strip, _, _ := tabStrip(items, active, budget)
	return strip, detail
}

func searchTabLabel(t searchTab) string {
	label := t.query
	if t.scope == "rooms" && len(t.rooms) > 0 {
		label = "#" + strings.Join(t.rooms, ", ") + ": " + label
	} else if t.scope == "buddies" {
		label = "buddies: " + label
	} else if len(t.usernames) > 0 {
		label = "@" + strings.Join(t.usernames, ", ") + ": " + label
	}
	if t.loading {
		label += "…"
	}
	return label
}

func browseTabLabel(t browseTab) string {
	label := browseErrorText(t.user)
	if heading, _ := t.failure(); heading != "" {
		label += " (error)"
	}
	if t.cached {
		label += " (cached)"
	}
	if t.loading {
		label += "…"
	}
	return label
}

func (m model) panelDetail() string {
	switch m.workspace {
	case workspaceSearch:
		return muted(m.searchCountText())
	case workspaceWishlist:
		return muted(countLabel(len(m.wishlist), "item") + " · " + m.wishlistCadence())
	case workspaceBrowse:
		return muted(m.browseCountText())
	case workspaceTransfers:
		return muted(countLabel(len(m.transfers), "transfer"))
	case workspaceStats:
		if detail := m.panelDetailShort(); detail != "" {
			if since := m.stats.overview.Since; !since.IsZero() {
				detail += faint(" · ") + muted("since ") + subtle(statsDate(since))
			}
			return detail
		}
	case workspaceCommunity:
		return m.communityState()
	case workspaceShares:
		return muted(countLabel(len(m.shares), "folder"))
	case workspaceSettings:
		return muted("s save")
	}
	return ""
}

// panelDetailShort is the fallback detail when the full one crowds the tabs.
func (m model) panelDetailShort() string {
	if m.workspace == workspaceStats {
		if account := m.stats.overview.Account; account != "" {
			if i := strings.LastIndex(account, "/"); i >= 0 && i+1 < len(account) {
				account = account[i+1:]
			}
			return muted("account ") + subtle(account)
		}
	}
	return ""
}

func (m model) searchCountText() string {
	if m.searchFound > 0 || m.searchTotal > len(m.results) {
		return fmt.Sprintf("%d loaded / %d filtered / %d found", len(m.results), m.searchTotal, m.searchFound)
	}
	return countLabel(len(m.results), "result")
}

func (m model) browseCountText() string {
	count, singular := len(m.entries), "item"
	if len(m.browseTabs) == 0 {
		count, singular = len(m.savedBrowses), "saved user"
	}
	text := countLabel(count, singular)
	if m.browsePaged {
		text = fmt.Sprintf("%d loaded / %d total", len(m.entries), m.browseTotal)
		if m.browseFilter != "" {
			text = fmt.Sprintf("%d matches / %d total", m.browsePages[browsePageKey("", m.browseFilter)].total, m.browseTotal)
		}
	} else if m.browseFilter != "" {
		matches := browseMatchCount(m.entries, m.browseFilter)
		word := "matches"
		if matches == 1 {
			word = "match"
		}
		text = fmt.Sprintf("%d %s / %s", matches, word, countLabel(len(m.entries), "item"))
	}
	return text
}

func (m model) mainView() string {
	if m.width < 36 || m.height < 8 || m.workspace == workspaceCommunity && m.community.chats.composing && m.height < 14 {
		return m.compactView()
	}
	header := m.headerLines()
	panelHeight := max(4, m.height-len(header)-2)
	innerWidth := max(10, m.width-4)
	innerHeight := max(2, panelHeight-2)
	var body string
	switch m.workspace {
	case workspaceSearch:
		body = m.renderSearch(innerWidth, innerHeight)
	case workspaceWishlist:
		body = m.renderWishlist(innerWidth, innerHeight)
	case workspaceBrowse:
		body = m.renderBrowse(innerWidth, innerHeight)
	case workspaceTransfers:
		body = m.renderTransfers(innerWidth, innerHeight)
	case workspaceCommunity:
		body = m.renderCommunity(innerWidth, innerHeight)
	case workspaceStats:
		body = m.renderStats(innerWidth, innerHeight)
	case workspaceShares:
		body = m.renderShares(innerWidth, innerHeight)
	case workspaceSettings:
		body = m.renderSettings(innerWidth, innerHeight)
	}
	title, detail := m.panelTitle(m.width)
	parts := append(header, frame(title, detail, body, m.width, panelHeight), m.statusLine(), m.footerView())
	return strings.Join(parts, "\n")
}

// compactView keeps the essentials usable on terminals too small for the panel.
func (m model) compactView() string {
	if m.workspace == workspaceCommunity && m.community.peer.form {
		return strings.Join(m.peerPictureForm(m.width, m.height), "\n")
	}
	if m.workspace == workspaceCommunity && m.community.inspectEditing {
		return strings.Join([]string{m.workspaceTabs(m.width), renderInputWindow(m.community.input, m.community.inputCursor, m.width), trunc(m.community.inputErr, m.width), trunc("Esc back · Enter inspect", m.width)}, "\n")
	}
	if m.workspace == workspaceCommunity && m.community.chats.form != "" {
		return strings.Join(m.chatFormView(m.width, m.height), "\n")
	}
	if m.workspace == workspaceCommunity && m.community.rooms.form != "" {
		return strings.Join(m.roomFormView(m.width, m.height), "\n")
	}
	if m.workspace == workspaceCommunity && m.community.rooms.private.editing() {
		return strings.Join(m.privateRoomFormView(m.width, m.height), "\n")
	}
	if m.workspace == workspaceCommunity && m.community.view == 2 && (m.community.buddies.editor != nil || m.community.buddies.form != "") {
		return strings.Join(m.buddyEditorView(m.width, m.height), "\n")
	}
	if m.workspace == workspaceCommunity && m.community.view == 3 && m.community.discover.form != "" {
		return strings.Join(m.discoverFormView(m.width, m.height), "\n")
	}
	if m.workspace == workspaceCommunity && m.community.chats.composing {
		d := m.community.chats.drafts[m.chatKey()]
		return strings.Join(communityPane([]string{"Compose to " + m.community.chats.conversation.Target, renderInputWindow(strings.ReplaceAll(strings.ReplaceAll(d.text, "\n", "↵"), "\t", "⇥"), d.cursor, m.width), m.community.chats.err, "Enter send · Esc navigate"}, m.width, m.height, 0), "\n")
	}
	footer := muted("tab switch · o status · ? help · q quit")
	if activity := m.activityView(m.width); activity != "" {
		footer = activity
	}
	if m.workspace == workspaceBrowse {
		if heading, detail := m.browseFailure(); heading != "" {
			return strings.Join(browseErrorLines(heading, detail, m.width, m.height), "\n")
		}
	}
	lines := []string{
		trunc(brand()+" "+m.statusView(), m.width),
		m.workspaceTabs(m.width),
		trunc(danger(m.errorText()), m.width),
		trunc(footer, m.width),
	}
	return strings.Join(lines, "\n")
}

func (m model) errorText() string {
	if m.status.err != "" {
		return "Error: " + m.status.err
	}
	if m.err != "" {
		return "Error: " + m.err
	}
	if m.historyErr != "" {
		return "Error: history: " + m.historyErr
	}
	return ""
}

func (m model) markedCount() int {
	n := 0
	var marks map[string]bool
	switch m.workspace {
	case workspaceSearch, workspaceBrowse:
		for _, v := range m.selected {
			if v {
				n++
			}
		}
		return n
	case workspaceTransfers:
		marks = m.uploadSelected
		if m.transferTab == transferDownloads {
			marks = m.downloadSelected
		}
	}
	for _, v := range marks {
		if v {
			n++
		}
	}
	return n
}

// statusLine reports errors and notices on the left and background progress
// (searches, browses, share scans) or the mark count on the right. It is
// always exactly one row so messages never shift the layout.
func (m model) statusLine() string {
	width := max(1, m.width-2)
	left := ""
	if message := m.errorText(); message != "" {
		left = danger("✗ " + message)
	} else if m.notice != "" {
		left = accent("•") + " " + subtle(m.notice)
	}
	right := ""
	budget := width
	if left != "" {
		budget = max(24, width/2)
	}
	if activity := m.activityView(min(budget, 64)); activity != "" {
		right = activity
	} else if scan := m.status.shareScan; scan != nil && (scan.State == "scanning" || scan.State == "cancelling" || scan.State == "publishing") {
		label := strings.ToUpper(scan.State[:1]) + scan.State[1:]
		right = muted(trunc(fmt.Sprintf("%s shares %q: %d files, %d folders, %ds ", label, scan.Root, scan.Files, scan.Directories, scan.ElapsedMS/1000), max(4, min(budget, 64)-8))) + styled(pulseBar(m.spinner, 6), fg(theme.accent))
	} else if n := m.markedCount(); n > 0 {
		right = accent("●") + " " + subtle(fmt.Sprintf("%d marked", n))
	}
	return " " + spread(left, right, width)
}

func (m model) footerView() string {
	width := max(1, m.width-2)
	if m.confirm {
		message := "Quit and interrupt active transfers?"
		if m.status.waitForUploadsOnQuit {
			message = "Wait for active uploads, then interrupt downloads?"
		}
		return " " + trunc(danger(message)+"   "+renderHint("y confirm")+"   "+renderHint("esc cancel"), width)
	}
	return " " + hintBar(m.footerHints(), []string{"? help"}, width)
}
