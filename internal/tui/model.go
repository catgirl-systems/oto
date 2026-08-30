package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/slsk-tui/internal/config"
	"github.com/catgirl-systems/slsk-tui/internal/daemon"
	"github.com/catgirl-systems/slsk-tui/internal/ipc"
	"github.com/catgirl-systems/slsk-tui/internal/soulseek"
	"github.com/charmbracelet/x/ansi"
)

type snapshot struct {
	status daemon.Status
	user   string
	err    string
}
type result struct {
	user, path   string
	size         uint64
	directory    bool
	free         bool
	speed, queue uint32
}
type entry struct {
	name      string
	size      uint64
	directory bool
}
type transfer struct {
	id, user, filename, direction, state string
	done, total                          uint64
	queue                                uint32
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
	width, height                          int
	workspace, cursor, settingsSection     int
	input, query, browseUser               string
	setupField                             int
	setupVals                              [5]string
	setupErr                               string
	status                                 snapshot
	results                                []result
	entries                                []entry
	transfers                              []transfer
	shares                                 []share
	selected                               map[int]bool
	err                                    string
}

func (m model) rows() int {
	switch m.workspace {
	case 0:
		return len(m.results)
	case 1:
		return len(m.entries)
	case 2:
		return len(m.transfers)
	case 3:
		return len(m.shares)
	default:
		return len(m.settingFields())
	}
}

func newModel(ctx context.Context, c *ipc.Client, path string, transient bool, cfg config.Config) model {
	return model{ctx: ctx, client: c, configPath: path, cfg: cfg, transient: transient, width: 80, height: 24, selected: map[int]bool{}, setupVals: [5]string{cfg.Soulseek.Username, cfg.Soulseek.Password, cfg.Soulseek.ListenAddr, cfg.DownloadDir, ""}}
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
	placeholders := []string{"Soulseek username", "Required", "0.0.0.0:50300", "~/Downloads/slsk-tui", "music:/home/me/Music"}
	var b strings.Builder
	b.WriteString(styled("SLSK", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#CBA6F7"))))
	b.WriteString("  First-time setup\n")
	b.WriteString(muted("Connect directly to Soulseek. Your password stays in your local config."))
	b.WriteString("\n\n")
	fieldWidth := max(24, min(52, m.width-12))
	for i, label := range labels {
		value := m.setupVals[i]
		if i == 1 {
			value = strings.Repeat("•", utf8.RuneCountInString(value))
		}
		if value == "" {
			value = muted(placeholders[i])
		}
		marker := "  "
		if i == m.setupField {
			marker = styled("› ", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#CBA6F7")))
			value += styled("█", lipgloss.NewStyle().Foreground(lipgloss.Color("#CBA6F7")))
		}
		fmt.Fprintf(&b, "%s%s\n  %s\n", marker, strong(label), trunc(value, fieldWidth))
	}
	if m.setupErr != "" {
		b.WriteString("\n" + danger("! "+m.setupErr))
	}
	b.WriteString("\n\n" + muted("↑↓ / tab move   •   enter next / save   •   esc quit"))

	cardWidth := max(34, min(64, m.width-4))
	card := panelStyle().Width(cardWidth).Padding(1, 2).Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, card)
}

func (m model) mainView() string {
	if m.width < 36 || m.height < 8 {
		return m.compactView()
	}

	names := []string{"Search", "Browse", "Transfers", "Shares", "Settings"}
	left := styled("SLSK", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#CBA6F7"))) + muted("  Soulseek for your terminal")
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
	names := []string{"Search", "Browse", "Transfers", "Shares", "Settings"}
	lines := []string{
		trunc("SLSK  "+string(m.status.status), m.width),
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
		{"← →", "change settings section"},
		{"/ / enter", "edit a query, username, share, or setting"},
		{"space", "select more than one file"},
		{"d", "download, cancel, or remove"},
		{"s", "save settings"},
		{"? / esc", "close this guide"},
		{"q", "quit"},
	}
	for _, row := range rows {
		fmt.Fprintf(&b, "%-20s %s\n", strong(row[0]), row[1])
	}
	b.WriteString("\n" + muted("github.com/catgirl-systems/slsk-tui  •  AGPL-3.0-only  •  no warranty"))
	cardWidth := max(34, min(72, m.width-4))
	card := panelStyle().Width(cardWidth).Padding(1, 2).Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, card)
}

func (m model) renderSearch(width, height int) string {
	query := m.query
	if m.editing {
		query = m.input
	}
	prompt := muted("/  Press / to search the network")
	if query != "" || m.editing {
		prompt = styled("/  "+query, lipgloss.NewStyle().Foreground(lipgloss.Color("#F5E0DC")))
	}
	if m.editing {
		prompt += styled("█", lipgloss.NewStyle().Foreground(lipgloss.Color("#CBA6F7"))) + muted("   enter search  •  esc cancel")
	}
	lines := []string{sectionHeader("SEARCH", countLabel(len(m.results), "result"), width), trunc(prompt, width)}
	limit := max(0, height-len(lines))
	if m.loading && limit > 0 {
		return strings.Join(append(lines, muted("◌  Searching Soulseek…")), "\n")
	}
	if len(m.results) == 0 && limit > 0 {
		return strings.Join(append(lines, "\n"+muted("No results yet. Press / and type an artist, album, or filename.")), "\n")
	}
	start, end := visibleRange(len(m.results), m.cursor, limit)
	for i := start; i < end; i++ {
		x := m.results[i]
		mark := "○"
		if m.selected[i] {
			mark = "●"
		}
		availability := formatBytes(x.size)
		if x.free {
			availability += "  free"
		} else if x.queue > 0 {
			availability += fmt.Sprintf("  queue %d", x.queue)
		}
		if x.speed > 0 {
			availability += "  " + formatBytes(uint64(x.speed)) + "/s"
		}
		kind := "·"
		if x.directory {
			kind = "▸"
		}
		pathWidth := max(4, width-lipgloss.Width(availability)-7)
		row := fmt.Sprintf("%s %s %s  %s", mark, kind, trunc(x.user+"/"+x.path, pathWidth), availability)
		lines = append(lines, selectedRow(row, i == m.cursor))
	}
	return strings.Join(lines, "\n")
}

func (m model) renderBrowse(width, height int) string {
	user := m.browseUser
	if m.editing {
		user = m.input
	}
	prompt := muted("/  Press / to enter a username")
	if user != "" || m.editing {
		prompt = styled("/  "+user, lipgloss.NewStyle().Foreground(lipgloss.Color("#F5E0DC")))
	}
	if m.editing {
		prompt += styled("█", lipgloss.NewStyle().Foreground(lipgloss.Color("#CBA6F7"))) + muted("   enter browse  •  esc cancel")
	}
	lines := []string{sectionHeader("BROWSE", countLabel(len(m.entries), "item"), width), trunc(prompt, width)}
	limit := max(0, height-len(lines))
	if m.loading && limit > 0 {
		return strings.Join(append(lines, muted("◌  Loading public shares…")), "\n")
	}
	if len(m.entries) == 0 && limit > 0 {
		return strings.Join(append(lines, "\n"+muted("Enter a Soulseek username to browse their public files.")), "\n")
	}
	start, end := visibleRange(len(m.entries), m.cursor, limit)
	for i := start; i < end; i++ {
		x := m.entries[i]
		mark := "○"
		if m.selected[i] {
			mark = "●"
		}
		kind := "·"
		if x.directory {
			kind = "▾"
		}
		path := strings.ReplaceAll(x.name, "\\", "/")
		depth := min(6, strings.Count(path, "/"))
		nameWidth := max(4, width-14-depth*2)
		row := fmt.Sprintf("%s %s %s%s  %s", mark, kind, strings.Repeat("  ", depth), trunc(filepath.Base(path), nameWidth), formatBytes(x.size))
		lines = append(lines, selectedRow(row, i == m.cursor))
	}
	return strings.Join(lines, "\n")
}

func (m model) renderTransfers(width, height int) string {
	lines := []string{sectionHeader("TRANSFERS", countLabel(len(m.transfers), "transfer"), width)}
	limit := max(0, height-1)
	if len(m.transfers) == 0 && limit > 0 {
		return strings.Join(append(lines, "\n"+muted("Nothing queued. Download a file from Search or Browse.")), "\n")
	}
	start, end := visibleRange(len(m.transfers), m.cursor, limit)
	for i := start; i < end; i++ {
		x := m.transfers[i]
		progress := x.state
		if x.total > 0 {
			progress += fmt.Sprintf("  %d%%", min(uint64(100), x.done*100/x.total))
		}
		if x.queue > 0 {
			progress += fmt.Sprintf("  queue %d", x.queue)
		}
		direction := "↓"
		if x.direction == "upload" {
			direction = "↑"
		}
		nameWidth := max(4, width-lipgloss.Width(progress)-5)
		row := fmt.Sprintf("%s  %s  %s", direction, trunc(x.filename, nameWidth), progress)
		lines = append(lines, selectedRow(row, i == m.cursor))
	}
	return strings.Join(lines, "\n")
}

func (m model) renderShares(width, height int) string {
	lines := []string{sectionHeader("SHARES", countLabel(len(m.shares), "folder"), width)}
	limit := max(0, height-1)
	if m.editing && limit > 0 {
		lines = append(lines, trunc(styled("/  "+m.input+"█", lipgloss.NewStyle().Foreground(lipgloss.Color("#F5E0DC")))+muted("   name:path  •  enter add  •  esc cancel"), width))
		limit--
	}
	if len(m.shares) == 0 && limit > 0 {
		return strings.Join(append(lines, "\n"+muted("No public folders. Press / to add one as name:path.")), "\n")
	}
	start, end := visibleRange(len(m.shares), m.cursor, limit)
	for i := start; i < end; i++ {
		x := m.shares[i]
		nameWidth := max(4, min(20, width/3))
		row := fmt.Sprintf("◆  %-*s  %s", nameWidth, trunc(x.name, nameWidth), trunc(x.path, max(4, width-nameWidth-5)))
		lines = append(lines, selectedRow(row, i == m.cursor))
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
			value = m.input
		}
		if field.secret && value != "" {
			value = strings.Repeat("•", utf8.RuneCountInString(value))
		}
		if value == "" {
			value = muted("Not set")
		}
		if m.editing && i == m.cursor {
			value += styled("█", lipgloss.NewStyle().Foreground(lipgloss.Color("#CBA6F7")))
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
		hints = append(hints, "/ search", "space select", "d download")
	case 1:
		hints = append(hints, "/ user", "space select", "d download")
	case 2:
		hints = append(hints, "d cancel", "r retry", "c clear")
	case 3:
		hints = append(hints, "/ add", "d remove", "r rescan")
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

func trunc(s string, n int) string {
	if n < 4 {
		return ""
	}
	return ansi.Truncate(s, n, "…")
}

func popRune(s string) string {
	_, size := utf8.DecodeLastRuneInString(s)
	if size == 0 {
		return s
	}
	return s[:len(s)-size]
}

func (m *model) setupKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if s == "esc" {
		return tea.Quit
	}
	if s == "tab" || s == "down" {
		m.setupField = (m.setupField + 1) % len(m.setupVals)
		return nil
	}
	if s == "shift+tab" || s == "up" {
		m.setupField = (m.setupField + len(m.setupVals) - 1) % len(m.setupVals)
		return nil
	}
	if s == "enter" {
		if m.setupField < 4 {
			m.setupField++
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
	if s == "backspace" {
		m.setupVals[m.setupField] = popRune(m.setupVals[m.setupField])
		return nil
	}
	if t := k.Key().Text; t != "" {
		m.setupVals[m.setupField] += t
	}
	return nil
}

func toResults(x []daemon.SearchResult) []result {
	r := make([]result, len(x))
	for i, v := range x {
		r[i] = result{user: v.Username, path: v.Path, size: v.Size, directory: v.Directory, free: v.SlotFree, speed: v.Speed, queue: v.Queue}
	}
	return r
}
func toEntries(x []soulseek.ShareEntry) []entry {
	r := make([]entry, len(x))
	for i, v := range x {
		r[i] = entry{v.Name, v.Size, v.Directory}
	}
	return r
}
func toTransfers(x []daemon.Transfer) []transfer {
	r := make([]transfer, len(x))
	for i, v := range x {
		r[i] = transfer{id: v.ID, user: v.Username, filename: v.Filename, direction: v.Direction, state: v.State, done: v.Done, total: v.Total, queue: v.Queue}
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
