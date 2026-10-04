package tui

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Row anatomy shared by the tree lists: "› " cursor prefix, selection mark,
// expand glyph, the indented name, then right-hand metadata columns.
const treeRowLead = 2 + 4 // cursor prefix + mark, glyph and their gaps

var spinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// emptyState appends a blank spacer and a muted message when room remains.
func emptyState(lines []string, height int, message string) string {
	if height > len(lines)+1 {
		lines = append(lines, "")
	}
	if height > len(lines) {
		lines = append(lines, "  "+muted(message))
	}
	return strings.Join(lines, "\n")
}

func truncLines(lines []string, width int) []string {
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "…")
	}
	return lines
}

func (m model) renderSearch(width, height int) string {
	lines := []string{
		promptLine("/", m.query, "Press / to search the network", m.editing && !m.filterEditing, m.input, m.inputCursor, "enter search · ↑/↓ history · esc cancel"),
		promptLine("f", m.searchFilter, "Press f to filter results", m.editing && m.filterEditing, m.input, m.inputCursor, "enter apply · tab complete · esc cancel"),
	}
	if m.searchTabIndex >= 0 && m.searchTabIndex < len(m.searchTabs) {
		tab := &m.searchTabs[m.searchTabIndex]
		if tab.scope != "" && tab.scope != "global" {
			context := map[string]string{"users": "specific users", "buddies": "all buddies", "rooms": "joined rooms"}[tab.scope]
			lines = append(lines, muted("Scope: ")+subtle(context))
			if tab.targetCount > 0 {
				lines = append(lines, muted(fmt.Sprintf("%d captured targets", tab.targetCount)))
			}
			if tab.warning != "" {
				lines = append(lines, warning("Warning: "+tab.warning))
			}
		}
	}
	if m.editing && m.filterEditing {
		lines = append(lines, faint(filterCompletionHint(inputBeforeCursor(m.input, m.inputCursor))))
	}
	lines = truncLines(lines, width)
	if m.loading {
		return emptyState(lines, height, "◌  Loading results…")
	}
	if len(m.results) == 0 {
		return emptyState(lines, height, "No matching results. Press / to search or f to change filters.")
	}

	_, selectedNode := m.searchTree.node(m.cursor)
	source := ""
	if selectedNode != nil {
		source = accent(selectedNode.user)
		if selectedNode.path != "" {
			folder, _ := resultPath(selectedNode.path)
			if selectedNode.kind != treeFile {
				folder = selectedNode.path
			}
			if folder != "" {
				source += muted(" · ") + subtle(folder)
			}
		}
	}
	headings, _ := searchMetadata(result{}, width, true)
	nameWidth := max(4, width-lipgloss.Width(headings)-treeRowLead-2)
	lines = append(lines, "", ansi.Truncate(faint("↳ ")+source, width, "…"), columnHeader("    "+searchTextColumn("FILE", nameWidth)+"  "+headings, width))

	limit := max(0, height-len(lines))
	start, end := visibleRange(len(m.searchTree.visible), m.cursor, limit)
	for rowIndex := start; rowIndex < end; rowIndex++ {
		nodeIndex := m.searchTree.visible[rowIndex]
		node := m.searchTree.nodes[nodeIndex]
		spans := append(treeSpans(&m.searchTree, nodeIndex, treeSelection(&m.searchTree, nodeIndex, m.selected), nameWidth), gap(2))
		if node.kind == treeFile && node.source >= 0 {
			spans = append(spans, metadataSpans(m.results[node.source], width, true)...)
		} else {
			spans = append(spans, tinted(searchColumn(countLabel(len(node.leaves), "file"), lipgloss.Width(headings)), theme.muted))
		}
		marked := node.kind == treeFile && node.source >= 0 && m.selected[node.source]
		lines = append(lines, listRow(spans, width, rowIndex == m.cursor, marked))
	}
	return strings.Join(lines, "\n")
}

func (m model) wishlistCadence() string {
	if m.cfg.Search.WishlistIntervalMinutes <= 0 {
		return "automatic searches off"
	}
	cadence := "every " + formatDuration(uint64(m.cfg.Search.WishlistIntervalMinutes)*60)
	if len(m.wishlist) > 0 && m.wishlist[0].AutomaticAvailable {
		effective := uint64(m.wishlist[0].EffectiveIntervalSeconds)
		configured := uint64(m.cfg.Search.WishlistIntervalMinutes) * 60
		if effective > configured {
			cadence += " (server minimum " + formatDuration(effective) + ")"
		}
	} else {
		cadence += " (waiting for server)"
	}
	return cadence
}

func (m model) renderWishlist(width, height int) string {
	var lines []string
	if m.editing {
		if m.filterEditing {
			lines = append(lines, promptLine("f", "", "", true, m.input, m.inputCursor, "enter save · tab complete · esc cancel"), faint(filterCompletionHint(inputBeforeCursor(m.input, m.inputCursor))))
		} else {
			lines = append(lines, promptLine("a", "", "", true, m.input, m.inputCursor, "enter add · ↑/↓ history · esc cancel"))
		}
		lines = append(lines, "")
	}
	lines = truncLines(lines, width)
	if len(m.wishlist) == 0 {
		return emptyState(lines, height, "No wishlist items. Press a to add one, or w from Search.")
	}
	stateWidth := min(34, max(12, width/3))
	queryWidth := max(4, width-stateWidth-7)
	lines = append(lines, columnHeader("   "+searchTextColumn("QUERY", queryWidth)+"  LAST RUN", width))
	limit := max(0, height-len(lines))
	start, end := visibleRange(len(m.wishlist), m.cursor, limit)
	for i := start; i < end; i++ {
		item := m.wishlist[i]
		marker := " "
		if item.Unread {
			marker = "●"
		}
		query := item.Query
		filter := ""
		if item.Filter != "" {
			filter = "  f:" + item.Filter
		}
		queryCell := searchTextColumn(query+filter, queryWidth)
		queryPart := trunc(query, queryWidth)
		filterPart := strings.TrimPrefix(queryCell, queryPart)
		state, tone := fmt.Sprintf("%d results", item.ResultCount), theme.subtext
		if item.Running {
			state, tone = "searching…", theme.accent
		} else if item.Error != "" {
			state, tone = "error: "+item.Error, theme.danger
		} else if !item.LastRunAt.IsZero() {
			state += "  " + item.LastRunAt.Local().Format("Jan 02 15:04")
		}
		spans := []span{tinted(marker, theme.accent), gap(2), boldSpan(queryPart, theme.text), tinted(filterPart, theme.muted), gap(2), tinted(searchTextColumn(state, stateWidth), tone)}
		lines = append(lines, listRow(spans, width, i == m.cursor, false))
	}
	return strings.Join(lines, "\n")
}

// browseTabsLine lists the open user tabs; the panel border normally shows them.
func (m model) browseTabsLine(width int) string {
	items := make([]tabItem, len(m.browseTabs))
	for i, t := range m.browseTabs {
		items[i] = tabItem{name: browseTabLabel(t)}
	}
	s, _, _ := tabStrip(items, m.browseTabIndex, width)
	return s
}

func (m model) renderBrowse(width, height int) string {
	var lines []string
	if (m.editing && !m.browseFindEditing) || len(m.browseTabs) == 0 {
		lines = append(lines, promptLine("/", browseErrorText(m.browseUser), "Press / to enter a username", m.editing && !m.browseFindEditing, m.input, m.inputCursor, "enter browse · esc cancel"))
	}
	heading, detail := m.browseFailure()
	if heading != "" {
		if height <= len(lines) {
			lines = lines[:max(0, height-1)]
		}
		errorHeight := min(6, height-len(lines))
		if len(m.entries) > 0 {
			errorHeight = min(errorHeight, max(3, (height-len(lines))/2))
		}
		lines = append(lines, browseErrorLines(heading, detail, width, errorHeight)...)
		if len(m.entries) == 0 || height-len(lines) < 4 {
			return strings.Join(lines, "\n")
		}
	}
	if m.browseLoaded {
		lines = append(lines, promptLine("f", m.browseFilter, "Press f to find in this share list", m.editing && m.browseFindEditing, m.input, m.inputCursor, "enter find · esc cancel"))
	}
	lines = truncLines(lines, width)
	if len(m.browseTabs) == 0 {
		if m.savedBrowseLoading {
			return emptyState(lines, height, "◌  Loading saved share lists…")
		}
		if len(m.savedBrowses) == 0 {
			return emptyState(lines, height, "No saved share lists. Press / and enter a Soulseek username to browse.")
		}
		lines = append(lines, "", columnHeader(searchTextColumn("SAVED USER", 26)+"  SAVED", width))
		limit := max(0, height-len(lines))
		start, end := visibleRange(len(m.savedBrowses), m.cursor, limit)
		for i := start; i < end; i++ {
			saved := m.savedBrowses[i]
			spans := []span{boldSpan(searchTextColumn(saved.Username, 26), theme.text), gap(2), tinted(saved.SavedAt.Local().Format("2006-01-02 15:04"), theme.muted)}
			lines = append(lines, listRow(spans, width, i == m.cursor, false))
		}
		return strings.Join(lines, "\n")
	}
	if m.loading {
		return emptyState(lines, height, "◌  Loading shared files…")
	}
	if heading == "" && m.browseFilter != "" && len(m.browseTree.visible) == 0 {
		return emptyState(lines, height, "No matching shared files. Press f to change or clear the find.")
	}
	if m.browseLoaded && len(m.entries) == 0 {
		return emptyState(lines, height, "No shared files.")
	}
	if len(m.entries) == 0 {
		return emptyState(lines, height, "Enter a Soulseek username to browse their shared files.")
	}
	_, selectedNode := m.browseTree.node(m.cursor)
	folder := ""
	if selectedNode != nil {
		folder = selectedNode.path
		if selectedNode.kind == treeFile {
			folder, _ = resultPath(folder)
		}
	}
	headings, _ := searchMetadata(result{}, width, false)
	nameWidth := max(4, width-lipgloss.Width(headings)-treeRowLead-2)
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, ansi.Truncate(faint("↳ ")+accent(browseErrorText(m.browseUser))+muted(" · ")+subtle(folder), width, "…"), columnHeader("    "+searchTextColumn("FILE", nameWidth)+"  "+headings, width))
	limit := max(0, height-len(lines))
	start, end := visibleRange(len(m.browseTree.visible), m.cursor, limit)
	for rowIndex := start; rowIndex < end; rowIndex++ {
		idx := m.browseTree.visible[rowIndex]
		node := m.browseTree.nodes[idx]
		mark := treeSelection(&m.browseTree, idx, m.selected)
		if m.browsePaged {
			mark = m.remoteMark(idx)
		}
		count := len(node.leaves)
		if m.browsePaged && node.kind == treeFolder && node.source >= 0 && node.source < len(m.browseRemote) {
			count = m.browseRemote[node.source].FileCount
		}
		var meta []span
		if node.kind == treePage {
			mark = " "
		} else if node.kind == treeFile && node.source >= 0 && node.source < len(m.entries) {
			x := m.entries[node.source]
			meta = metadataSpans(result{size: x.size, extension: x.extension, bitrate: x.bitrate, duration: x.duration, vbr: x.vbr, vbrKnown: x.vbrKnown, sampleRate: x.sampleRate, bitDepth: x.bitDepth, public: !x.private}, width, false)
		} else {
			meta = []span{tinted(searchColumn(countLabel(count, "file"), lipgloss.Width(headings)), theme.muted)}
		}
		spans := append(append(treeSpans(&m.browseTree, idx, mark, nameWidth), gap(2)), meta...)
		chosen := m.selected[node.source]
		if m.browsePaged {
			chosen = m.remoteNodeChosen(idx)
		}
		lines = append(lines, listRow(spans, width, rowIndex == m.cursor, chosen))
	}
	return strings.Join(lines, "\n")
}

func (m model) transferIndexes() []int {
	direction := transferDirections[m.transferTab]
	indexes := make([]int, 0, len(m.transfers))
	for i, transfer := range m.transfers {
		if transfer.direction == direction {
			indexes = append(indexes, i)
		}
	}
	return indexes
}

func progressBar(done, total uint64, width int) (string, int) {
	percent := 0
	if total > 0 {
		percent = min(100, int(float64(done)*100/float64(total)))
	}
	filled := percent * width / 100
	return strings.Repeat("━", filled) + strings.Repeat("─", width-filled), percent
}

// progressSpans is progressBar with the filled part tinted and the rest faint.
func progressSpans(done, total uint64, width int, tone color.Color) ([]span, int) {
	bar, percent := progressBar(done, total, width)
	filled := percent * width / 100
	runes := []rune(bar)
	return []span{tinted(string(runes[:filled]), tone), tinted(string(runes[filled:]), theme.faint)}, percent
}

func pulseBar(frame, width int) string {
	width = max(1, width)
	position := frame % width
	return strings.Repeat("─", position) + "━" + strings.Repeat("─", width-position-1)
}

func (m model) currentActivity() (activity, bool) {
	if m.workspace == workspaceSearch && m.searchTabIndex >= 0 && m.searchTabIndex < len(m.searchTabs) {
		tab := m.searchTabs[m.searchTabIndex]
		if tab.searching {
			return activity{kind: activitySearch, label: tab.query, request: tab.request}, true
		}
	}
	if m.workspace == workspaceBrowse && m.browseTabIndex >= 0 && m.browseTabIndex < len(m.browseTabs) {
		tab := m.browseTabs[m.browseTabIndex]
		if tab.loading {
			return activity{kind: activityBrowse, label: tab.user, request: tab.request, received: tab.received, total: tab.total}, true
		}
	}
	browseIndex := -1
	for i := range m.browseTabs {
		tab := &m.browseTabs[i]
		if tab.loading && tab.total > 0 && (browseIndex < 0 || tab.request < m.browseTabs[browseIndex].request) {
			browseIndex = i
		}
	}
	if browseIndex < 0 {
		for i := range m.browseTabs {
			tab := &m.browseTabs[i]
			if tab.loading && (browseIndex < 0 || tab.request < m.browseTabs[browseIndex].request) {
				browseIndex = i
			}
		}
	}
	if browseIndex >= 0 {
		tab := m.browseTabs[browseIndex]
		return activity{kind: activityBrowse, label: tab.user, request: tab.request, received: tab.received, total: tab.total}, true
	}
	searchIndex := -1
	for i := range m.searchTabs {
		tab := &m.searchTabs[i]
		if tab.searching && (searchIndex < 0 || tab.request < m.searchTabs[searchIndex].request) {
			searchIndex = i
		}
	}
	if searchIndex >= 0 {
		tab := m.searchTabs[searchIndex]
		return activity{kind: activitySearch, label: tab.query, request: tab.request}, true
	}
	return activity{}, false
}

// activityView is the background-operation indicator: a label and a pulsing
// or determinate bar sized to width.
func (m model) activityView(width int) string {
	operation, ok := m.currentActivity()
	if !ok || width < 1 {
		return ""
	}
	label := "Searching " + operation.label
	if operation.kind == activityBrowse {
		label = "Browsing @" + operation.label
		if operation.total > 0 && operation.received >= operation.total {
			label = "Finishing @" + operation.label
		}
	}
	percentWidth := 0
	if operation.total > 0 {
		percentWidth = 5
	}
	barMin := max(3, min(16, width/3))
	label = trunc(label, max(4, width-barMin-percentWidth-2))
	barWidth := max(1, min(24, width-lipgloss.Width(label)-percentWidth-2))
	if operation.total == 0 {
		return subtle(label+"  ") + styled(pulseBar(m.activityFrame, barWidth), fg(theme.accent))
	}
	bar, percent := progressBar(operation.received, operation.total, barWidth)
	return subtle(label+"  ") + styled(bar, fg(theme.accent)) + fmt.Sprintf(" %3d%%", percent)
}

// transferTone colours a transfer row by direction and state.
func transferTone(upload bool, state string) color.Color {
	switch state {
	case "failed":
		return theme.danger
	case "completed":
		return theme.success
	case "queued", "paused", "cancelled", "incomplete":
		return theme.muted
	}
	if upload {
		return theme.upload
	}
	return theme.download
}

func (m model) renderTransfers(width, height int) string {
	upload := m.transferTab == transferUploads
	var lines []string
	indexes := m.transferIndexes()
	tree := &m.transferTrees[m.transferTab]
	if _, node := tree.node(m.cursor); node != nil && node.kind == treeFile && node.source >= 0 && height > len(lines)+1 {
		x := m.transfers[node.source]
		status := transferStatus(x, false)
		if status != x.state || x.err != "" {
			if x.err != "" {
				status += ": " + x.err
			}
			lines = append(lines, ansi.Truncate(styled(status, fg(transferTone(upload, x.state))), width, "…"))
		}
	}
	if width < 110 && len(indexes) > 0 && height > len(lines) {
		if _, node := tree.node(m.cursor); node != nil {
			lines = append(lines, ansi.Truncate(muted(m.transferTimeText(*node)), width, "…"))
		}
	}
	if len(indexes) == 0 {
		label := "No downloads. Choose a file in Search or Browse."
		if upload {
			label = "No uploads. Shared files requested by peers appear here."
		}
		return emptyState(lines, height, label)
	}
	if len(lines) > 0 && height-len(lines) > 3 {
		lines = append(lines, "")
	}
	limit := max(0, height-len(lines))
	start, end := visibleRange(len(tree.visible), m.cursor, limit)
	barWidth, stateWidth := 8, 16
	if width >= 70 {
		barWidth = 14
	}
	if width >= 90 {
		stateWidth = 40
	}
	if width >= 110 {
		stateWidth = 22
	}
	selection := m.uploadSelected
	if !upload {
		selection = m.downloadSelected
	}
	direction := "↓"
	if upload {
		direction = "↑"
	}
	for rowIndex := start; rowIndex < end; rowIndex++ {
		nodeIndex := tree.visible[rowIndex]
		node := tree.nodes[nodeIndex]
		var done, total, speed uint64
		running, failed := false, false
		for _, source := range node.leaves {
			x := m.transfers[source]
			done, total = addSaturated(done, x.done), addSaturated(total, x.total)
			if x.state == "running" {
				speed = addSaturated(speed, x.speed)
			}
			running = running || x.state == "running" || x.state == "finalizing" || x.state == "retrying"
			failed = failed || x.state == "failed"
		}
		state, rowState := fmt.Sprintf("%d transfers", len(node.leaves)), ""
		if running {
			rowState = "running"
		}
		if node.kind == treeFile && node.source >= 0 {
			x := m.transfers[node.source]
			rowState = x.state
			state = transferStatus(x, true)
			if x.err != "" {
				state += ": " + x.err
			}
			if x.queue > 0 {
				state += fmt.Sprintf(" q%d", x.queue)
			}
			if width >= 90 && x.user != "" {
				state += "  @" + trunc(x.user, 16)
			}
		}
		if failed {
			rowState = "failed"
		}
		if speed > 0 {
			state += "  " + formatBytes(speed) + "/s"
		}
		times := ""
		if width >= 110 {
			times = "  " + m.transferTimeText(node)
		}
		tone := transferTone(upload, rowState)
		spinner := " "
		if running {
			spinner = string(spinnerFrames[m.spinner%len(spinnerFrames)])
		}
		statusWidth := barWidth + 6 + 2 + stateWidth + ansi.StringWidth(times)
		nameWidth := max(4, width-2-8-2-statusWidth)
		bar, percent := progressSpans(done, total, barWidth, tone)
		spans := []span{markSpan(treeSelectionIDs(tree, nodeIndex, m.transfers, selection)), gap(1), tinted(spinner, theme.accent), gap(1), tinted(direction, tone), gap(1)}
		spans = append(spans, treeSpans(tree, nodeIndex, "", nameWidth)...)
		spans = append(spans, gap(2))
		spans = append(spans, bar...)
		spans = append(spans, tinted(fmt.Sprintf(" %3d%%", percent), theme.subtext), gap(2), tinted(searchTextColumn(state, stateWidth), tone), tinted(times, theme.muted))
		lines = append(lines, listRow(spans, width, rowIndex == m.cursor, false))
	}
	return strings.Join(lines, "\n")
}

func (m model) renderShares(width, height int) string {
	var lines []string
	if scan := m.status.shareScan; scan != nil {
		state := scan.State
		detail := fmt.Sprintf("root:%q  files:%d dirs:%d  %s", scan.Root, scan.Files, scan.Directories, (time.Duration(scan.ElapsedMS) * time.Millisecond).Round(time.Second))
		line := muted("Scan   ") + subtle(state) + "  " + muted(detail)
		if scan.State == "scanning" || scan.State == "cancelling" || scan.State == "publishing" {
			line += "  " + styled(pulseBar(m.spinner, max(3, min(12, width/4))), fg(theme.accent))
		}
		if scan.Error != "" {
			line += "  " + danger("error: "+strconv.Quote(scan.Error))
		}
		audio := muted("Audio  ") + subtle(fmt.Sprintf("%d extracted / %d cached / %d failed", scan.Audio.Extracted, scan.Audio.Cached, scan.Audio.Failed))
		if !m.cfg.AudioMetadata {
			audio = muted("Audio  extraction disabled")
		} else if scan.Audio.Unavailable != "" {
			audio = muted("Audio  ") + warning("install ffmpeg; "+scan.Audio.Unavailable)
		}
		lines = append(lines, line, audio)
	}
	if m.editing {
		lines = append(lines, promptLine("a", "", "", true, m.input, m.inputCursor, "name:path · enter add · esc cancel"))
	}
	lines = truncLines(lines, width)
	if len(m.shares) == 0 {
		return emptyState(lines, height, "No shared folders. Press a to add one as name:path.")
	}
	if len(lines) > 0 && height-len(lines) > 3 {
		lines = append(lines, "")
	}
	limit := max(0, height-len(lines))
	start, end := visibleRange(len(m.shareTree.visible), m.cursor, limit)
	for rowIndex := start; rowIndex < end; rowIndex++ {
		nodeIndex := m.shareTree.visible[rowIndex]
		node := m.shareTree.nodes[nodeIndex]
		var status []span
		statusWidth := 0
		if node.kind == treeShareRoot {
			access := "public"
			if node.source >= 0 && node.source < len(m.shares) {
				root := m.shares[node.source]
				if root.access != "" {
					access = root.access
				}
				if root.reveal && access != "public" {
					access += ", locked reveal"
				}
			}
			tone := theme.success
			if access != "public" {
				tone = theme.warning
			}
			text := trunc("["+access+"] "+node.detail, max(4, width/2))
			badge, rest, _ := strings.Cut(text, "] ")
			status = []span{tinted(badge+"]", tone), tinted(" "+rest, theme.muted)}
			statusWidth = ansi.StringWidth(text)
		} else if node.kind == treeFile {
			text := formatBytes(node.size)
			status, statusWidth = []span{tinted(text, theme.subtext)}, ansi.StringWidth(text)
		}
		spinner := " "
		if node.loading {
			spinner = string(spinnerFrames[m.spinner%len(spinnerFrames)])
		}
		nameWidth := max(4, width-statusWidth-8)
		spans := append([]span{tinted(spinner, theme.accent), gap(1)}, treeSpans(&m.shareTree, nodeIndex, "", nameWidth)...)
		spans = append(append(spans, gap(2)), status...)
		lines = append(lines, listRow(spans, width, rowIndex == m.cursor, false))
	}
	return strings.Join(lines, "\n")
}

var settingsSectionNames = [settingsSectionCount]string{"Account", "Connection", "Bandwidth", "Downloads", "Uploads", "Search", "Shares", "Browse", "Statistics", "Logging", "API", "Community"}

// settingValue formats a field for display, styled by kind.
func (m model) settingValue(field settingField, editing bool) span {
	value := field.value
	if editing {
		return plain(value)
	}
	switch field.kind {
	case settingSecret:
		if value != "" {
			return plain(strings.Repeat("•", utf8.RuneCountInString(value)))
		}
	case settingBool:
		if value == "true" {
			return tinted("● On", theme.success)
		}
		return tinted("○ Off", theme.muted)
	case settingChoice:
		return tinted("‹ "+value+" ›", theme.accent)
	case settingInt:
		if value == "0" {
			switch field.id {
			case settingWishlistInterval:
				value = "Off"
			case settingMinimumIncomingSearchLength:
				value = "No minimum"
			default:
				value = "Unlimited"
			}
			return tinted(value, theme.subtext)
		}
	case settingAction:
		return tinted(value, theme.accent)
	case settingInfo:
		return tinted(value, theme.subtext)
	}
	if value == "" {
		return tinted("Not set", theme.faint)
	}
	return tinted(value, theme.text)
}

// settingGroups names the group that starts at a field, splitting every
// settings section into labelled blocks. Keys are the first field of each
// group; field order and cursor indexes are unchanged.
var settingGroups = map[settingID]string{
	settingUsername:                  "Identity",
	settingAccountPrivileges:         "Privileges",
	settingBandwidthProfile:          "Profile",
	settingUploadSpeedLimit:          "Speed limits",
	settingDeleteBandwidthProfile:    "Manage profiles",
	settingAudioMetadata:             "Indexing",
	settingManageShareExclusions:     "Exclusions",
	settingBrowseMaxEntries:          "Payload limits",
	settingStatsLogRetention:         "Retention",
	settingStatsASCII:                "Display",
	settingStatsPrune:                "Maintenance",
	settingLoggingLevel:              "Diagnostics",
	settingAPIListenAddress:          "Server",
	settingAPIAuthRequests:           "Access",
	settingPrivacyRules:              "Privacy",
	settingTextTools:                 "Chat",
	settingAway:                      "Presence",
	settingServer:                    "Network",
	settingPublicIPAddress:           "Reachability",
	settingConnectOnStartup:          "Automation",
	settingDownloadPath:              "Location",
	settingAfterFileCommand:          "When finished",
	settingFileNotifications:         "Notifications",
	settingAutoClearDownloads:        "Auto-clear",
	settingDownloadFilters:           "Filename filters",
	settingReceiving:                 "Received files",
	settingUploadLimitScope:          "Scheduling",
	settingPrioritizeBuddies:         "Priority",
	settingAutoClearUploads:          "Cleanup",
	settingUploadFileCap:             "Per-user quotas",
	settingUploadSlotBandwidth:       "Slots",
	settingRespondToIncomingSearches: "Incoming searches",
	settingRememberSearches:          "History",
	settingWishlistInterval:          "Wishlist",
	settingClearSearchHistory:        "Clear history",
	settingDefaultFilter:             "Results",
}

// sectionRule renders an uppercase section title followed by a faint rule
// across width; detail, when set, sits at the right end of the rule.
func sectionRule(title, detail string, width int) string {
	label := " " + styled(strings.ToUpper(title), fg(theme.subtext).Bold(true)) + " "
	right := ""
	if detail != "" {
		right = " " + detail
	}
	// The rule runs to the last column so stacked rules share a right edge.
	rule := width - lipgloss.Width(label) - lipgloss.Width(right)
	if rule < 1 {
		return ansi.Truncate(label, max(0, width), "…")
	}
	return label + faint(strings.Repeat("─", rule)) + right
}

// settingRows lays out the fields of a section, optionally with group headers
// and blank lines between groups. It reports the cursor field's first row and
// the row of the header above it (the cursor row when there is none).
func (m model) settingRows(fields []settingField, labelWidth, formWidth, rowsPerField int, headers, spacers bool) ([]string, int, int) {
	var rows []string
	cursorRow, headerRow, lastHeader := 0, 0, -1
	for i, field := range fields {
		if title, ok := settingGroups[field.id]; ok && headers {
			if len(rows) > 0 && spacers {
				rows = append(rows, "")
			}
			lastHeader = len(rows)
			rows = append(rows, sectionRule(title, "", formWidth))
		}
		current := i == m.cursor
		if current {
			cursorRow, headerRow = len(rows), len(rows)
			if lastHeader >= 0 {
				headerRow = lastHeader
			}
		}
		value := m.settingValue(field, false)
		var valueText string
		if m.editing && current {
			valueText = renderInput("", m.input, m.inputCursor, field.kind == settingSecret, lipgloss.NewStyle())
		}
		if rowsPerField == 2 {
			rows = append(rows, listRow([]span{tinted(field.label, theme.subtext)}, formWidth, current, false))
			if valueText != "" {
				rows = append(rows, "    "+ansi.Truncate(valueText, max(4, formWidth-4), "…"))
			} else {
				rows = append(rows, listRow([]span{gap(2), value}, formWidth, false, false))
			}
			continue
		}
		if valueText != "" {
			rows = append(rows, ansi.Truncate(accent("› ")+styled(searchTextColumn(field.label, labelWidth), fg(theme.highlight).Bold(true))+" "+valueText, formWidth, "…"))
			continue
		}
		rows = append(rows, listRow([]span{tinted(searchTextColumn(field.label, labelWidth), theme.subtext), gap(1), value}, formWidth, current, false))
	}
	return rows, cursorRow, headerRow
}

func (m model) renderSettings(width, height int) string {
	if m.stats.prune {
		return m.renderStatsPrune(width, height)
	}
	if m.shareExclusions.open {
		return m.renderShareExclusions(width, height)
	}
	if width < 4 || height < 2 {
		return trunc("Settings", width)
	}
	sections := settingsSectionNames[:]
	wide := width >= 70
	sidebarWidth := max(14, min(18, width/5))
	formWidth := width
	if wide {
		formWidth = width - sidebarWidth - 3
	}
	fields := m.settingFields()
	labelWidth := 0
	for _, field := range fields {
		labelWidth = max(labelWidth, utf8.RuneCountInString(field.label))
	}
	labelWidth = min(labelWidth, max(1, formWidth-20))
	rowsPerField := 1
	if !wide {
		rowsPerField = 2
	}
	var formLines []string
	if !wide {
		formLines = []string{accent(trunc(sections[m.settingsSection], formWidth)) + "  " + faint("[ ] section"), ""}
	}
	room := max(0, height-len(formLines))
	// Headers always show; short terminals only drop the blank lines between
	// groups, and the section scrolls with the cursor.
	rows, cursorRow, headerRow := m.settingRows(fields, labelWidth, formWidth, rowsPerField, true, true)
	if len(rows) > room {
		rows, cursorRow, headerRow = m.settingRows(fields, labelWidth, formWidth, rowsPerField, true, false)
	}
	rowStart, rowEnd := visibleRange(len(rows), cursorRow, room)
	if rowStart > headerRow && cursorRow-headerRow < room {
		// Scroll up just enough to keep the cursor field's group header visible.
		rowStart, rowEnd = headerRow, min(len(rows), headerRow+room)
	}
	formLines = append(formLines, rows[rowStart:rowEnd]...)
	if m.settingsSection == settingsBrowse && len(formLines)+2 <= height {
		formLines = append(formLines, "", "  "+faint(trunc("Payload limits only; decoded entries and UI use extra RAM.", formWidth-2)))
	}
	if !wide {
		formLines[0] = muted(fmt.Sprintf("%d/%d  ", m.settingsSection+1, settingsSectionCount)) + formLines[0]
		return strings.Join(formLines[:min(len(formLines), height)], "\n")
	}
	start, end := visibleRange(len(sections), int(m.settingsSection), height)
	out := make([]string, height)
	for row := range height {
		cell := strings.Repeat(" ", sidebarWidth)
		if i := start + row; i < end {
			name := searchTextColumn(sections[i], sidebarWidth-2)
			if settingsSection(i) == m.settingsSection {
				cell = styled("▍ "+name, lipgloss.NewStyle().Bold(true).Foreground(theme.accent).Background(theme.surface))
				if !colorsEnabled() {
					cell = "› " + name
				}
			} else {
				cell = "  " + muted(name)
			}
		}
		form := ""
		if row < len(formLines) {
			form = formLines[row]
		}
		out[row] = cell + " " + borderLine("│") + " " + form
	}
	return strings.Join(out, "\n")
}
