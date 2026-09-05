package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func (m model) renderSearch(width, height int) string {
	inputStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F5E0DC"))
	prompt := muted("/  Press / to search the network")
	if m.editing && !m.filterEditing {
		prompt = renderInput("/  ", m.input, m.inputCursor, false, inputStyle) + muted("   ↑↓ history  •  enter search  •  esc cancel")
	} else if m.query != "" {
		prompt = styled("/  "+m.query, inputStyle)
	}
	filterLine := muted("f  Press f to filter cached results")
	if m.editing && m.filterEditing {
		filterLine = renderInput("f  ", m.input, m.inputCursor, false, inputStyle) + muted("   ↑↓ history  •  enter apply  •  esc cancel")
	} else if m.searchFilter != "" {
		filterLine = styled("f  "+m.searchFilter, inputStyle)
	}
	count := countLabel(len(m.results), "result")
	if m.searchFound > 0 || m.searchTotal > len(m.results) {
		count = fmt.Sprintf("%d loaded / %d filtered / %d found", len(m.results), m.searchTotal, m.searchFound)
	}
	lines := []string{sectionHeader("SEARCH", count, width)}
	if tabs := m.searchTabsLine(width); tabs != "" {
		lines = append(lines, tabs)
	}
	lines = append(lines, trunc(prompt, width), trunc(filterLine, width))
	if m.editing && m.filterEditing {
		lines = append(lines, trunc(muted(filterCompletionHint(inputBeforeCursor(m.input, m.inputCursor))), width))
	}
	if m.loading && height > len(lines) {
		return strings.Join(append(lines, muted("◌  Loading results…")), "\n")
	}
	if len(m.results) == 0 && height > len(lines) {
		return strings.Join(append(lines, muted("No matching results. Press / to search or f to change filters.")), "\n")
	}

	_, selectedNode := m.searchTree.node(m.cursor)
	source := "SOURCE"
	if selectedNode != nil {
		if selectedNode.user != "" {
			source += "  " + selectedNode.user
		}
		if selectedNode.path != "" {
			folder, _ := resultPath(selectedNode.path)
			if selectedNode.kind != treeFile {
				folder = selectedNode.path
			}
			if folder != "" {
				source += "  •  " + folder
			}
		}
	}
	lines = append(lines, trunc(muted(source), width))
	headings, _ := searchMetadata(result{}, width, true)
	nameWidth := max(4, width-lipgloss.Width(headings)-8)
	lines = append(lines, muted("      "+searchTextColumn("FILE", nameWidth)+"  "+headings))

	limit := max(0, height-len(lines))
	start, end := visibleRange(len(m.searchTree.visible), m.cursor, limit)
	for rowIndex := start; rowIndex < end; rowIndex++ {
		nodeIndex := m.searchTree.visible[rowIndex]
		node := m.searchTree.nodes[nodeIndex]
		mark := treeSelection(&m.searchTree, nodeIndex, m.selected)
		metadata := fmt.Sprintf("%d files", len(node.leaves))
		if node.kind == treeFile && node.source >= 0 {
			_, metadata = searchMetadata(m.results[node.source], width, true)
		}
		row := fmt.Sprintf("%s %s %s  %s", mark, treeGlyph(&m.searchTree, node), searchTextColumn(treeLabel(&m.searchTree, nodeIndex), nameWidth), metadata)
		selected := node.kind == treeFile && node.source >= 0 && m.selected[node.source]
		lines = append(lines, searchResultRow(row, rowIndex == m.cursor, selected))
	}
	return strings.Join(lines, "\n")
}

func (m model) renderWishlist(width, height int) string {
	cadence := "automatic searches off"
	if m.cfg.Search.WishlistIntervalMinutes > 0 {
		cadence = "every " + formatDuration(uint64(m.cfg.Search.WishlistIntervalMinutes)*60)
		if len(m.wishlist) > 0 && m.wishlist[0].AutomaticAvailable {
			effective := uint64(m.wishlist[0].EffectiveIntervalSeconds)
			configured := uint64(m.cfg.Search.WishlistIntervalMinutes) * 60
			if effective > configured {
				cadence += " (server minimum " + formatDuration(effective) + ")"
			}
		} else {
			cadence += " (waiting for server)"
		}
	}
	lines := []string{sectionHeader("WISHLIST", countLabel(len(m.wishlist), "item")+"  •  "+cadence, width)}
	prompt := muted("/ add  •  f edit filter  •  enter open  •  r rerun  •  d remove")
	if m.editing {
		prefix, hint := "/  ", "enter add  •  esc cancel"
		if m.filterEditing {
			prefix, hint = "f  ", "tab complete  •  enter save  •  esc cancel"
		}
		prompt = renderInput(prefix, m.input, m.inputCursor, false, lipgloss.NewStyle().Foreground(lipgloss.Color("#F5E0DC"))) + muted("   "+hint)
	}
	lines = append(lines, trunc(prompt, width))
	if m.editing && m.filterEditing {
		lines = append(lines, trunc(muted(filterCompletionHint(inputBeforeCursor(m.input, m.inputCursor))), width))
	}
	if len(m.wishlist) == 0 && height > len(lines) {
		return strings.Join(append(lines, muted("No wishlist items. Press / to add one, or w from Search.")), "\n")
	}
	limit := max(0, height-len(lines))
	start, end := visibleRange(len(m.wishlist), m.cursor, limit)
	for i := start; i < end; i++ {
		item := m.wishlist[i]
		marker := " "
		if item.Unread {
			marker = "●"
		}
		filter := ""
		if item.Filter != "" {
			filter = "  f:" + item.Filter
		}
		state := fmt.Sprintf("%d results", item.ResultCount)
		if item.Running {
			state = "searching…"
		} else if item.Error != "" {
			state = "error: " + item.Error
		} else if !item.LastRunAt.IsZero() {
			state += "  " + item.LastRunAt.Local().Format("Jan 02 15:04")
		}
		stateWidth := min(34, max(12, width/3))
		queryWidth := max(4, width-stateWidth-7)
		row := fmt.Sprintf("%s  %s  %s", marker, searchTextColumn(item.Query+filter, queryWidth), searchTextColumn(state, stateWidth))
		lines = append(lines, selectedRow(trunc(row, width), i == m.cursor))
	}
	return strings.Join(lines, "\n")
}

func (m model) browseTabsLine(width int) string {
	if len(m.browseTabs) == 0 {
		return ""
	}
	labels := make([]string, len(m.browseTabs))
	for i, tab := range m.browseTabs {
		label := tab.user
		if tab.cached {
			label += " (cached)"
		}
		if tab.loading {
			label += "…"
		}
		if i == m.browseTabIndex {
			labels[i] = accent("[" + label + "]")
		} else {
			labels[i] = muted(label)
		}
	}
	return trunc(muted("USERS  ")+strings.Join(labels, muted("  ")), width)
}

func (m model) renderBrowse(width, height int) string {
	prompt := muted("/  Press / to enter a username")
	inputStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F5E0DC"))
	if m.editing && !m.browseFindEditing {
		prompt = renderInput("/  ", m.input, m.inputCursor, false, inputStyle) + muted("   enter browse  •  esc cancel")
	} else if m.browseUser != "" {
		prompt = styled("/  "+m.browseUser, inputStyle)
	}
	count, singular := len(m.entries), "item"
	if len(m.browseTabs) == 0 {
		count, singular = len(m.savedBrowses), "saved user"
	}
	countText := countLabel(count, singular)
	if m.browseFilter != "" {
		matches := browseMatchCount(m.entries, m.browseFilter)
		matchWord := "matches"
		if matches == 1 {
			matchWord = "match"
		}
		countText = fmt.Sprintf("%d %s / %s", matches, matchWord, countLabel(len(m.entries), "item"))
	}
	lines := []string{sectionHeader("BROWSE", countText, width)}
	if tabs := m.browseTabsLine(width); tabs != "" {
		lines = append(lines, tabs)
	}
	lines = append(lines, trunc(prompt, width))
	if m.browseLoaded {
		findLine := muted("f  Press f to find in loaded shares")
		if m.editing && m.browseFindEditing {
			findLine = renderInput("f  ", m.input, m.inputCursor, false, inputStyle) + muted("   enter apply  •  esc cancel")
		} else if m.browseFilter != "" {
			findLine = styled("f  "+m.browseFilter, inputStyle)
		}
		lines = append(lines, trunc(findLine, width))
	}

	if len(m.browseTabs) == 0 {
		if m.savedBrowseLoading && height > len(lines) {
			return strings.Join(append(lines, muted("◌  Loading saved share lists…")), "\n")
		}
		if len(m.savedBrowses) == 0 && height > len(lines) {
			return strings.Join(append(lines, "\n"+muted("No saved share lists. Enter a Soulseek username to browse.")), "\n")
		}
		lines = append(lines, muted("SAVED USER"))
		limit := max(0, height-len(lines))
		start, end := visibleRange(len(m.savedBrowses), m.cursor, limit)
		for i := start; i < end; i++ {
			saved := m.savedBrowses[i]
			row := fmt.Sprintf("  %-24s  %s", saved.Username, saved.SavedAt.Local().Format("2006-01-02 15:04"))
			lines = append(lines, selectedRow(trunc(row, width), i == m.cursor))
		}
		return strings.Join(lines, "\n")
	}
	if m.loading && height > len(lines) {
		return strings.Join(append(lines, muted("◌  Loading shared files…")), "\n")
	}
	if m.browseLoaded && len(m.entries) == 0 && height > len(lines) {
		return strings.Join(append(lines, muted("No shared files.")), "\n")
	}
	if len(m.entries) == 0 && height > len(lines) {
		return strings.Join(append(lines, "\n"+muted("Enter a Soulseek username to browse their shared files.")), "\n")
	}
	if m.browseFilter != "" && len(m.browseTree.visible) == 0 && height > len(lines) {
		return strings.Join(append(lines, muted("No matching shared files. Press f to change or clear the find.")), "\n")
	}

	_, selectedNode := m.browseTree.node(m.cursor)
	folder := ""
	if selectedNode != nil {
		folder = selectedNode.path
		if selectedNode.kind == treeFile {
			folder, _ = resultPath(folder)
		}
	}
	lines = append(lines, trunc(muted("FOLDER  "+folder), width))
	headings, _ := searchMetadata(result{}, width, false)
	nameWidth := max(4, width-lipgloss.Width(headings)-8)
	lines = append(lines, muted("      "+searchTextColumn("FILE", nameWidth)+"  "+headings))

	limit := max(0, height-len(lines))
	start, end := visibleRange(len(m.browseTree.visible), m.cursor, limit)
	for rowIndex := start; rowIndex < end; rowIndex++ {
		nodeIndex := m.browseTree.visible[rowIndex]
		node := m.browseTree.nodes[nodeIndex]
		mark := treeSelection(&m.browseTree, nodeIndex, m.selected)
		metadata := fmt.Sprintf("%d files", len(node.leaves))
		if node.kind == treeFile && node.source >= 0 {
			x := m.entries[node.source]
			_, metadata = searchMetadata(result{size: x.size, extension: x.extension, bitrate: x.bitrate, duration: x.duration, vbr: x.vbr, vbrKnown: x.vbrKnown, sampleRate: x.sampleRate, bitDepth: x.bitDepth, public: !x.private}, width, false)
		}
		row := fmt.Sprintf("%s %s %s  %s", mark, treeGlyph(&m.browseTree, node), searchTextColumn(treeLabel(&m.browseTree, nodeIndex), nameWidth), metadata)
		selected := node.kind == treeFile && node.source >= 0 && m.selected[node.source]
		lines = append(lines, searchResultRow(row, rowIndex == m.cursor, selected))
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
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled), percent
}

func pulseBar(frame, width int) string {
	width = max(1, width)
	position := frame % width
	return strings.Repeat("░", position) + "█" + strings.Repeat("░", width-position-1)
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
	barWidth := max(1, width-lipgloss.Width(label)-percentWidth-2)
	if operation.total == 0 {
		return muted(label+"  ") + accent(pulseBar(m.activityFrame, barWidth))
	}
	bar, percent := progressBar(operation.received, operation.total, barWidth)
	return muted(label+"  ") + accent(bar) + fmt.Sprintf(" %3d%%", percent)
}

func transferResultRow(row string, current, upload, failed bool) string {
	color := lipgloss.Color("#89B4FA")
	if upload {
		color = lipgloss.Color("#FAB387")
	}
	if failed {
		color = lipgloss.Color("#F38BA8")
	}
	style := lipgloss.NewStyle().Foreground(color)
	prefix := "  "
	if current {
		prefix = "› "
		style = style.Bold(true).Background(lipgloss.Color("#313244"))
	}
	return styled(prefix+row, style)
}

func (m model) renderTransfers(width, height int) string {
	downloads, uploads := 0, 0
	for _, transfer := range m.transfers {
		if transfer.direction == "upload" {
			uploads++
		} else {
			downloads++
		}
	}
	downloadTab := fmt.Sprintf("↓ DOWNLOADS %d", downloads)
	uploadTab := fmt.Sprintf("↑ UPLOADS %d", uploads)
	if m.transferTab == transferDownloads {
		downloadTab = styled("["+downloadTab+"]", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89B4FA")))
		uploadTab = muted(uploadTab)
	} else {
		downloadTab = muted(downloadTab)
		uploadTab = styled("["+uploadTab+"]", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAB387")))
	}
	lines := []string{
		sectionHeader("TRANSFERS", countLabel(len(m.transfers), "transfer"), width),
		trunc(downloadTab+"  "+uploadTab, width),
	}
	indexes := m.transferIndexes()
	tree := &m.transferTrees[m.transferTab]
	if width < 110 && len(indexes) > 0 && height > len(lines) {
		if _, node := tree.node(m.cursor); node != nil {
			lines = append(lines, trunc(m.transferTimeText(*node), width))
		}
	}
	limit := max(0, height-len(lines))
	if len(indexes) == 0 && limit > 0 {
		label := "No downloads. Choose a file in Search or Browse."
		if m.transferTab == transferUploads {
			label = "No uploads. Shared files requested by peers appear here."
		}
		return strings.Join(append(lines, "\n"+muted(label)), "\n")
	}
	start, end := visibleRange(len(tree.visible), m.cursor, limit)
	frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
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
		barWidth := 8
		if width >= 70 {
			barWidth = 14
		}
		bar, percent := progressBar(done, total, barWidth)
		state := fmt.Sprintf("%d transfers", len(node.leaves))
		var x transfer
		if node.kind == treeFile && node.source >= 0 {
			x = m.transfers[node.source]
			state = x.state
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
		if speed > 0 {
			state += "  " + formatBytes(speed) + "/s"
		}
		stateWidth := 16
		if width >= 90 {
			stateWidth = 40
		}
		times := ""
		if width >= 110 {
			stateWidth = 22
			times = "  " + m.transferTimeText(node)
		}
		status := fmt.Sprintf("%s %3d%%  %s%s", bar, percent, searchTextColumn(state, stateWidth), times)
		spinner := " "
		if running {
			spinner = string(frames[m.spinner%len(frames)])
		}
		direction := "↓"
		if m.transferTab == transferUploads {
			direction = "↑"
		}
		nameWidth := max(4, width-lipgloss.Width(status)-10)
		label := treeLabel(tree, nodeIndex)
		row := fmt.Sprintf("%s %s %s %s  %s", spinner, direction, treeGlyph(tree, node), searchTextColumn(label, nameWidth), status)
		selection := m.uploadSelected
		if m.transferTab == transferDownloads {
			selection = m.downloadSelected
		}
		nameWidth = max(4, width-lipgloss.Width(status)-12)
		mark := treeSelectionIDs(tree, nodeIndex, m.transfers, selection)
		row = fmt.Sprintf("%s %s %s %s %s  %s", mark, spinner, direction, treeGlyph(tree, node), searchTextColumn(label, nameWidth), status)
		lines = append(lines, transferResultRow(trunc(row, max(4, width-2)), rowIndex == m.cursor, m.transferTab == transferUploads, failed))
	}
	return strings.Join(lines, "\n")
}

func (m model) renderShares(width, height int) string {
	lines := []string{sectionHeader("SHARES", countLabel(len(m.shares), "folder"), width)}
	if scan := m.status.shareScan; scan != nil {
		detail := fmt.Sprintf("%s  root:%q  files:%d dirs:%d  %s", scan.State, scan.Root, scan.Files, scan.Directories, (time.Duration(scan.ElapsedMS) * time.Millisecond).Round(time.Second))
		if scan.State == "scanning" || scan.State == "cancelling" || scan.State == "publishing" {
			detail += "  " + pulseBar(m.spinner, max(3, min(12, width/4)))
		}
		if scan.Error != "" {
			detail += "  error: " + strconv.Quote(scan.Error)
		}
		lines = append(lines, trunc(muted("SCAN  "+detail), width))
		audio := fmt.Sprintf("AUDIO  %d extracted / %d cached / %d failed", scan.Audio.Extracted, scan.Audio.Cached, scan.Audio.Failed)
		if !m.cfg.AudioMetadata {
			audio = "AUDIO  extraction disabled"
		} else if scan.Audio.Unavailable != "" {
			audio = "AUDIO  install ffmpeg; " + scan.Audio.Unavailable
		}
		lines = append(lines, trunc(muted(audio), width))
	}
	limit := max(0, height-len(lines))
	if m.editing && limit > 0 {
		line := renderInput("/  ", m.input, m.inputCursor, false, lipgloss.NewStyle().Foreground(lipgloss.Color("#F5E0DC"))) + muted("   name:path  •  enter add  •  esc cancel")
		lines = append(lines, trunc(line, width))
		limit--
	}
	if len(m.shares) == 0 && limit > 0 {
		return strings.Join(append(lines, "\n"+muted("No public folders. Press / to add one as name:path.")), "\n")
	}
	start, end := visibleRange(len(m.shareTree.visible), m.cursor, limit)
	frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
	for rowIndex := start; rowIndex < end; rowIndex++ {
		nodeIndex := m.shareTree.visible[rowIndex]
		node := m.shareTree.nodes[nodeIndex]
		status := ""
		if node.kind == treeShareRoot {
			status = trunc(node.detail, max(4, width/2))
		} else if node.kind == treeFile {
			status = formatBytes(node.size)
		}
		spinner := " "
		if node.loading {
			spinner = string(frames[m.spinner%len(frames)])
		}
		nameWidth := max(4, width-lipgloss.Width(status)-8)
		row := fmt.Sprintf("%s %s %s  %s", spinner, treeGlyph(&m.shareTree, node), searchTextColumn(treeLabel(&m.shareTree, nodeIndex), nameWidth), status)
		lines = append(lines, selectedRow(trunc(row, max(4, width-2)), rowIndex == m.cursor))
	}
	return strings.Join(lines, "\n")
}

var settingsSectionNames = [settingsSectionCount]string{"Account", "Connection", "Bandwidth", "Downloads", "Uploads", "Search", "Shares", "Statistics"}

func (m model) renderSettings(width, height int) string {
	if m.shareExclusions.open {
		return m.renderShareExclusions(width, height)
	}
	sections := settingsSectionNames[:]
	lines := []string{sectionHeader("SETTINGS", "Press s to save changes", width)}
	sidebarWidth := max(14, min(20, width/4))
	contentHeight := max(1, height-1)
	start, end := visibleRange(len(sections), int(m.settingsSection), contentHeight)
	var sidebar strings.Builder
	for i := start; i < end; i++ {
		section := sections[i]
		if settingsSection(i) == m.settingsSection {
			sidebar.WriteString(styled("› "+section, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F5E0DC"))))
		} else {
			sidebar.WriteString("  " + muted(section))
		}
		if i < end-1 {
			sidebar.WriteByte('\n')
		}
	}
	sideStyle := lipgloss.NewStyle().Width(sidebarWidth).Height(contentHeight).Border(lipgloss.NormalBorder(), false, true, false, false).PaddingRight(1)
	if colorsEnabled() {
		sideStyle = sideStyle.BorderForeground(lipgloss.Color("#45475A"))
	}

	fields := m.settingFields()
	labelWidth := 0
	for _, field := range fields {
		labelWidth = max(labelWidth, utf8.RuneCountInString(field.label))
	}
	formLines := []string{strong(sections[m.settingsSection])}
	fieldStart, fieldEnd := visibleRange(len(fields), m.cursor, max(1, contentHeight-1))
	for i := fieldStart; i < fieldEnd; i++ {
		field := fields[i]
		value := field.value
		if m.editing && i == m.cursor {
			value = renderInput("", m.input, m.inputCursor, field.kind == settingSecret, lipgloss.NewStyle())
		} else {
			switch field.kind {
			case settingSecret:
				if value != "" {
					value = strings.Repeat("•", utf8.RuneCountInString(value))
				}
			case settingBool:
				if value == "true" {
					value = "On"
				} else {
					value = "Off"
				}
			case settingChoice:
				value = "‹ " + value + " ›"
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
				}
			}
			if value == "" {
				value = muted("Not set")
			}
		}
		row := fmt.Sprintf("%-*s %s", labelWidth, field.label, value)
		formLines = append(formLines, selectedRow(trunc(row, max(4, width-sidebarWidth-4)), i == m.cursor))
	}
	if fieldStart == 0 && fieldEnd == len(fields) && len(formLines)+2 <= contentHeight {
		formLines = append(formLines, "", muted("enter edit/toggle/choose/run  •  s save  •  ← → section"))
	}
	formWidth := max(12, width-sidebarWidth-2)
	content := lipgloss.JoinHorizontal(lipgloss.Top, sideStyle.Render(sidebar.String()), lipgloss.NewStyle().Width(formWidth).PaddingLeft(2).Render(strings.Join(formLines, "\n")))
	return strings.Join(append(lines, content), "\n")
}

func (m model) settingFields() []settingField {
	switch m.settingsSection {
	case settingsStatistics:
		return []settingField{
			{settingStatsLogRetention, "Log retention days (0 keep forever)", strconv.Itoa(m.cfg.Statistics.LogRetentionDays), settingInt},
			{settingStatsDailyRetention, "Daily retention days (0 keep forever)", strconv.Itoa(m.cfg.Statistics.DailyRetentionDays), settingInt},
			{settingStatsASCII, "ASCII charts", strconv.FormatBool(m.cfg.Statistics.ASCIICharts), settingBool},
			{settingStatsPrune, "Prune now", "Preview and confirm", settingAction},
		}
	case settingsShares:
		return []settingField{
			{settingAudioMetadata, "Audio metadata (optional ffprobe)", strconv.FormatBool(m.cfg.AudioMetadata), settingBool},
			{settingManageShareExclusions, "Excluded content", fmt.Sprintf("%d rules · Enter to manage", len(m.cfg.ShareExclusions)), settingAction},
		}
	case settingsAccount:
		return []settingField{
			{settingUsername, "Username", m.cfg.Soulseek.Username, settingText},
			{settingChangePassword, "Change Soulseek password", "Press Enter", settingAction},
		}
	case settingsConnection:
		publicIP := m.status.publicIP
		if publicIP == "" {
			publicIP = "Unknown"
		}
		portStatus := "Unavailable while offline"
		if m.status.status == daemon.StatusConnected && m.status.publicPort != 0 {
			portStatus = "Press Enter"
			if m.portChecking {
				portStatus = fmt.Sprintf("Checking %d/tcp…", m.portCheckPort)
			} else if m.portCheckPort == m.status.publicPort {
				switch m.portCheckStatus {
				case "unknown":
					portStatus = "Status unknown"
				case "open", "closed":
					portStatus = fmt.Sprintf("%d/tcp %s", m.status.publicPort, m.portCheckStatus)
				}
			}
		}
		return []settingField{
			{settingServer, "Server", m.cfg.Soulseek.Server, settingText},
			{settingListenAddress, "Listen address", m.cfg.Soulseek.ListenAddr, settingText},
			{settingNetworkInterface, "Network interface", m.networkInterfaceValue(), settingChoice},
			{settingPublicIPAddress, "Public IP address", publicIP, settingInfo},
			{settingListeningPortStatus, "Listening port status", portStatus, settingAction},
			{settingConnectOnStartup, "Connect on startup", strconv.FormatBool(m.cfg.Soulseek.ConnectOnStartup), settingBool},
			{settingNATPMPPortMapping, "NAT-PMP port forwarding", strconv.FormatBool(m.cfg.Soulseek.NATPMPPortMapping), settingBool},
			{settingUPnPPortMapping, "UPnP port forwarding", strconv.FormatBool(m.cfg.Soulseek.UPnPPortMapping), settingBool},
		}
	case settingsDownloads:
		return append([]settingField{
			{settingDownloadPath, "Download path", m.cfg.DownloadDir, settingText},
			{settingAfterFileCommand, "After file command", m.cfg.Downloads.AfterFileCommand, settingText},
			{settingAfterFolderCommand, "After folder command", m.cfg.Downloads.AfterFolderCommand, settingText},
			{settingFileNotifications, "File notifications", strconv.FormatBool(m.cfg.Downloads.FileNotifications), settingBool},
			{settingFolderNotifications, "Folder notifications", strconv.FormatBool(m.cfg.Downloads.FolderNotifications), settingBool},
			{settingAutoClearDownloads, "Auto-clear new completed downloads", strconv.FormatBool(m.cfg.Downloads.AutoClearCompleted), settingBool},
		}, m.downloadFilterFields()...)
	case settingsBandwidth:
		profile := m.cfg.Bandwidth.ActiveProfileLimits()
		return []settingField{
			{settingBandwidthProfile, "Active profile", m.choiceValue(settingBandwidthProfile, profile.Name), settingChoice},
			{settingBandwidthProfileName, "Profile name", profile.Name, settingText},
			{settingUploadSpeedLimit, "Upload speed limit (KiB/s)", strconv.Itoa(profile.UploadSpeedLimitKiB), settingInt},
			{settingDownloadSpeedLimit, "Download speed limit (KiB/s)", strconv.Itoa(profile.DownloadSpeedLimitKiB), settingInt},
			{settingDeleteBandwidthProfile, "Delete profile", "Press Enter", settingAction},
		}
	case settingsUploads:
		return []settingField{
			{settingUploadLimitScope, "Limit applies to", m.choiceValue(settingUploadLimitScope, uploadScopeLabel(m.cfg.Uploads.LimitScope)), settingChoice},
			{settingUploadScheduling, "Scheduling", m.choiceValue(settingUploadScheduling, uploadSchedulingLabel(m.cfg.Uploads.Scheduling)), settingChoice},
			{settingAutoClearUploads, "Auto-clear new completed uploads", strconv.FormatBool(m.cfg.Uploads.AutoClearCompleted), settingBool},
			{settingUploadFileCap, "Per-user files (queued + active, 0 unlimited)", strconv.FormatUint(m.cfg.Uploads.MaxQueuedFilesPerUser, 10), settingInt},
			{settingUploadByteCap, "Per-user bytes (queued + active, 0 unlimited)", strconv.FormatUint(m.cfg.Uploads.MaxQueuedBytesPerUser, 10) + " B", settingText},
		}
	default:
		return []settingField{
			{settingRespondToIncomingSearches, "Respond to incoming searches", strconv.FormatBool(m.cfg.Search.RespondToIncomingSearches), settingBool},
			{settingMinimumIncomingSearchLength, "Minimum incoming search length", strconv.Itoa(m.cfg.Search.MinimumIncomingSearchLength), settingInt},
			{settingMaximumIncomingSearchResults, "Maximum incoming search results", strconv.Itoa(m.cfg.Search.MaximumIncomingSearchResults), settingInt},
			{settingRememberSearches, "Remember searches", strconv.FormatBool(m.cfg.Search.RememberSearches), settingBool},
			{settingSearchHistoryLimit, "Search history limit", strconv.Itoa(m.cfg.Search.SearchHistoryLimit), settingInt},
			{settingRememberFilters, "Remember filters", strconv.FormatBool(m.cfg.Search.RememberFilters), settingBool},
			{settingFilterHistoryLimit, "Filter history limit", strconv.Itoa(m.cfg.Search.FilterHistoryLimit), settingInt},
			{settingWishlistInterval, "Wishlist interval (minutes)", strconv.Itoa(m.cfg.Search.WishlistIntervalMinutes), settingInt},
			{settingWishlistNotifications, "Wishlist notifications", strconv.FormatBool(m.cfg.Search.WishlistNotifications), settingBool},
			{settingClearSearchHistory, "Clear search history", "Press Enter", settingAction},
			{settingClearFilterHistory, "Clear filter history", "Press Enter", settingAction},
			{settingDefaultFilter, "Default result filter", m.cfg.Search.DefaultFilter, settingText},
		}
	}
}

func (m *model) setSettingValue(value string) error {
	field := m.settingFields()[m.cursor]
	switch field.id {
	case settingStatsLogRetention, settingStatsDailyRetention:
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || n > 365000 {
			return errors.New("retention must be 0–365000 days")
		}
		if field.id == settingStatsLogRetention {
			m.cfg.Statistics.LogRetentionDays = n
		} else {
			m.cfg.Statistics.DailyRetentionDays = n
		}
	case settingUploadFileCap:
		n, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return err
		}
		m.cfg.Uploads.MaxQueuedFilesPerUser = n
	case settingUploadByteCap:
		n, err := parseByteLimit(value)
		if err != nil {
			return err
		}
		m.cfg.Uploads.MaxQueuedBytesPerUser = n
	case settingDownloadRule, settingAddDownloadRule:
		return m.setDownloadRule(value, field.id == settingAddDownloadRule)
	case settingUsername:
		m.cfg.Soulseek.Username = value
	case settingServer:
		m.cfg.Soulseek.Server = value
	case settingListenAddress:
		m.cfg.Soulseek.ListenAddr = value
	case settingNetworkInterface:
		if value == "" {
			return errors.New("network interface cannot be empty; choose Automatic to clear it")
		}
		m.cfg.Soulseek.NetworkInterface = value
	case settingDownloadPath:
		m.cfg.DownloadDir = value
	case settingAfterFileCommand:
		m.cfg.Downloads.AfterFileCommand = value
	case settingAfterFolderCommand:
		m.cfg.Downloads.AfterFolderCommand = value
	case settingDefaultFilter:
		if err := daemon.ValidateSearchFilter(value); err != nil {
			return err
		}
		m.cfg.Search.DefaultFilter = value
	case settingBandwidthProfile:
		if !m.addingBandwidthProfile {
			return nil
		}
		if err := m.validateBandwidthProfileName(value, -1); err != nil {
			return err
		}
		m.cfg.Bandwidth.Profiles = append(m.cfg.Bandwidth.Profiles, config.BandwidthProfile{Name: value})
		m.cfg.Bandwidth.ActiveProfile = value
		m.addingBandwidthProfile = false
	case settingBandwidthProfileName:
		i := m.activeBandwidthProfileIndex()
		if err := m.validateBandwidthProfileName(value, i); err != nil {
			return err
		}
		m.cfg.Bandwidth.Profiles[i].Name = value
		m.cfg.Bandwidth.ActiveProfile = value
	case settingUploadSpeedLimit, settingDownloadSpeedLimit, settingMinimumIncomingSearchLength, settingMaximumIncomingSearchResults, settingSearchHistoryLimit, settingFilterHistoryLimit, settingWishlistInterval:
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 0 {
			return errors.New("value must be a nonnegative integer")
		}
		switch field.id {
		case settingUploadSpeedLimit, settingDownloadSpeedLimit:
			if limit > 1000000 {
				return errors.New("speed limit must be at most 1000000 KiB/s")
			}
			if field.id == settingDownloadSpeedLimit {
				m.cfg.Bandwidth.Profiles[m.activeBandwidthProfileIndex()].DownloadSpeedLimitKiB = limit
			} else {
				m.cfg.Bandwidth.Profiles[m.activeBandwidthProfileIndex()].UploadSpeedLimitKiB = limit
			}
		case settingMinimumIncomingSearchLength:
			if limit > 50 {
				return errors.New("minimum incoming search length must be at most 50")
			}
			m.cfg.Search.MinimumIncomingSearchLength = limit
		case settingMaximumIncomingSearchResults:
			if limit < 50 || limit > 10000 {
				return errors.New("maximum incoming search results must be between 50 and 10000")
			}
			m.cfg.Search.MaximumIncomingSearchResults = limit
		case settingSearchHistoryLimit:
			m.cfg.Search.SearchHistoryLimit = limit
		case settingFilterHistoryLimit:
			m.cfg.Search.FilterHistoryLimit = limit
		default:
			if limit > 525600 {
				return errors.New("wishlist interval must be at most 525600 minutes")
			}
			m.cfg.Search.WishlistIntervalMinutes = limit
		}
	}
	return nil
}

func (m model) activeBandwidthProfileIndex() int {
	for i, profile := range m.cfg.Bandwidth.Profiles {
		if profile.Name == m.cfg.Bandwidth.ActiveProfile {
			return i
		}
	}
	return 0
}

func (m model) validateBandwidthProfileName(name string, except int) error {
	if err := config.ValidateBandwidthProfileName(name); err != nil {
		return err
	}
	for i, profile := range m.cfg.Bandwidth.Profiles {
		if i != except && strings.EqualFold(profile.Name, name) {
			return fmt.Errorf("bandwidth profile %q already exists", name)
		}
	}
	return nil
}

func uploadScopeLabel(scope config.UploadLimitScope) string {
	if scope == config.UploadLimitPerTransfer {
		return "Each transfer"
	}
	return "All transfers"
}

func uploadSchedulingLabel(scheduling config.UploadScheduling) string {
	switch scheduling {
	case config.UploadSchedulingRoundRobin:
		return "Round-robin"
	case config.UploadSchedulingRandom:
		return "Random"
	case config.UploadSchedulingSmallestFirst:
		return "Smallest first"
	default:
		return "FIFO"
	}
}

func (m model) choiceOptions(id settingID) []string {
	switch id {
	case settingNetworkInterface:
		return append(append([]string{"Automatic"}, m.networkInterfaces...), "Custom…")
	case settingBandwidthProfile:
		options := make([]string, 0, len(m.cfg.Bandwidth.Profiles)+1)
		for _, profile := range m.cfg.Bandwidth.Profiles {
			options = append(options, profile.Name)
		}
		return append(options, "New…")
	case settingUploadLimitScope:
		return []string{"All transfers", "Each transfer"}
	case settingUploadScheduling:
		return []string{"FIFO", "Round-robin", "Random", "Smallest first"}
	default:
		return nil
	}
}

func (m model) choiceValue(id settingID, current string) string {
	if m.choiceChoosing && m.choiceSetting == id {
		options := m.choiceOptions(id)
		if m.choiceIndex >= 0 && m.choiceIndex < len(options) {
			return options[m.choiceIndex]
		}
	}
	return current
}

func (m model) configuredChoice(id settingID) int {
	options := m.choiceOptions(id)
	current := ""
	switch id {
	case settingNetworkInterface:
		if m.cfg.Soulseek.NetworkInterface == "" {
			current = "Automatic"
		} else {
			current = m.cfg.Soulseek.NetworkInterface
		}
	case settingBandwidthProfile:
		current = m.cfg.Bandwidth.ActiveProfile
	case settingUploadLimitScope:
		current = uploadScopeLabel(m.cfg.Uploads.LimitScope)
	case settingUploadScheduling:
		current = uploadSchedulingLabel(m.cfg.Uploads.Scheduling)
	}
	for i, option := range options {
		if option == current {
			return i
		}
	}
	return len(options) - 1
}

func (m model) networkInterfaceValue() string {
	if m.choiceChoosing && m.choiceSetting == settingNetworkInterface {
		return m.choiceValue(settingNetworkInterface, "")
	}
	if m.cfg.Soulseek.NetworkInterface == "" {
		return "Automatic"
	}
	for _, name := range m.networkInterfaces {
		if name == m.cfg.Soulseek.NetworkInterface {
			return name
		}
	}
	return "Custom: " + m.cfg.Soulseek.NetworkInterface
}
