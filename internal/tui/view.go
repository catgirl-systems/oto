package tui

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/charmbracelet/x/ansi"
)

func (m model) View() tea.View {
	content := m.mainView()
	if m.setup {
		content = m.setupView()
	} else if m.commandOutput != nil {
		content = m.commandOutputView()
	} else if m.downloadAs != nil {
		content = m.downloadAsView()
	} else if m.searchScope != nil {
		content = m.searchScopeView()
	} else if m.privileges != nil && !m.confirm {
		content = m.privilegesView()
	} else if m.receivingEditor != nil && !m.confirm {
		content = m.receivingSettingsView()
	} else if m.awayEditor != nil && !m.confirm {
		content = m.awaySettingsView()
	} else if m.textTools != nil && !m.confirm {
		content = m.textToolsView()
	} else if m.privacyRules != nil && !m.confirm {
		content = m.privacyRulesView()
	} else if m.shareAccess != nil && !m.confirm {
		content = m.shareAccessView()
	} else if m.userActions != nil {
		content = m.userActionsView()
	} else if m.passwordForm {
		content = m.passwordFormView()
	} else if m.folderMenu {
		content = m.folderMenuView()
	} else if m.statusMenu {
		content = m.statusMenuView()
	} else if m.uploadStatusMenu {
		content = m.uploadStatusMenuView()
	} else if m.uploadConfirm {
		content = m.uploadConfirmView()
	} else if m.help {
		content = m.helpView()
	} else if m.details {
		content = m.detailView()
	}
	if m.community.chats.dialog != nil {
		content = m.chatDialogView()
	} else if m.community.rooms.dialog != nil {
		content = m.roomDialogView()
	}
	if m.community.rooms.private.dialog != nil {
		content = m.privateRoomDialogView()
	}
	if m.community.buddies.dialog != nil {
		content = m.buddyDialogView()
	} else if m.community.discover.dialog != nil {
		content = m.discoverDialogView()
	}
	if m.community.peer.dialog {
		content = communityConfirmationView("Save picture for "+m.community.peer.image.Username+" to "+m.community.peer.path+"? Existing files are never overwritten.", m.community.peer.confirm, m.community.peer.dialogScroll, m.width, m.height)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.ReportFocus = true
	return v
}

func (m model) setupView() string {
	labels := []string{"Username", "Password", "Listen address", "Network interface (optional)", "Download path", "Share (name:path, optional)"}
	placeholders := []string{"Soulseek username", "Required", "0.0.0.0:50300", "Automatic (for example, wg0)", "~/Downloads/oto", "music:/home/me/Music"}
	var b strings.Builder
	b.WriteString(styled("oto", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#CBA6F7"))))
	b.WriteString("  First-time setup\n")
	b.WriteString(muted("Connect directly to Soulseek. Your password stays in your local config."))
	b.WriteString("\n\n")
	fieldWidth := max(24, min(52, m.width-12))
	for i, label := range labels {
		raw := m.setupVals[i]
		value := raw
		if i == 1 {
			value = strings.Repeat("•", utf8.RuneCountInString(raw))
		}
		if value == "" {
			value = muted(placeholders[i])
		}
		marker := "  "
		if i == m.setupField {
			marker = styled("› ", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#CBA6F7")))
			if raw == "" {
				value = inputCursorStyle().Render("█") + muted(placeholders[i])
			} else {
				value = renderInput("", raw, m.inputCursor, i == 1, lipgloss.NewStyle())
			}
		}
		fmt.Fprintf(&b, "%s%s\n  %s\n", marker, strong(label), trunc(value, fieldWidth))
	}
	if m.setupErr != "" {
		b.WriteString("\n" + danger("! "+m.setupErr))
	}
	b.WriteString("\n\n" + muted("↑↓ / tab fields   •   ←→ move caret   •   enter next / save   •   esc quit"))

	cardWidth := max(34, min(64, m.width-4))
	card := panelStyle().Width(cardWidth).Padding(1, 2).Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, card)
}

func (m model) passwordFormView() string {
	var b strings.Builder
	b.WriteString(styled("Change Soulseek password", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#CBA6F7"))))
	b.WriteString("\n\n" + strong("Username") + "\n  " + m.passwordUser)
	labels := []string{"New password", "Confirm new password"}
	placeholders := []string{"Required", "Enter it again"}
	fieldWidth := max(24, min(52, m.width-12))
	for i, label := range labels {
		raw := m.passwordVals[i]
		value := strings.Repeat("•", utf8.RuneCountInString(raw))
		if value == "" {
			value = muted(placeholders[i])
		}
		marker := "  "
		if i == m.passwordField {
			marker = styled("› ", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#CBA6F7")))
			if raw == "" {
				value = inputCursorStyle().Render("█") + muted(placeholders[i])
			} else {
				value = renderInput("", raw, m.inputCursor, true, lipgloss.NewStyle())
			}
		}
		fmt.Fprintf(&b, "\n\n%s%s\n  %s", marker, strong(label), trunc(value, fieldWidth))
	}
	if m.passwordErr != "" {
		b.WriteString("\n\n" + danger("! "+m.passwordErr))
	}
	if m.passwordChanging {
		b.WriteString("\n\n" + muted("Changing password…"))
	} else {
		b.WriteString("\n\n" + muted("↑↓ / tab fields   •   ←→ move caret   •   enter next / change   •   esc cancel"))
	}
	cardWidth := max(34, min(64, m.width-4))
	card := panelStyle().Width(cardWidth).Padding(1, 2).Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, card)
}

func (m model) workspaceNames() []string {
	names := []string{"Search", "Wishlist", "Browse", "Transfers", "Community", "Stats", "Shares", "Settings"}
	unread, downloads, uploads := 0, 0, 0
	for _, item := range m.wishlist {
		if item.Unread {
			unread += item.ResultCount
		}
	}
	for _, transfer := range m.transfers {
		if transfer.state != "running" {
			continue
		}
		if transfer.direction == "upload" {
			uploads++
		} else {
			downloads++
		}
	}
	if unread > 0 {
		names[workspaceWishlist] = fmt.Sprintf("Wishlist %d", unread)
	}
	names[workspaceTransfers] = fmt.Sprintf("Transfers %d↓ %d↑", downloads, uploads)
	return names
}
func (m model) mainView() string {
	if m.width < 36 || m.height < 8 || m.workspace == workspaceCommunity && m.community.chats.composing && m.height < 14 {
		return m.compactView()
	}

	left := styled("oto", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#CBA6F7")))
	if down, up := m.transferSpeeds(); down > 0 || up > 0 {
		left += muted(fmt.Sprintf("  ↓ %s/s  ↑ %s/s", formatBytes(down), formatBytes(up)))
	} else {
		left += muted("  Soulseek for your terminal")
	}
	header := spread(left, m.statusView(), m.width-2)
	hs := lipgloss.NewStyle().Width(m.width).Padding(0, 1)
	if colorsEnabled() {
		hs = hs.Background(lipgloss.Color("#181825"))
	}
	header = hs.Render(header)

	tabLine := lipgloss.NewStyle().Width(m.width).Padding(0, 1).Render(m.workspaceTabs(m.width - 2))

	panelHeight := max(4, m.height-4)
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
	panel := panelStyle().Width(m.width).Height(panelHeight).Padding(0, 1).Render(body)

	parts := []string{header, tabLine, panel, m.errorView(), m.footerView()}
	return strings.Join(parts, "\n")
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
	footer := "tab switch  o status  ? help  q quit"
	if activity := m.activityView(m.width); activity != "" {
		footer = activity
	}
	if m.workspace == workspaceBrowse {
		if heading, detail := m.browseFailure(); heading != "" {
			return strings.Join(browseErrorLines(heading, detail, m.width, m.height), "\n")
		}
	}
	lines := []string{
		trunc("oto  "+m.statusText(), m.width),
		m.workspaceTabs(m.width),
		trunc(m.errorText(), m.width),
		trunc(footer, m.width),
	}
	return strings.Join(lines, "\n")
}

func (m model) folderMenuView() string {
	width := max(1, min(64, m.width-6))
	bodyWidth := max(1, width-4)
	body := []string{strong("Download folder"), fmt.Sprintf("%q  %q", m.folderMenuUser, m.folderMenuPath), ""}
	for i, option := range []string{"Download folder only", "Download folder + subfolders"} {
		marker := "  "
		if i == m.folderMenuChoice {
			marker, option = accent("› "), strong(option)
		}
		body = append(body, marker+option)
	}
	for i, field := range []struct{ label, value string }{{"Download root", m.folderMenuDownloadDir}, {"Folder name", m.folderMenuName}} {
		value := fmt.Sprintf("%q", field.value)
		if m.folderMenuEditing && (i == 1) == m.folderMenuRename {
			value = renderInputWindow(field.value, m.inputCursor, bodyWidth)
		}
		body = append(body, "", strong(field.label), value)
	}
	body = append(body, "", m.folderMenuError)
	if m.folderMenuEditing {
		body = append(body, "←→ move · enter / esc finish editing")
	} else {
		body = append(body, "↑↓ choose · / edit root · n rename", "enter download · esc cancel")
	}
	for i := range body {
		body[i] = trunc(body[i], bodyWidth)
	}
	card := panelStyle().Width(width).Padding(1, 1).Render(strings.Join(body, "\n"))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, card)
}

func renderInputWindow(value string, cursor, width int) string {
	runes := []rune(value)
	cursor = max(0, min(cursor, len(runes)))
	start := max(0, lipgloss.Width(string(runes[:cursor]))-width+1)
	return ansi.Cut(renderInput("", value, cursor, false, lipgloss.NewStyle()), start, start+width)
}

var presenceChoices = []daemon.Presence{daemon.PresenceOnline, daemon.PresenceAway, daemon.PresenceOffline}

func (m model) statusMenuView() string {
	var b strings.Builder
	b.WriteString(strong("Soulseek status") + "\n\n")
	for i, presence := range presenceChoices {
		label := strings.ToUpper(string(presence[:1])) + string(presence[1:])
		if presence == m.status.presence {
			label += muted("  current")
		}
		marker := "  "
		if i == m.statusMenuChoice {
			marker, label = accent("› "), strong(label)
		}
		b.WriteString(marker + label + "\n")
	}
	b.WriteString("\n" + muted("↑↓ / j k choose  •  enter apply  •  esc / o cancel"))
	card := panelStyle().Width(max(34, min(48, m.width-4))).Padding(1, 2).Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, card)
}

func (m model) uploadStatusMenuView() string {
	var b strings.Builder
	b.WriteString(strong("Clear uploads by status") + "\n\n")
	for i, scope := range uploadClearScopes {
		marker, label := "  ", scope.label
		if i == m.uploadStatusChoice {
			marker, label = accent("› "), strong(label)
		}
		b.WriteString(marker + label + "\n")
	}
	b.WriteString("\n" + muted("↑↓ / j k choose  •  enter clear  •  esc cancel"))
	card := panelStyle().Width(max(1, min(64, m.width-4))).Padding(1, 2).Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, card)
}

func (m model) uploadConfirmView() string {
	var b strings.Builder
	title := "Confirm upload action"
	if m.forcePending != nil {
		title = "Download anyway"
	}
	if m.restoreShareExclusions {
		title = "Restore share exclusions"
	}
	b.WriteString(strong(title) + "\n\n")
	b.WriteString(trunc(m.uploadConfirmLabel+"?", max(4, m.width-12)) + "\n\n")
	for i, label := range []string{"No", "Yes"} {
		marker := "  "
		if i == m.uploadConfirmChoice {
			marker = accent("› ")
			label = strong(label)
		}
		b.WriteString(marker + label + "\n")
	}
	b.WriteString("\n" + muted("↑↓ / j k choose  •  enter accept  •  esc cancel"))
	card := panelStyle().Width(max(1, min(64, m.width-4))).Padding(1, 2).Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, card)
}

func (m model) helpView() string {
	rows := []string{accent("Keyboard")}
	groups := []struct {
		title string
		rows  [][2]string
	}{
		{"Navigation", [][2]string{
			{"tab / shift+tab", "switch workspace"},
			{"1 2 … 8", "jump to a workspace"},
			{"↑ ↓  or  j k", "move selection / edit history"},
			{"page up/down", "move by a page"},
			{"← →", "expand / collapse; change Settings section"},
			{"home / end", "first / last item; line boundary while editing"},
			{"ctrl+page up/down", "switch Search, Browse, Transfers or Community tabs"},
		}},
		{"Editing", [][2]string{
			{"ctrl+← → / ctrl+⌫", "move / delete by word"},
			{"ctrl+a e u k", "move / delete to a line boundary"},
			{"/", "edit the current field"},
			{"tab (filter)", "complete fields and special values"},
		}},
		{"Files & actions", [][2]string{
			{"enter", "toggle folder / download Search or Browse file"},
			{"i", "show file details"},
			{"U", "User actions: inspect / browse / search selected user"},
			{"F6 / shift+F6", "Community: next / previous pane; Esc goes back"},
			{"/ (Community)", "inspect an exact username"},
			{"N / ctrl+n (Chats)", "new / next unread conversation"},
			{"i / Enter (chat)", "compose; multiline Enter previews before send"},
			{"Tab / Esc (composer)", "complete username / return to navigation"},
			{"f / p n / End (chat)", "find history / older-newer pages / reach latest"},
			{"y / e E (chat)", "copy selected / export text or JSON to new file"},
			{"↑↓ / j k (chat)", "select message; page up/down scroll the transcript"},
			{"R / X / C (private chat)", "confirm retry / cancel selected / clear history"},
			{"ctrl+w / h (Chats list)", "close (keep history/draft) / show closed history"},
			{"N / J (Rooms)", "join/create form / join selected room"},
			{"u (Rooms / Buddies)", "search selected room / all buddies; preview query and scope"},
			{"Enter / ctrl+w (room)", "open / close history; neither changes membership"},
			{"L / R / F (room)", "leave now / remember autojoin / forget autojoin"},
			{"f / m / p n (Rooms list)", "filter / all-remembered-joined-history-invitations / pages"},
			{"G / g (public feed)", "view read-only feed / explicitly subscribe or stop"},
			{"Ctrl+P (join form)", "explicit public/private creation"},
			{"M / W / I (Rooms)", "private roles / room wall / invitation preference"},
			{"a / A (private roles)", "add exact member / operator (confirmed role required)"},
			{"o / O / d (private roles)", "grant / revoke operator / remove selected member"},
			{"c / C (private roles)", "relinquish membership / ownership; retains history"},
			{"r (private roles)", "reconcile last request ID, never duplicate an uncertain write"},
			{"i / C (wall)", "edit desired text / clear own ticker; restored after rejoin"},
			{"F6 then U (room)", "focus members, choose user, open User actions"},
			{"a / e / D (Buddies)", "add / edit note and flags / remove exact buddy (Cancel default)"},
			{"f / s / p / n (Buddies)", "filter username/note / sort all buddies / previous/next page"},
			{"Tab / Space (buddy editor)", "choose field / toggle notification, priority, or trust"},
			{"Ctrl+R / Esc (buddy editor)", "confirm reloading saved metadata / retain local draft"},
			{"↑↓ / Enter (Discover)", "choose interests / recommendations / users / self-profile"},
			{"s / i / u (Discover item)", "search files / item recommendations / related users"},
			{"a / e / D (Interests)", "add / edit like or dislike / confirmed removal"},
			{"e / Ctrl+J / Ctrl+R (self-profile)", "edit description / insert newline / confirm reload"},
			{"r / p / n (User Inspector)", "refresh peer profile / first / next interests page"},
			{"P (User Inspector)", "save cached picture to chosen path (confirm, no overwrite)"},
			{"f", "edit Search filters / find in loaded Browse list"},
			{"c", "clear / restore search filters"},
			{"w (search)", "save the active query and filter to Wishlist"},
			{"s / S (transfers)", "prepare file/folder or containing-folder search"},
			{"/ f r d (wishlist)", "add, edit filter, rerun, or remove a wishlist item"},
			{"space", "select item or loaded folder contents"},
			{"d", "download / choose folder mode and destination"},
			{"D (Search/Browse)", "download highlighted file with a different local filename"},
			{"r", "refresh browse / resume or retry transfer / rescan shares"},
			{"d / D (uploads)", "abort selected / confirm abort all for selected users"},
			{"c / C (uploads)", "confirm clear selected / clear by status"},
			{"p", "pause download subtree"},
			{"b (search)", "browse the selected user's folder"},
			{"s", "save Browse list or Settings"},
			{"o", "choose Online, Away, or Offline"},
		}},
		{"General", [][2]string{
			{"share scan", "Shares: r rescan, c cancel before publication; last index stays available"},
			{"s (Shares)", "send highlighted shared file/folder: recipient, paged preview, explicit confirmation"},
			{"elapsed / ETA", "daemon stream time; folder/user elapsed is cumulative"},
			{"Settings → Shares", "edit/add rules, d remove, restore defaults; s saves"},
			{"Settings → Bandwidth", "named upload + download limits; s saves both"},
			{"auto-clear completed", "Downloads / Uploads: opt in for future completions only"},
			{"ctrl+w (results)", "close the active search or user tab"},
			{"? / esc", "open / close this guide"},
			{"q", "quit"},
		}},
	}
	for _, group := range groups {
		rows = append(rows, "", muted(strings.ToUpper(group.title)))
		for _, row := range group.rows {
			rows = append(rows, fmt.Sprintf("%-20s %s", strong(row[0]), row[1]))
		}
	}
	visible := max(4, m.height-8)
	scroll := max(0, min(m.helpScroll, len(rows)-visible))
	window := []string{strong("oto controls") + muted("  ·  ↑↓ scroll  ·  ? / esc close")}
	window = append(window, rows[scroll:min(len(rows), scroll+visible)]...)
	if scroll+visible < len(rows) {
		window = append(window, muted("↓ more"))
	}
	cardWidth := max(34, min(72, m.width-4))
	card := panelStyle().Width(cardWidth).Padding(1, 2).Render(strings.Join(window, "\n"))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, card)
}

func (m model) detailView() string {
	tree := m.currentTree()
	if tree == nil {
		return m.mainView()
	}
	_, node := tree.node(m.cursor)
	if node == nil || node.kind != treeFile || node.source < 0 {
		return m.mainView()
	}

	x := result{path: node.path, user: node.user}
	if m.workspace == workspaceSearch && node.source < len(m.results) {
		x = m.results[node.source]
	} else if m.workspace == workspaceBrowse && node.source < len(m.entries) {
		entry := m.entries[node.source]
		x = result{path: node.path, user: m.browseUser, extension: entry.extension, size: entry.size, bitrate: entry.bitrate, duration: entry.duration, vbr: entry.vbr, vbrKnown: entry.vbrKnown, sampleRate: entry.sampleRate, bitDepth: entry.bitDepth, public: !entry.private}
	}
	access := "private"
	if x.public {
		access = "public"
	}
	rows := [][2]string{{"Path", x.path}, {"User", x.user}, {"Size", formatBytes(x.size)}, {"Access", access}}
	if x.country != "" {
		rows = append(rows, [2]string{"Country", x.country})
	}
	if x.extension != "" {
		rows = append(rows, [2]string{"Type", x.extension})
	}
	if x.bitrate > 0 {
		encoding := ""
		if x.vbr {
			encoding = "VBR"
		} else if x.vbrKnown {
			encoding = "CBR"
		}
		rows = append(rows, [2]string{"Bitrate", fmt.Sprintf("%d kbps %s", x.bitrate, encoding)})
	}
	if x.duration > 0 {
		rows = append(rows, [2]string{"Duration", formatDuration(uint64(x.duration))})
	}
	if x.sampleRate > 0 {
		rows = append(rows, [2]string{"Sample rate", fmt.Sprintf("%d Hz", x.sampleRate)})
	}
	if x.bitDepth > 0 {
		rows = append(rows, [2]string{"Bit depth", fmt.Sprintf("%d-bit", x.bitDepth)})
	}
	if m.workspace == workspaceSearch {
		availability := "queued"
		if x.free {
			availability = "free slot"
		} else if x.queue > 0 {
			availability = fmt.Sprintf("queue %d", x.queue)
		}
		rows = append(rows, [2]string{"Availability", availability})
	}

	cardWidth := max(34, min(72, m.width-4))
	var b strings.Builder
	b.WriteString(accent("File details") + "\n\n")
	for _, row := range rows {
		fmt.Fprintf(&b, "%s %s\n", strong(fmt.Sprintf("%-13s", row[0])), trunc(row[1], cardWidth-18))
	}
	b.WriteString("\n" + muted("i / esc close"))
	card := panelStyle().Width(cardWidth).Padding(1, 2).Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, card)
}

func resultPath(path string) (string, string) {
	if i := strings.LastIndexAny(path, `/\\`); i >= 0 {
		return path[:i], path[i+1:]
	}
	return "", path
}

func searchColumn(value string, width int) string {
	value = trunc(value, width)
	return strings.Repeat(" ", max(0, width-lipgloss.Width(value))) + value
}

func searchTextColumn(value string, width int) string {
	value = trunc(value, width)
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}

func searchMetadata(x result, width int, peer bool) (string, string) {
	type column struct {
		label, value string
		width        int
	}
	size := ""
	if !x.directory {
		size = formatBytes(x.size)
	}
	columns := []column{{"SIZE", size, 9}}
	if width >= 70 {
		quality := ""
		if x.bitrate > 0 {
			quality = fmt.Sprintf("%dk", x.bitrate)
			if x.vbr {
				quality += "v"
			}
		}
		duration := ""
		if x.duration > 0 {
			duration = formatDuration(uint64(x.duration))
		}
		columns = append(columns, column{"RATE", quality, 6}, column{"TIME", duration, 6})
	}
	if width >= 90 {
		status := ""
		if !x.public {
			status = "private"
		}
		if x.free {
			status = strings.TrimSpace(status + " free")
		} else if x.queue > 0 {
			status = strings.TrimSpace(status + fmt.Sprintf(" q%d", x.queue))
		}
		columns = append(columns, column{"STATUS", status, 12})
	}
	if peer && width >= 110 {
		speed := ""
		if x.speed > 0 {
			speed = formatBytes(uint64(x.speed)) + "/s"
		}
		columns = append(columns, column{"SPEED", speed, 11})
	}
	if peer && width >= 130 {
		columns = append(columns, column{"USER", x.user, 18})
	}

	headings, values := make([]string, len(columns)), make([]string, len(columns))
	for i, column := range columns {
		if column.label == "USER" {
			headings[i] = searchTextColumn(column.label, column.width)
			values[i] = searchTextColumn(column.value, column.width)
		} else {
			headings[i] = searchColumn(column.label, column.width)
			values[i] = searchColumn(column.value, column.width)
		}
	}
	return strings.Join(headings, " "), strings.Join(values, " ")
}

func treeGlyph(tree *treeState, node treeNode) string {
	if node.kind == treeFile {
		return "·"
	}
	if tree.expandedNode(node) {
		return "▾"
	}
	return "▸"
}

func treeSelection(tree *treeState, index int, selected map[int]bool) string {
	chosen, total := tree.selection(index, selected)
	if chosen == 0 {
		return "○"
	}
	if chosen == total {
		return "●"
	}
	return "◐"
}

func treeSelectionIDs(tree *treeState, index int, transfers []transfer, selected map[string]bool) string {
	chosen, total := 0, 0
	for _, source := range tree.nodes[index].leaves {
		if source >= 0 && source < len(transfers) && transfers[source].direction == "upload" {
			total++
			if selected[transfers[source].id] {
				chosen++
			}
		}
	}
	if chosen == 0 {
		return "○"
	}
	if chosen == total {
		return "●"
	}
	return "◐"
}

func treeLabel(tree *treeState, index int) string {
	return strings.Repeat("  ", tree.depth(index)) + tree.nodes[index].label
}

func (m model) searchTabsLine(width int) string {
	if len(m.searchTabs) == 0 {
		return ""
	}
	labels := make([]string, len(m.searchTabs))
	for i, tab := range m.searchTabs {
		label := tab.query
		if tab.scope == "rooms" && len(tab.rooms) > 0 {
			label = "#" + strings.Join(tab.rooms, ", ") + ": " + label
		} else if tab.scope == "buddies" {
			label = "buddies: " + label
		} else if len(tab.usernames) > 0 {
			label = "@" + strings.Join(tab.usernames, ", ") + ": " + label
		}
		if tab.loading {
			label += "…"
		}
		if i == m.searchTabIndex {
			labels[i] = accent("[" + label + "]")
		} else {
			labels[i] = muted(label)
		}
	}
	return trunc(muted("SEARCHES  ")+strings.Join(labels, muted("  ")), width)
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

func (m model) statusView() string {
	status := m.statusText()
	color := lipgloss.Color("#F9E2AF")
	if status == string(daemon.PresenceOnline) {
		color = lipgloss.Color("#A6E3A1")
	} else if status == string(daemon.PresenceOffline) || m.status.status == daemon.StatusError {
		color = lipgloss.Color("#F38BA8")
	}
	label := styled("●", lipgloss.NewStyle().Foreground(color)) + " " + status
	if m.status.user != "" {
		label += muted(" @" + m.status.user)
	}
	return label
}

func (m model) footerHints() []string {
	if m.stats.prune {
		if m.stats.prunePending {
			if m.stats.pruneConfirm {
				return []string{"pruning…"}
			}
			return []string{"loading preview…", "esc cancel"}
		}
		if m.stats.pruneConfirm {
			return []string{"enter confirm prune", "esc cancel"}
		}
		return []string{"enter preview", "l logs", "d daily", "esc cancel"}
	}
	if m.choiceChoosing {
		return []string{"← → choose", "enter accept", "esc cancel"}
	}
	if m.editing {
		action := "apply"
		switch {
		case m.filterEditing:
			action = "apply filter"
		case m.browseFindEditing:
			action = "apply find"
		case m.workspace == workspaceSearch:
			action = "search"
		case m.workspace == workspaceBrowse:
			action = "browse"
		case m.workspace == workspaceShares:
			action = "add"
		}
		return []string{"enter " + action, "esc cancel"}
	}

	switch m.workspace {
	case workspaceSearch:
		hints := []string{"/ search", "U user actions", "f filter", "w wishlist"}
		if len(m.searchTabs) > 1 {
			hints = append(hints, "ctrl+pgup/pgdn tab")
		}
		_, node := m.searchTree.node(m.cursor)
		if node == nil {
			return hints
		}
		if node.kind == treeFile {
			return append(hints, "enter/d download", "space select", "b browse", "i details")
		}
		hints = append(hints, "enter expand", "b browse")
		if node.kind == treeFolder {
			return append(hints, "d folder download")
		}
		return append(hints, "d download")
	case workspaceWishlist:
		return []string{"/ add", "f filter", "enter open", "r rerun", "d remove"}
	case workspaceBrowse:
		if len(m.browseTabs) == 0 {
			return []string{"enter open", "r refresh"}
		}
		hints := []string{"U user actions", "s save list", "r refresh"}
		if len(m.browseTabs) > 1 {
			hints = append(hints, "ctrl+pgup/pgdn tab")
		}
		if m.browseLoaded {
			hints = append([]string{"f find"}, hints...)
		}
		_, node := m.browseTree.node(m.cursor)
		if node == nil {
			return hints
		}
		if node.kind == treeFile {
			return append([]string{"enter/d download", "space select", "i details"}, hints...)
		}
		if node.kind == treeFolder {
			return append([]string{"enter expand", "d folder download"}, hints...)
		}
		return append([]string{"enter expand"}, hints...)
	case workspaceTransfers:
		hints := []string{"ctrl+pgup/pgdn ↓↑", "U user actions", "s search", "S folder search"}
		if m.transferTab == transferDownloads {
			return append(hints, "space mark files", "F download anyway", "p pause", "r resume/retry", "d cancel", "c clear")
		}
		return append(hints, "space mark", "r retry", "d abort", "D abort users", "c clear selected", "C clear status")
	case workspaceCommunity:
		if m.community.inspectEditing {
			return []string{"enter inspect", "esc back"}
		}
		if m.community.chats.composing {
			return []string{"enter send/preview", "tab complete", "esc navigate"}
		}
		if m.community.chats.form != "" {
			return []string{"enter submit", "esc cancel"}
		}
		if m.community.view == 2 && m.community.supports("buddies") {
			return []string{"a add", "e edit", "D remove", "f filter", "s sort", "p/n pages", "U actions"}
		}
		if m.community.view == 1 && m.community.supports("public-rooms") {
			if m.community.rooms.private.editing() {
				return []string{"enter submit/preview", "esc keep wall draft"}
			}
			if m.community.rooms.private.view != "" {
				return []string{"p/n pages", "r refresh/reconcile", "esc back"}
			}
			if m.community.rooms.form != "" {
				return []string{"enter submit", "tab autojoin", "esc cancel"}
			}
			if m.community.rooms.feedView {
				return []string{"g subscribe/off", "up/down scroll", "p/n pages", "esc back"}
			}
			return []string{"J join", "L leave", "R remember", "F forget", "F6 members", "G feed", "C clear", "e/E export"}
		}
		if m.community.view == 0 && m.community.supports("private-chat") {
			return []string{"N new chat", "i compose", "ctrl+n unread", "F6 panes", "f find", "e/E export", "R/X retry/cancel", "C clear"}
		}
		return []string{"F6 panes", "ctrl+pgup/down views", "/ inspect", "U actions", "esc back"}
	case workspaceStats:
		if m.stats.edit != "" {
			return []string{"enter apply", "esc cancel"}
		}
		if m.stats.detail != nil {
			return []string{"up/down scroll", "esc close"}
		}
		hints := []string{"ctrl+pgup/down pages", "a account", "/ peer"}
		switch m.stats.page {
		case 0:
			hints = append(hints, "up/down scroll", "esc clear peer")
		case 1:
			hints = append(hints, "r range", "[ / ] dates", "up/down scroll", "esc clear peer")
		case 2:
			hints = append(hints, "s sort", "d direction", "enter details", "n next", "p first")
		case 3:
			hints = append(hints, "d direction", "e outcome", "[ / ] dates", "enter details", "n next", "p first")
		case 4:
			hints = append(hints, "r refresh", "f level", "/ search", "esc clear filters")
		}
		return append(hints, "P prune")
	case workspaceShares:
		hints := []string{"/ add", "s send", "Enter access", "r rescan"}
		if scan := m.status.shareScan; scan != nil && scan.State == "scanning" {
			hints = append(hints, "c cancel scan")
		}
		_, node := m.shareTree.node(m.cursor)
		if node == nil {
			return hints
		}
		if node.kind != treeFile {
			hints = append([]string{"enter expand"}, hints...)
		}
		if node.kind == treeShareRoot {
			hints = append(hints, "d remove")
		}
		return hints
	case workspaceSettings:
		if m.shareExclusions.open {
			if m.shareExclusions.editing {
				return []string{"enter stage rule", "esc cancel edit", "← → move caret"}
			}
			if m.settingsSaving {
				return []string{"saving settings", "esc back"}
			}
			return []string{"a add", "enter edit", "d remove", "s save", "esc back", "R restore defaults"}
		}
		hints := []string{"s save"}
		fields := m.settingFields()
		if m.cursor < 0 || m.cursor >= len(fields) {
			return hints
		}
		field := fields[m.cursor]
		action := "edit"
		switch field.kind {
		case settingInfo:
			return hints
		case settingBool:
			action = "toggle"
		case settingChoice:
			action = "choose"
		case settingAction:
			switch field.id {
			case settingStatsPrune:
				action = "edit prune days"
			case settingManageShareExclusions:
				action = "manage exclusions"
			case settingChangePassword:
				action = "change password"
			case settingListeningPortStatus:
				action = "check port"
			case settingClearSearchHistory:
				action = "clear searches"
			case settingClearFilterHistory:
				action = "clear filters"
			default:
				action = "run"
			}
		}
		return append([]string{"enter " + action}, hints...)
	default:
		return nil
	}
}

func (m model) footerView() string {
	style := lipgloss.NewStyle().Width(m.width).Padding(0, 1)
	if m.confirm {
		message := "Quit and interrupt active transfers?"
		if m.status.waitForUploadsOnQuit {
			message = "Wait for active uploads, then interrupt downloads?"
		}
		return style.Render(danger(message + "  y confirm  •  esc cancel"))
	}
	if activity := m.activityView(m.width - 2); activity != "" {
		return style.Render(activity)
	}
	if scan := m.status.shareScan; !m.editing && scan != nil && (scan.State == "scanning" || scan.State == "cancelling" || scan.State == "publishing") {
		label := strings.ToUpper(scan.State[:1]) + scan.State[1:]
		return style.Render(muted(trunc(fmt.Sprintf("%s %s shares %q: %d files, %d folders, %ds", pulseBar(m.spinner, 6), label, scan.Root, scan.Files, scan.Directories, scan.ElapsedMS/1000), m.width-2)))
	}
	actions := strings.Join(m.footerHints(), "  •  ")
	return style.Render(muted(spread(actions, "•  ? all controls", m.width-2)))
}

// cardView centers the app's standard modal card over the terminal. fill receives the
// usable inner width and body row count so callers window their own content; excess
// rows are dropped and long lines truncated. Terminals too small for a border fall
// back to plain truncated lines. Card width is the total rendered width.
func (m model) cardView(title, footer string, fill func(width, rows int) []string) string {
	if m.width < 40 || m.height < 8 {
		width := max(1, m.width)
		lines := append([]string{title}, fill(width, max(0, m.height-2))...)
		if footer != "" {
			lines = append(lines, footer)
		}
		for i := range lines {
			lines[i] = trunc(lines[i], width)
		}
		return strings.Join(lines[:min(len(lines), max(1, m.height))], "\n")
	}
	cardWidth := max(34, min(84, m.width-4))
	inner := max(1, cardWidth-4)
	tail := 0
	if footer != "" {
		tail = 1
	}
	rows := max(1, m.height-4-tail)
	body := fill(inner, rows)
	if len(body) > rows {
		body = body[:rows]
	}
	lines := append([]string{strong(trunc(title, inner))}, body...)
	if footer != "" {
		lines = append(lines, muted(trunc(footer, inner)))
	}
	for i := range lines {
		lines[i] = trunc(lines[i], inner)
	}
	card := panelStyle().Width(cardWidth).Padding(0, 1).Render(strings.Join(lines, "\n"))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, card)
}

func panelStyle() lipgloss.Style {
	s := lipgloss.NewStyle().Border(lipgloss.RoundedBorder(), true)
	if colorsEnabled() {
		s = s.BorderForeground(lipgloss.Color("#45475A"))
	}
	return s
}

func colorsEnabled() bool { return os.Getenv("NO_COLOR") == "" }

func styled(s string, style lipgloss.Style) string {
	if !colorsEnabled() {
		return s
	}
	return style.Render(s)
}

func accent(s string) string {
	return styled(s, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#CBA6F7")))
}

func strong(s string) string {
	return styled(s, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#CDD6F4")))
}

func muted(s string) string {
	return styled(s, lipgloss.NewStyle().Foreground(lipgloss.Color("#7F849C")))
}

func danger(s string) string {
	return styled(s, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F38BA8")))
}

func selectedRow(s string, selected bool) string {
	if !selected {
		return "  " + s
	}
	return styled("› "+s, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F5E0DC")))
}

func searchResultRow(s string, current, selected bool) string {
	prefix := "  "
	style := lipgloss.NewStyle()
	if selected {
		style = style.Bold(true).Foreground(lipgloss.Color("#CBA6F7"))
	}
	if current {
		prefix = "› "
		style = style.Bold(true).Foreground(lipgloss.Color("#F5E0DC")).Background(lipgloss.Color("#313244"))
		if selected {
			style = style.Foreground(lipgloss.Color("#1E1E2E")).Background(lipgloss.Color("#CBA6F7"))
		}
	}
	return styled(prefix+s, style)
}

func sectionHeader(title, detail string, width int) string {
	return spread(accent(title), muted(detail), width)
}

func spread(left, right string, width int) string {
	if width <= 0 {
		return ""
	}
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	if lw+rw+1 <= width {
		return left + strings.Repeat(" ", width-lw-rw) + right
	}
	if rw >= width {
		return trunc(right, width)
	}
	if rw+1 < width {
		return trunc(left, width-rw-1) + " " + right
	}
	return trunc(left, width)
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

func (m model) errorView() string {
	message := m.errorText()
	if message != "" {
		message = danger(message)
	} else if m.notice != "" {
		message = muted(m.notice)
	}
	return lipgloss.NewStyle().Width(m.width).Padding(0, 1).Render(trunc(message, m.width-2))
}

func countLabel(n int, singular string) string {
	word := singular
	if n != 1 {
		word += "s"
	}
	return fmt.Sprintf("%d %s", n, word)
}

func visibleRange(total, cursor, limit int) (int, int) {
	if limit <= 0 || total == 0 {
		return 0, 0
	}
	if total <= limit {
		return 0, total
	}
	start := cursor - limit/2
	if start < 0 {
		start = 0
	}
	if start+limit > total {
		start = total - limit
	}
	return start, start + limit
}

func formatBytes(n uint64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	value := float64(n)
	for _, unit := range units {
		value /= 1024
		if value < 1024 || unit == "TiB" {
			if value >= 10 {
				return fmt.Sprintf("%.0f %s", value, unit)
			}
			return fmt.Sprintf("%.1f %s", value, unit)
		}
	}
	return fmt.Sprintf("%d B", n)
}

func formatDuration(seconds uint64) string {
	if seconds >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}

func trunc(s string, n int) string {
	if n < 4 {
		return ""
	}
	return ansi.Truncate(s, n, "…")
}

func inputCursorStyle() lipgloss.Style {
	style := lipgloss.NewStyle()
	if colorsEnabled() {
		style = style.Foreground(lipgloss.Color("#CBA6F7"))
	}
	return style
}

func renderInput(prefix, value string, cursor int, secret bool, style lipgloss.Style) string {
	if !colorsEnabled() {
		style = lipgloss.NewStyle()
	}
	runes := []rune(value)
	cursor = max(0, min(cursor, len(runes)))
	if secret {
		for i := range runes {
			runes[i] = '•'
		}
	}
	return style.Render(prefix+string(runes[:cursor])) + inputCursorStyle().Render("█") + style.Render(string(runes[cursor:]))
}
