package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/charmbracelet/x/ansi"
)

func (m model) View() tea.View {
	content, modal := m.screen()
	if modal && colorsEnabled() && m.width >= 40 && m.height >= 8 {
		content = overlay(m.mainView(), content, m.width, m.height)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.ReportFocus = true
	return v
}

// screen picks what fills the terminal and reports whether it is a dialog
// drawn over the workspace.
func (m model) screen() (string, bool) {
	content, modal := m.mainView(), true
	switch {
	case m.setup:
		content, modal = m.setupView(), false
	case m.commandOutput != nil:
		content = m.commandOutputView()
	case m.downloadAs != nil:
		content = m.downloadAsView()
	case m.searchScope != nil:
		content = m.searchScopeView()
	case m.privileges != nil && !m.confirm:
		content = m.privilegesView()
	case m.receivingEditor != nil && !m.confirm:
		content = m.receivingSettingsView()
	case m.awayEditor != nil && !m.confirm:
		content = m.awaySettingsView()
	case m.apiEditor != nil && !m.confirm:
		content = m.apiEditorView()
	case m.textTools != nil && !m.confirm:
		content = m.textToolsView()
	case m.privacyRules != nil && !m.confirm:
		content = m.privacyRulesView()
	case m.shareAccess != nil && !m.confirm:
		content = m.shareAccessView()
	case m.userActions != nil:
		content = m.userActionsView()
	case m.passwordForm:
		content = m.passwordFormView()
	case m.folderMenu:
		content = m.folderMenuView()
	case m.statusMenu:
		content = m.statusMenuView()
	case m.uploadStatusMenu:
		content = m.uploadStatusMenuView()
	case m.uploadConfirm:
		content = m.uploadConfirmView()
	case m.help:
		content = m.helpView()
	case m.details:
		content = m.detailView()
	default:
		modal = false
	}
	if m.community.chats.dialog != nil {
		content, modal = m.chatDialogView(), true
	} else if m.community.rooms.dialog != nil {
		content, modal = m.roomDialogView(), true
	}
	if m.community.rooms.private.dialog != nil {
		content, modal = m.privateRoomDialogView(), true
	}
	if m.community.buddies.dialog != nil {
		content, modal = m.buddyDialogView(), true
	} else if m.community.discover.dialog != nil {
		content, modal = m.discoverDialogView(), true
	}
	if m.community.peer.dialog {
		content, modal = communityConfirmationView("Save picture for "+m.community.peer.image.Username+" to "+m.community.peer.path+"? Existing files are never overwritten.", m.community.peer.confirm, m.community.peer.dialogScroll, m.width, m.height), true
	}
	return content, modal
}

// dialog centres a modal card of the given total width: title in the top
// border, the body, and a hint line. Lines are truncated to the inner width.
func (m model) dialog(title string, body []string, hints string, width int) string {
	width = max(24, min(width, m.width-4))
	inner := width - 4
	lines := []string{""}
	for _, line := range body {
		lines = append(lines, ansi.Truncate(line, inner, "…"))
	}
	if hints != "" {
		lines = append(lines, "", dialogHints(hints, inner))
	}
	card := modalFrame(title, lines, width)
	if m.width < 28 || lipgloss.Height(card) > m.height {
		// Too small for a card: plain lines, clipped to the terminal.
		plain := append([]string{accent(title)}, body...)
		if hints != "" {
			plain = append(plain, dialogHints(hints, max(1, m.width)))
		}
		plain = plain[:min(len(plain), max(1, m.height))]
		for i := range plain {
			plain[i] = ansi.Truncate(plain[i], max(1, m.width), "…")
		}
		return strings.Join(plain, "\n")
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, card)
}

// modalFrame is frame with an accent border, sized to its content.
func modalFrame(title string, lines []string, width int) string {
	inner := width - 4
	edge := func(s string) string { return styled(s, fg(theme.accent)) }
	top := edge("╭" + strings.Repeat("─", width-2) + "╮")
	if title != "" {
		head := edge("╭─") + " " + accent(ansi.Truncate(title, max(1, width-7), "…")) + " "
		top = head + edge(strings.Repeat("─", max(0, width-lipgloss.Width(head)-1))+"╮")
	}
	out := []string{top}
	for _, line := range lines {
		line = ansi.Truncate(line, inner, "…")
		out = append(out, edge("│")+" "+line+strings.Repeat(" ", max(0, inner-lipgloss.Width(line)))+" "+edge("│"))
	}
	out = append(out, edge("│"+strings.Repeat(" ", width-2)+"│"), edge("╰"+strings.Repeat("─", width-2)+"╯"))
	return strings.Join(out, "\n")
}

// dialogHints styles "key action · key action" hint text.
func dialogHints(hints string, width int) string {
	parts := strings.Split(hints, " · ")
	for i, part := range parts {
		parts[i] = renderHint(part)
	}
	return ansi.Truncate(strings.Join(parts, "   "), width, "…")
}

// buttons renders a horizontal choice such as No / Yes.
func buttons(labels []string, choice int) string {
	parts := make([]string, len(labels))
	for i, label := range labels {
		if i == choice {
			parts[i] = pill(label, theme.accent, theme.onAccent)
		} else {
			parts[i] = " " + muted(label) + " "
		}
	}
	return strings.Join(parts, "  ")
}

// formField renders a labelled input for setup-style forms.
func formField(label, raw, placeholder string, focused, secret bool, cursor, width int) string {
	value := raw
	if secret {
		value = strings.Repeat("•", utf8.RuneCountInString(raw))
	}
	if value == "" {
		value = faint(placeholder)
	}
	marker, title := "  ", subtle(label)
	if focused {
		marker, title = accent("▍ "), strong(label)
		if raw == "" {
			value = inputCursorStyle().Render("█") + faint(placeholder)
		} else {
			value = renderInput("", raw, cursor, secret, lipgloss.NewStyle())
		}
	}
	return marker + title + "\n" + marker + ansi.Truncate(value, max(4, width-2), "…")
}

func (m model) setupView() string {
	labels := []string{"Username", "Password", "Listen address", "Network interface (optional)", "Download path", "Share (name:path, optional)"}
	placeholders := []string{"Soulseek username", "Required", "0.0.0.0:50300", "Automatic (for example, wg0)", "~/Downloads/oto", "music:/home/me/Music"}
	width := max(34, min(64, m.width-4))
	body := []string{subtle("Connect directly to Soulseek."), muted("Your password stays in your local config."), ""}
	for i, label := range labels {
		body = append(body, strings.Split(formField(label, m.setupVals[i], placeholders[i], i == m.setupField, i == 1, m.inputCursor, width-4), "\n")...)
		body = append(body, "")
	}
	if m.setupErr != "" {
		body = append(body, danger("✗ "+m.setupErr))
	}
	return m.dialog("Welcome to oto", body, "tab next field · ←/→ move caret · enter next / save · esc quit", width)
}

func (m model) passwordFormView() string {
	width := max(34, min(64, m.width-4))
	body := []string{subtle("Username"), "  " + strong(m.passwordUser), ""}
	labels := []string{"New password", "Confirm new password"}
	placeholders := []string{"Required", "Enter it again"}
	for i, label := range labels {
		body = append(body, strings.Split(formField(label, m.passwordVals[i], placeholders[i], i == m.passwordField, true, m.inputCursor, width-4), "\n")...)
		body = append(body, "")
	}
	if m.passwordErr != "" {
		body = append(body, danger("✗ "+m.passwordErr))
	}
	hints := "tab next field · ←/→ move caret · enter next / change · esc cancel"
	if m.passwordChanging {
		body, hints = append(body, muted("Changing password…")), ""
	}
	return m.dialog("Change Soulseek password", body, hints, width)
}

func (m model) folderMenuView() string {
	width := min(68, m.width-4)
	inner := max(1, width-4)
	body := []string{subtle(fmt.Sprintf("%q  %q", m.folderMenuUser, m.folderMenuPath)), ""}
	for i, option := range []string{"Download folder only", "Download folder + subfolders"} {
		body = append(body, selectedRow(option, i == m.folderMenuChoice))
	}
	for i, field := range []struct{ label, value string }{{"Download root", m.folderMenuDownloadDir}, {"Folder name", m.folderMenuName}} {
		value := fmt.Sprintf("%q", field.value)
		if m.folderMenuEditing && (i == 1) == m.folderMenuRename {
			value = renderInputWindow(field.value, m.inputCursor, inner)
		}
		body = append(body, "", muted(field.label), value)
	}
	if m.folderMenuError != "" {
		body = append(body, "", danger("✗ "+m.folderMenuError))
	}
	hints := "↑/↓ choose · / edit root · n rename · enter download · esc cancel"
	if m.folderMenuEditing {
		hints = "←/→ move caret · enter done · esc done"
	}
	return m.dialog("Download folder", body, hints, width)
}

func renderInputWindow(value string, cursor, width int) string {
	runes := []rune(value)
	cursor = max(0, min(cursor, len(runes)))
	start := max(0, lipgloss.Width(string(runes[:cursor]))-width+1)
	return ansi.Cut(renderInput("", value, cursor, false, lipgloss.NewStyle()), start, start+width)
}

var presenceChoices = []daemon.Presence{daemon.PresenceOnline, daemon.PresenceAway, daemon.PresenceOffline}

func (m model) statusMenuView() string {
	var body []string
	colors := []string{success("●"), warning("●"), danger("●")}
	for i, presence := range presenceChoices {
		label := strings.ToUpper(string(presence[:1])) + string(presence[1:])
		if presence == m.status.presence {
			label += muted("  current")
		}
		marker := "  "
		if i == m.statusMenuChoice {
			marker, label = accent("› "), strong(label)
		}
		body = append(body, marker+colors[i]+" "+label)
	}
	return m.dialog("Soulseek status", body, "↑/↓ choose · enter apply · esc cancel", 44)
}

func (m model) uploadStatusMenuView() string {
	var body []string
	for i, scope := range uploadClearScopes {
		body = append(body, selectedRow(scope.label, i == m.uploadStatusChoice))
	}
	return m.dialog("Clear uploads by status", body, "↑/↓ choose · enter clear · esc cancel", 52)
}

func (m model) uploadConfirmView() string {
	title := "Confirm upload action"
	if m.forcePending != nil {
		title = "Download anyway"
	}
	if m.restoreShareExclusions {
		title = "Restore share exclusions"
	}
	width := min(64, m.width-4)
	body := strings.Split(ansi.Wrap(m.uploadConfirmLabel+"?", max(8, width-4), ""), "\n")
	body = append(body, "", buttons([]string{"No", "Yes"}, m.uploadConfirmChoice))
	return m.dialog(title, body, "←/→ choose · y yes · enter accept · esc cancel", width)
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
	width := max(34, min(76, m.width-4))
	body := make([]string, 0, len(rows))
	for _, row := range rows {
		body = append(body, muted(fmt.Sprintf("%-13s", row[0]))+" "+ansi.Truncate(row[1], width-18, "…"))
	}
	return m.dialog("File details", body, "i close · esc close", width)
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

type metadataColumn struct {
	label, value string
	width        int
	tone         string
}

// searchColumns lists the file metadata columns that fit width.
func searchColumns(x result, width int, peer bool) []metadataColumn {
	size := ""
	if !x.directory {
		size = formatBytes(x.size)
	}
	columns := []metadataColumn{{"SIZE", size, 9, ""}}
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
		columns = append(columns, metadataColumn{"RATE", quality, 6, ""}, metadataColumn{"TIME", duration, 6, ""})
	}
	if width >= 90 {
		status, tone := "", ""
		if !x.public {
			status, tone = "private", "warning"
		}
		if x.free {
			status, tone = strings.TrimSpace(status+" free"), "success"
		} else if x.queue > 0 {
			status, tone = strings.TrimSpace(status+fmt.Sprintf(" q%d", x.queue)), "warning"
		}
		columns = append(columns, metadataColumn{"STATUS", status, 12, tone})
	}
	if peer && width >= 110 {
		speed := ""
		if x.speed > 0 {
			speed = formatBytes(uint64(x.speed)) + "/s"
		}
		columns = append(columns, metadataColumn{"SPEED", speed, 11, ""})
	}
	if peer && width >= 130 {
		columns = append(columns, metadataColumn{"USER", x.user, 18, "user"})
	}
	return columns
}

func searchMetadata(x result, width int, peer bool) (string, string) {
	columns := searchColumns(x, width, peer)
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

// metadataSpans is searchMetadata's value row as coloured spans.
func metadataSpans(x result, width int, peer bool) []span {
	var spans []span
	for i, column := range searchColumns(x, width, peer) {
		if i > 0 {
			spans = append(spans, gap(1))
		}
		text := searchColumn(column.value, column.width)
		if column.label == "USER" {
			text = searchTextColumn(column.value, column.width)
		}
		switch column.tone {
		case "success":
			spans = append(spans, tinted(text, theme.success))
		case "warning":
			spans = append(spans, tinted(text, theme.warning))
		case "user":
			spans = append(spans, tinted(text, theme.accent))
		default:
			spans = append(spans, tinted(text, theme.subtext))
		}
	}
	return spans
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

func selectionMark(chosen, total int) string {
	if chosen == 0 {
		return "○"
	}
	if chosen == total {
		return "●"
	}
	return "◐"
}

func treeSelection(tree *treeState, index int, selected map[int]bool) string {
	return selectionMark(tree.selection(index, selected))
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
	return selectionMark(chosen, total)
}

func treeLabel(tree *treeState, index int) string {
	return strings.Repeat("  ", tree.depth(index)) + tree.nodes[index].label
}

// treeSpans renders the shared leading cells of a tree row: selection mark,
// expand glyph, and the indented label padded to nameWidth.
func treeSpans(tree *treeState, index int, mark string, nameWidth int) []span {
	node := tree.nodes[index]
	glyph := treeGlyph(tree, node)
	label := searchTextColumn(treeLabel(tree, index), nameWidth)
	name := plain(label)
	switch node.kind {
	case treeUser:
		name = boldSpan(label, theme.accent)
	case treeFolder, treeShareRoot:
		name = boldSpan(label, theme.text)
	case treePage:
		name = tinted(label, theme.muted)
	}
	glyphColor := theme.subtext
	if node.kind == treeFile {
		glyphColor = theme.faint
	}
	spans := []span{}
	if mark != "" {
		spans = append(spans, markSpan(mark), gap(1))
	}
	return append(spans, tinted(glyph, glyphColor), gap(1), name)
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
	if footer != "" {
		body = append(body, muted(trunc(footer, inner)))
	}
	lines := make([]string, len(body))
	for i := range body {
		lines[i] = trunc(body[i], inner)
	}
	card := modalFrame(title, lines, cardWidth)
	// modalFrame adds a spacer row before the bottom edge; drop it so the
	// card keeps the rows callers were promised.
	parts := strings.Split(card, "\n")
	card = strings.Join(append(parts[:len(parts)-2], parts[len(parts)-1]), "\n")
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, card)
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
		style = style.Foreground(theme.accent)
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

// promptLine renders a workspace input row: the key that edits it, then the
// live input with caret, the current value, or a placeholder.
func promptLine(key, value, placeholder string, editing bool, input string, cursor int, hint string) string {
	prefix := accent(key) + "  "
	if editing {
		return prefix + renderInput("", input, cursor, false, fg(theme.highlight)) + "   " + faint(hint)
	}
	if value != "" {
		return prefix + styled(value, fg(theme.text))
	}
	return prefix + faint(placeholder)
}
