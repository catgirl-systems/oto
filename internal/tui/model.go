package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/ipc"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/charmbracelet/x/ansi"
)

type snapshot struct {
	status daemon.Status
	user   string
	err    string
}
type result struct {
	user, path, extension string
	size                  uint64
	directory, free       bool
	speed, queue          uint32
	bitrate, duration     uint32
	vbr                   bool
	sampleRate, bitDepth  uint32
	public                bool
}
type entry struct {
	name, extension      string
	size                 uint64
	directory, private   bool
	bitrate, duration    uint32
	vbr                  bool
	sampleRate, bitDepth uint32
}

type searchTab struct {
	query, id, filter, filterUndo, err string
	results                            []result
	total, found, next, cursor         int
	selected                           map[int]bool
	loading, loadingMore               bool
	request, operation                 uint64
	tree                               treeState
}

type browseTab struct {
	user, target, err string
	entries           []entry
	cursor            int
	selected          map[int]bool
	loading           bool
	request           uint64
	tree              treeState
}
type transfer struct {
	id, user, filename, direction, state, err string
	done, total, speed                        uint64
	queue                                     uint32
}
type share struct{ name, path string }
type download struct {
	filename string
	size     uint64
}

type settingField struct {
	label, value string
	secret       bool
}

type model struct {
	ctx                                    context.Context
	client                                 *ipc.Client
	configPath                             string
	cfg                                    config.Config
	transient                              bool
	setup, help, confirm, editing, loading bool
	loadingMore, filterEditing             bool
	width, height                          int
	workspace, cursor, settingsSection     int
	searchTotal, searchFound, searchNext   int
	input, query, browseUser, searchID     string
	searchFilter, searchFilterUndo         string
	inputCursor                            int
	setupField                             int
	setupVals                              [5]string
	setupErr                               string
	status                                 snapshot
	results                                []result
	searchTabs                             []searchTab
	searchTabIndex                         int
	searchRequest                          uint64
	searchOperation                        uint64
	searchTree                             treeState
	entries                                []entry
	browseTabs                             []browseTab
	browseTabIndex                         int
	browseRequest                          uint64
	browseTree                             treeState
	transfers                              []transfer
	transferSampleAt                       time.Time
	transferTab                            int
	transferCursors                        [2]int
	spinner                                int
	transferTrees                          [2]treeState
	shares                                 []share
	shareTree                              treeState
	shareCursor                            int
	shareGeneration, shareRequest          uint64
	selected                               map[int]bool
	err                                    string
}

func (m model) rows() int {
	switch m.workspace {
	case 0:
		return len(m.searchTree.visible)
	case 1:
		return len(m.browseTree.visible)
	case 2:
		return len(m.transferTrees[m.transferTab].visible)
	case 3:
		return len(m.shareTree.visible)
	default:
		return len(m.settingFields())
	}
}

func newModel(ctx context.Context, c *ipc.Client, path string, transient bool, cfg config.Config) model {
	return model{ctx: ctx, client: c, configPath: path, cfg: cfg, transient: transient, width: 80, height: 24, selected: map[int]bool{}, setupVals: [5]string{cfg.Soulseek.Username, cfg.Soulseek.Password, cfg.Soulseek.ListenAddr, cfg.DownloadDir, ""}, inputCursor: utf8.RuneCountInString(cfg.Soulseek.Username)}
}
func (m model) View() tea.View {
	content := m.mainView()
	if m.setup {
		content = m.setupView()
	} else if m.help {
		content = m.helpView()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func (m model) setupView() string {
	labels := []string{"Username", "Password", "Listen address", "Download path", "Share (name:path, optional)"}
	placeholders := []string{"Soulseek username", "Required", "0.0.0.0:50300", "~/Downloads/oto", "music:/home/me/Music"}
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

func (m model) workspaceNames() []string {
	names := []string{"Search", "Browse", "Transfers", "Shares", "Settings"}
	downloads, uploads := 0, 0
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
	names[2] = fmt.Sprintf("Transfers %d↓ %d↑", downloads, uploads)
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
		if i == m.workspace {
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
	case 0:
		body = m.renderSearch(innerWidth, innerHeight)
	case 1:
		body = m.renderBrowse(innerWidth, innerHeight)
	case 2:
		body = m.renderTransfers(innerWidth, innerHeight)
	case 3:
		body = m.renderShares(innerWidth, innerHeight)
	case 4:
		body = m.renderSettings(innerWidth, innerHeight)
	}
	panel := panelStyle().Width(m.width).Height(panelHeight).Padding(0, 1).Render(body)

	parts := []string{header, tabLine, panel, m.errorView(), m.footerView()}
	return strings.Join(parts, "\n")
}

func (m model) compactView() string {
	names := m.workspaceNames()
	lines := []string{
		trunc("oto  "+string(m.status.status), m.width),
		trunc("["+names[m.workspace]+"]", m.width),
		trunc(m.errorText(), m.width),
		trunc("tab switch  ? help  q quit", m.width),
	}
	return strings.Join(lines, "\n")
}

func (m model) helpView() string {
	var b strings.Builder
	b.WriteString(styled("Keyboard guide", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#CBA6F7"))))
	b.WriteString("\n" + muted("Everything is reachable without a mouse.") + "\n\n")
	rows := [][2]string{
		{"tab / shift+tab", "switch workspace"},
		{"↑ ↓  or  j k", "move through items or fields"},
		{"← →", "collapse or expand trees; change Settings section"},
		{"home end (editing)", "move the caret to a line boundary"},
		{"ctrl+← → / ctrl+⌫", "move or delete by word while editing"},
		{"ctrl+a e u k", "jump or delete to a line boundary"},
		{"/", "edit a query, username, share, or setting"},
		{"enter", "toggle a folder or download a Search/Browse file"},
		{"f", "edit cached search filters"},
		{"c", "clear or restore search filters"},
		{"tab (filter)", "complete fields and special values"},
		{"space", "select a file or all loaded descendants"},
		{"d", "act on selected files or the cursor subtree"},
		{"r", "refresh browse, retry transfer, or rescan shares"},
		{"b (search)", "browse the selected result's user and folder"},
		{"ctrl+page up/down", "switch search, browse, or transfer tabs"},
		{"ctrl+w (results)", "close the active search or user tab"},
		{"s", "save settings and reconnect"},
		{"? / esc", "open or close this guide"},
		{"q", "quit"},
	}
	for _, row := range rows {
		fmt.Fprintf(&b, "%-20s %s\n", strong(row[0]), row[1])
	}
	cardWidth := max(34, min(72, m.width-4))
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
			duration = formatDuration(x.duration)
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

func (m model) renderSearch(width, height int) string {
	inputStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F5E0DC"))
	prompt := muted("/  Press / to search the network")
	if m.editing && !m.filterEditing {
		prompt = renderInput("/  ", m.input, m.inputCursor, false, inputStyle) + muted("   enter search  •  esc cancel")
	} else if m.query != "" {
		prompt = styled("/  "+m.query, inputStyle)
	}
	filterLine := muted("f  Press f to filter cached results")
	if m.editing && m.filterEditing {
		filterLine = renderInput("f  ", m.input, m.inputCursor, false, inputStyle) + muted("   enter apply  •  esc cancel")
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

func (m model) browseTabsLine(width int) string {
	if len(m.browseTabs) == 0 {
		return ""
	}
	labels := make([]string, len(m.browseTabs))
	for i, tab := range m.browseTabs {
		label := tab.user
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
	if m.editing {
		prompt = renderInput("/  ", m.input, m.inputCursor, false, inputStyle) + muted("   enter browse  •  esc cancel")
	} else if m.browseUser != "" {
		prompt = styled("/  "+m.browseUser, inputStyle)
	}
	lines := []string{sectionHeader("BROWSE", countLabel(len(m.entries), "item"), width)}
	if tabs := m.browseTabsLine(width); tabs != "" {
		lines = append(lines, tabs)
	}
	lines = append(lines, trunc(prompt, width))
	if m.loading && height > len(lines) {
		return strings.Join(append(lines, muted("◌  Loading shared files…")), "\n")
	}
	if len(m.entries) == 0 && height > len(lines) {
		return strings.Join(append(lines, "\n"+muted("Enter a Soulseek username to browse their shared files.")), "\n")
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
			_, metadata = searchMetadata(result{size: x.size, extension: x.extension, bitrate: x.bitrate, duration: x.duration, vbr: x.vbr, sampleRate: x.sampleRate, bitDepth: x.bitDepth, public: !x.private}, width, false)
		}
		row := fmt.Sprintf("%s %s %s  %s", mark, treeGlyph(&m.browseTree, node), searchTextColumn(treeLabel(&m.browseTree, nodeIndex), nameWidth), metadata)
		selected := node.kind == treeFile && node.source >= 0 && m.selected[node.source]
		lines = append(lines, searchResultRow(row, rowIndex == m.cursor, selected))
	}
	return strings.Join(lines, "\n")
}

func (m model) transferIndexes() []int {
	direction := "download"
	if m.transferTab == 1 {
		direction = "upload"
	}
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
	if m.transferTab == 0 {
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
	limit := max(0, height-len(lines))
	if len(indexes) == 0 && limit > 0 {
		label := "No downloads. Choose a file in Search or Browse."
		if m.transferTab == 1 {
			label = "No uploads. Shared files requested by peers appear here."
		}
		return strings.Join(append(lines, "\n"+muted(label)), "\n")
	}
	tree := &m.transferTrees[m.transferTab]
	start, end := visibleRange(len(tree.visible), m.cursor, limit)
	frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
	for rowIndex := start; rowIndex < end; rowIndex++ {
		nodeIndex := tree.visible[rowIndex]
		node := tree.nodes[nodeIndex]
		var done, total, speed uint64
		running, failed := false, false
		for _, source := range node.leaves {
			x := m.transfers[source]
			done, total, speed = done+x.done, total+x.total, speed+x.speed
			running = running || x.state == "running"
			failed = failed || x.state == "failed"
		}
		barWidth := 8
		if width >= 70 {
			barWidth = 14
		}
		bar, percent := progressBar(done, total, barWidth)
		state := fmt.Sprintf("%d transfers", len(node.leaves))
		if node.kind == treeFile && node.source >= 0 {
			x := m.transfers[node.source]
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
		status := fmt.Sprintf("%s %3d%%  %s", bar, percent, state)
		spinner := " "
		if running {
			spinner = string(frames[m.spinner%len(frames)])
		}
		direction := "↓"
		if m.transferTab == 1 {
			direction = "↑"
		}
		nameWidth := max(4, width-lipgloss.Width(status)-10)
		label := treeLabel(tree, nodeIndex)
		row := fmt.Sprintf("%s %s %s %s  %s", spinner, direction, treeGlyph(tree, node), searchTextColumn(label, nameWidth), status)
		lines = append(lines, transferResultRow(trunc(row, max(4, width-2)), rowIndex == m.cursor, m.transferTab == 1, failed))
	}
	return strings.Join(lines, "\n")
}

func (m model) renderShares(width, height int) string {
	lines := []string{sectionHeader("SHARES", countLabel(len(m.shares), "folder"), width)}
	limit := max(0, height-1)
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

func (m model) renderSettings(width, height int) string {
	sections := []string{"Account", "Connection", "Downloads"}
	lines := []string{sectionHeader("SETTINGS", "Changes reconnect the session", width)}
	sidebarWidth := max(14, min(20, width/4))
	var sidebar strings.Builder
	for i, section := range sections {
		if i == m.settingsSection {
			sidebar.WriteString(styled("› "+section, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F5E0DC"))))
		} else {
			sidebar.WriteString("  " + muted(section))
		}
		if i < len(sections)-1 {
			sidebar.WriteByte('\n')
		}
	}
	contentHeight := max(1, height-1)
	sideStyle := lipgloss.NewStyle().Width(sidebarWidth).Height(contentHeight).Border(lipgloss.NormalBorder(), false, true, false, false).PaddingRight(1)
	if colorsEnabled() {
		sideStyle = sideStyle.BorderForeground(lipgloss.Color("#45475A"))
	}

	fields := m.settingFields()
	formLines := []string{strong(sections[m.settingsSection])}
	for i, field := range fields {
		value := field.value
		if m.editing && i == m.cursor {
			value = renderInput("", m.input, m.inputCursor, field.secret, lipgloss.NewStyle())
		} else {
			if field.secret && value != "" {
				value = strings.Repeat("•", utf8.RuneCountInString(value))
			}
			if value == "" {
				value = muted("Not set")
			}
		}
		row := fmt.Sprintf("%-16s %s", field.label, value)
		formLines = append(formLines, selectedRow(trunc(row, max(4, width-sidebarWidth-4)), i == m.cursor))
	}
	if len(formLines) < contentHeight {
		formLines = append(formLines, "", muted("enter edit  •  s save  •  ← → section"))
	}
	if len(formLines) > contentHeight {
		formLines = formLines[:contentHeight]
	}
	formWidth := max(12, width-sidebarWidth-2)
	content := lipgloss.JoinHorizontal(lipgloss.Top, sideStyle.Render(sidebar.String()), lipgloss.NewStyle().Width(formWidth).PaddingLeft(2).Render(strings.Join(formLines, "\n")))
	return strings.Join(append(lines, content), "\n")
}

func (m model) settingFields() []settingField {
	switch m.settingsSection {
	case 0:
		return []settingField{{"Username", m.cfg.Soulseek.Username, false}, {"Password", m.cfg.Soulseek.Password, true}}
	case 1:
		return []settingField{{"Server", m.cfg.Soulseek.Server, false}, {"Listen address", m.cfg.Soulseek.ListenAddr, false}}
	default:
		return []settingField{{"Download path", m.cfg.DownloadDir, false}}
	}
}

func (m *model) setSettingValue(value string) {
	switch m.settingsSection {
	case 0:
		if m.cursor == 0 {
			m.cfg.Soulseek.Username = value
		} else {
			m.cfg.Soulseek.Password = value
		}
	case 1:
		if m.cursor == 0 {
			m.cfg.Soulseek.Server = value
		} else {
			m.cfg.Soulseek.ListenAddr = value
		}
	default:
		m.cfg.DownloadDir = value
	}
}

func (m model) statusView() string {
	status := string(m.status.status)
	if status == "" {
		status = "starting"
	}
	color := lipgloss.Color("#F9E2AF")
	if m.status.status == daemon.StatusConnected {
		color = lipgloss.Color("#A6E3A1")
	} else if m.status.status == daemon.StatusError || m.status.status == daemon.StatusStopped {
		color = lipgloss.Color("#F38BA8")
	}
	label := styled("●", lipgloss.NewStyle().Foreground(color)) + " " + status
	if m.status.user != "" {
		label += muted("  @" + m.status.user)
	}
	return label
}

func (m model) footerView() string {
	if m.confirm {
		return lipgloss.NewStyle().Width(m.width).Padding(0, 1).Render(danger("Quit and interrupt active transfers?  y confirm  •  esc cancel"))
	}
	hints := []string{"tab switch", "↑↓ move"}
	switch m.workspace {
	case 0:
		hints = append(hints, "←→ tree", "/ search", "ctrl+pgup/down tabs", "ctrl+w close", "f filter", "b browse folder", "space select", "d download")
	case 1:
		hints = append(hints, "←→ tree", "/ user", "ctrl+pgup/down tabs", "ctrl+w close", "r refresh", "space select", "d download")
	case 2:
		hints = append(hints, "←→ tree", "ctrl+pgup/down downloads/uploads", "d cancel", "r retry", "c clear")
	case 3:
		hints = append(hints, "←→ tree", "/ add", "d remove root", "r rescan")
	case 4:
		hints = append(hints, "←→ section", "enter edit", "s save")
	}
	hints = append(hints, "? help", "q quit")
	return lipgloss.NewStyle().Width(m.width).Padding(0, 1).Render(muted(trunc(strings.Join(hints, "  •  "), m.width-2)))
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
	return ""
}

func (m model) errorView() string {
	message := m.errorText()
	if message != "" {
		message = danger(message)
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

func formatDuration(seconds uint32) string {
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

func inputBeforeCursor(value string, cursor int) string {
	runes := []rune(value)
	return string(runes[:max(0, min(cursor, len(runes)))])
}

func insertText(value, text string, cursor int) (string, int) {
	runes, added := []rune(value), []rune(text)
	cursor = max(0, min(cursor, len(runes)))
	out := make([]rune, 0, len(runes)+len(added))
	out = append(out, runes[:cursor]...)
	out = append(out, added...)
	out = append(out, runes[cursor:]...)
	return string(out), cursor + len(added)
}

func deleteBeforeCursor(value string, cursor int) (string, int) {
	runes := []rune(value)
	cursor = max(0, min(cursor, len(runes)))
	if cursor == 0 {
		return value, cursor
	}
	return string(append(runes[:cursor-1], runes[cursor:]...)), cursor - 1
}

func deleteAtCursor(value string, cursor int) string {
	runes := []rune(value)
	cursor = max(0, min(cursor, len(runes)))
	if cursor == len(runes) {
		return value
	}
	return string(append(runes[:cursor], runes[cursor+1:]...))
}

func wordBefore(value string, cursor int) int {
	runes := []rune(value)
	cursor = max(0, min(cursor, len(runes)))
	for cursor > 0 && unicode.IsSpace(runes[cursor-1]) {
		cursor--
	}
	for cursor > 0 && !unicode.IsSpace(runes[cursor-1]) {
		cursor--
	}
	return cursor
}

func wordAfter(value string, cursor int) int {
	runes := []rune(value)
	cursor = max(0, min(cursor, len(runes)))
	for cursor < len(runes) && unicode.IsSpace(runes[cursor]) {
		cursor++
	}
	for cursor < len(runes) && !unicode.IsSpace(runes[cursor]) {
		cursor++
	}
	return cursor
}

func editText(value string, cursor int, k tea.KeyPressMsg) (string, int, bool) {
	runes := []rune(value)
	cursor = max(0, min(cursor, len(runes)))
	switch k.String() {
	case "left", "ctrl+b":
		cursor = max(0, cursor-1)
	case "right", "ctrl+f":
		cursor = min(len(runes), cursor+1)
	case "ctrl+left", "alt+left", "alt+b":
		cursor = wordBefore(value, cursor)
	case "ctrl+right", "alt+right", "alt+f":
		cursor = wordAfter(value, cursor)
	case "home", "ctrl+a":
		cursor = 0
	case "end", "ctrl+e":
		cursor = len(runes)
	case "backspace", "ctrl+h":
		value, cursor = deleteBeforeCursor(value, cursor)
	case "delete", "ctrl+d":
		value = deleteAtCursor(value, cursor)
	case "ctrl+backspace", "alt+backspace", "ctrl+w":
		start := wordBefore(value, cursor)
		value, cursor = string(append(runes[:start], runes[cursor:]...)), start
	case "ctrl+delete", "alt+delete", "alt+d":
		value = string(append(runes[:cursor], runes[wordAfter(value, cursor):]...))
	case "ctrl+u":
		value, cursor = string(runes[cursor:]), 0
	case "ctrl+k":
		value = string(runes[:cursor])
	default:
		if text := k.Key().Text; text != "" {
			value, cursor = insertText(value, text, cursor)
			return value, cursor, true
		}
		return value, cursor, false
	}
	return value, cursor, true
}

func (m *model) selectSetupField(field int) {
	m.setupField = (field + len(m.setupVals)) % len(m.setupVals)
	m.inputCursor = len([]rune(m.setupVals[m.setupField]))
}

func (m *model) setupKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if s == "esc" {
		return tea.Quit
	}
	switch s {
	case "tab", "down":
		m.selectSetupField(m.setupField + 1)
		return nil
	case "shift+tab", "up":
		m.selectSetupField(m.setupField - 1)
		return nil
	}
	if s == "enter" {
		if m.setupField < 4 {
			m.selectSetupField(m.setupField + 1)
			return nil
		}
		cfg := config.Default()
		cfg.Soulseek.Username = strings.TrimSpace(m.setupVals[0])
		cfg.Soulseek.Password = m.setupVals[1]
		cfg.Soulseek.ListenAddr = strings.TrimSpace(m.setupVals[2])
		cfg.DownloadDir = strings.TrimSpace(m.setupVals[3])
		if x := strings.TrimSpace(m.setupVals[4]); x != "" {
			p := strings.SplitN(x, ":", 2)
			if len(p) != 2 {
				m.setupErr = "share must be name:path"
				return nil
			}
			cfg.Shares = []config.Share{{Name: strings.TrimSpace(p[0]), Path: strings.TrimSpace(p[1])}}
		}
		if cfg.Soulseek.Username == "" || cfg.Soulseek.Password == "" {
			m.setupErr = "username and password are required"
			return nil
		}
		if err := cfg.Save(m.configPath); err != nil {
			m.setupErr = err.Error()
			return nil
		}
		if m.client == nil {
			return tea.Quit
		}
		m.setup = false
		return nil
	}
	m.setupVals[m.setupField], m.inputCursor, _ = editText(m.setupVals[m.setupField], m.inputCursor, k)
	return nil
}

func toResults(x []daemon.SearchResult) []result {
	r := make([]result, len(x))
	for i, v := range x {
		r[i] = result{user: v.Username, path: v.Path, extension: v.Extension, size: v.Size, directory: v.Directory, free: v.SlotFree, speed: v.Speed, queue: v.Queue, bitrate: v.Bitrate, duration: v.Duration, vbr: v.VBR, sampleRate: v.SampleRate, bitDepth: v.BitDepth, public: v.Public}
	}
	return r
}
func toEntries(x []soulseek.ShareEntry) []entry {
	r := make([]entry, len(x))
	for i, v := range x {
		r[i] = entry{name: v.Name, extension: v.Extension, size: v.Size, directory: v.Directory, private: v.Private, bitrate: v.Bitrate, duration: v.Duration, vbr: v.VBR, sampleRate: v.SampleRate, bitDepth: v.BitDepth}
	}
	return r
}
func toTransfers(x []daemon.Transfer) []transfer {
	r := make([]transfer, len(x))
	for i, v := range x {
		r[i] = transfer{id: v.ID, user: v.Username, filename: v.Filename, direction: v.Direction, state: v.State, err: v.Error, done: v.Done, total: v.Total, queue: v.Queue}
	}
	return r
}
func toShares(x []config.Share) []share {
	r := make([]share, len(x))
	for i, v := range x {
		r[i] = share{v.Name, v.Path}
	}
	return r
}
