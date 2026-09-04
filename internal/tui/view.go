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
	} else if m.passwordForm {
		content = m.passwordFormView()
	} else if m.folderMenu {
		content = m.folderMenuView()
	} else if m.statusMenu {
		content = m.statusMenuView()
	} else if m.help {
		content = m.helpView()
	} else if m.details {
		content = m.detailView()
	}
	v := tea.NewView(content)
	v.AltScreen = true
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
	names := []string{"Search", "Wishlist", "Browse", "Transfers", "Shares", "Settings"}
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
	if m.width < 36 || m.height < 8 {
		return m.compactView()
	}

	names := m.workspaceNames()
	left := styled("oto", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#CBA6F7"))) + muted("  Soulseek for your terminal")
	header := spread(left, m.statusView(), m.width-2)
	hs := lipgloss.NewStyle().Width(m.width).Padding(0, 1)
	if colorsEnabled() {
		hs = hs.Background(lipgloss.Color("#181825"))
	}
	header = hs.Render(header)

	var tabs strings.Builder
	for i, name := range names {
		if i > 0 {
			tabs.WriteString("  ")
		}
		if workspace(i) == m.workspace {
			tabs.WriteString(styled(" "+name+" ", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#11111B")).Background(lipgloss.Color("#CBA6F7"))))
		} else {
			tabs.WriteString(muted(" " + name + " "))
		}
	}
	tabLine := lipgloss.NewStyle().Width(m.width).Padding(0, 1).Render(tabs.String())

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
	case workspaceShares:
		body = m.renderShares(innerWidth, innerHeight)
	case workspaceSettings:
		body = m.renderSettings(innerWidth, innerHeight)
	}
	panel := panelStyle().Width(m.width).Height(panelHeight).Padding(0, 1).Render(body)

	parts := []string{header, tabLine, panel, m.errorView(), m.footerView()}
	return strings.Join(parts, "\n")
}

func (m model) compactView() string {
	names := m.workspaceNames()
	footer := "tab switch  o status  ? help  q quit"
	if activity := m.activityView(m.width); activity != "" {
		footer = activity
	}
	lines := []string{
		trunc("oto  "+m.statusText(), m.width),
		trunc("["+names[m.workspace]+"]", m.width),
		trunc(m.errorText(), m.width),
		trunc(footer, m.width),
	}
	return strings.Join(lines, "\n")
}

func (m model) folderMenuView() string {
	var b strings.Builder
	b.WriteString(strong("Download folder") + "\n")
	b.WriteString(muted(m.folderMenuUser+"  "+m.folderMenuPath) + "\n\n")
	options := []string{"Download folder only", "Download folder + subfolders"}
	for i, option := range options {
		marker := "  "
		if i == m.folderMenuChoice {
			marker = accent("› ")
			option = strong(option)
		}
		b.WriteString(marker + option + "\n")
	}
	b.WriteString("\n" + muted("↑↓ / j k choose  •  enter download  •  esc cancel"))
	cardWidth := max(38, min(64, m.width-4))
	card := panelStyle().Width(cardWidth).Padding(1, 2).Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, card)
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

func (m model) helpView() string {
	var b strings.Builder
	b.WriteString(accent("Keyboard"))
	groups := []struct {
		title string
		rows  [][2]string
	}{
		{"Navigation", [][2]string{
			{"tab / shift+tab", "switch workspace"},
			{"↑ ↓  or  j k", "move selection / edit history"},
			{"page up/down", "move by a page"},
			{"← →", "expand / collapse; change Settings section"},
			{"home / end", "first / last item; line boundary while editing"},
			{"ctrl+page up/down", "switch search, browse, or transfer tabs"},
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
			{"f", "edit search filters"},
			{"c", "clear / restore search filters"},
			{"w (search)", "save the active query and filter to Wishlist"},
			{"/ f r d (wishlist)", "add, edit filter, rerun, or remove a wishlist item"},
			{"space", "select item or loaded folder contents"},
			{"d", "download / choose folder mode"},
			{"r", "refresh browse / retry transfer / rescan shares"},
			{"b (search)", "browse the selected user's folder"},
			{"s", "save Browse list or Settings"},
			{"o", "choose Online, Away, or Offline"},
		}},
		{"General", [][2]string{
			{"ctrl+w (results)", "close the active search or user tab"},
			{"? / esc", "open / close this guide"},
			{"q", "quit"},
		}},
	}
	for _, group := range groups {
		b.WriteString("\n\n" + muted(strings.ToUpper(group.title)) + "\n")
		for _, row := range group.rows {
			fmt.Fprintf(&b, "%-20s %s\n", strong(row[0]), row[1])
		}
	}
	cardWidth := max(34, min(72, m.width-4))
	card := panelStyle().Width(cardWidth).Padding(1, 2).Render(b.String())
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
		x = result{path: node.path, user: m.browseUser, extension: entry.extension, size: entry.size, bitrate: entry.bitrate, duration: entry.duration, vbr: entry.vbr, sampleRate: entry.sampleRate, bitDepth: entry.bitDepth, public: !entry.private}
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
		encoding := "CBR"
		if x.vbr {
			encoding = "VBR"
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
		label += muted("  @" + m.status.user)
	}
	return label
}

func (m model) footerHints() []string {
	if m.interfaceChoosing {
		return []string{"← → choose", "enter accept", "esc cancel"}
	}
	if m.editing {
		action := "apply"
		switch {
		case m.filterEditing:
			action = "apply filter"
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
		hints := []string{"/ search", "f filter", "w wishlist"}
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
		hints := []string{"s save list", "r refresh"}
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
		return []string{"d cancel", "r retry", "c clear"}
	case workspaceShares:
		hints := []string{"/ add", "r rescan"}
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
		return style.Render(danger("Quit and interrupt active transfers?  y confirm  •  esc cancel"))
	}
	if activity := m.activityView(m.width - 2); activity != "" {
		return style.Render(activity)
	}
	actions := strings.Join(m.footerHints(), "  •  ")
	return style.Render(muted(spread(actions, "•  ? all controls", m.width-2)))
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
