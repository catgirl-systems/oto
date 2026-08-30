package tui

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/ipc"
)

// Run loads config, presents setup when needed, then starts the terminal UI.
func Run(ctx context.Context, client *ipc.Client, configPath string) error {
	return RunWithTransient(ctx, client, configPath, false)
}

// RunSetup writes the initial config before a daemon exists.
func RunSetup(ctx context.Context, configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("load config: %w", err)
	}
	m := newModel(ctx, nil, configPath, false, cfg)
	m.setup = true
	_, err = tea.NewProgram(m).Run()
	return err
}

func RunWithTransient(ctx context.Context, client *ipc.Client, configPath string, transient bool) error {
	if client == nil {
		return errors.New("tui: nil IPC client")
	}
	cfg, err := config.Load(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("load config: %w", err)
	}
	m := newModel(ctx, client, configPath, transient, cfg)
	if strings.TrimSpace(cfg.Soulseek.Username) == "" || strings.TrimSpace(cfg.Soulseek.Password) == "" {
		m.setup = true
	}
	_, err = tea.NewProgram(m).Run()
	return err
}

type tickMsg time.Time
type statusMsg struct {
	snapshot daemon.Snapshot
	err      error
}
type searchMsg struct {
	page         daemon.SearchPage
	append       bool
	filter       string
	filterChange bool
	err          error
}
type browseMsg struct {
	entries []entry
	err     error
}
type transferMsg struct {
	transfers []transfer
	err       error
}
type sharesMsg struct {
	shares []share
	err    error
}
type settingsMsg struct{ err error }
type transferActionMsg struct{ err error }

func tick() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }
func (m model) loadStatus() tea.Cmd {
	return func() tea.Msg { s, e := m.client.Status(m.ctx); return statusMsg{s, e} }
}
func (m model) loadTransfers() tea.Cmd {
	return func() tea.Msg { x, e := m.client.Transfers(m.ctx); return transferMsg{toTransfers(x), e} }
}
func (m model) loadShares() tea.Cmd {
	return func() tea.Msg { x, e := m.client.Shares(m.ctx); return sharesMsg{toShares(x), e} }
}
func (m model) rescanShares() tea.Cmd {
	return func() tea.Msg { x, e := m.client.Rescan(m.ctx); return sharesMsg{toShares(x), e} }
}
func (m model) search() tea.Cmd {
	q, filter := m.query, m.searchFilter
	return func() tea.Msg { page, err := m.client.Search(m.ctx, q, filter); return searchMsg{page: page, err: err} }
}
func (m model) loadSearchPage() tea.Cmd {
	id, cursor, filter := m.searchID, m.searchNext, m.searchFilter
	return func() tea.Msg {
		page, err := m.client.SearchPage(m.ctx, id, cursor, filter)
		return searchMsg{page: page, append: true, err: err}
	}
}
func (m model) filterSearch(filter string) tea.Cmd {
	id := m.searchID
	return func() tea.Msg {
		page, err := m.client.SearchPage(m.ctx, id, 0, filter)
		return searchMsg{page: page, filter: filter, filterChange: true, err: err}
	}
}
func (m model) browse() tea.Cmd {
	u := m.browseUser
	return func() tea.Msg { x, e := m.client.Browse(m.ctx, u); return browseMsg{toEntries(x), e} }
}

func (m model) saveSettings() tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg {
		_, err := m.client.UpdateConfig(m.ctx, cfg)
		return settingsMsg{err}
	}
}

func (m model) Init() tea.Cmd {
	if m.setup {
		return nil
	}
	return tea.Batch(m.loadStatus(), m.loadTransfers(), m.loadShares(), tick())
}
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch x := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = x.Width, x.Height
	case tickMsg:
		return m, tea.Batch(m.loadStatus(), m.loadTransfers(), m.loadShares(), tick())
	case statusMsg:
		if x.err != nil {
			m.status.err = x.err.Error()
		} else {
			m.status = snapshot{status: x.snapshot.Status, user: x.snapshot.Config.Soulseek.Username, err: x.snapshot.Error}
		}
	case searchMsg:
		m.err = errText(x.err)
		if x.append {
			m.loadingMore = false
		} else {
			m.loading = false
		}
		if x.err != nil {
			break
		}
		results := toResults(x.page.Results)
		if x.append {
			oldLen := len(m.results)
			m.results = append(m.results, results...)
			if m.cursor == oldLen-1 && len(m.results) > oldLen {
				m.cursor++
			}
		} else {
			m.results, m.cursor, m.selected = results, 0, map[int]bool{}
		}
		if x.filterChange {
			if x.filter == "" && m.searchFilter != "" {
				m.searchFilterUndo = m.searchFilter
			} else if m.searchFilter == "" && x.filter == m.searchFilterUndo {
				m.searchFilterUndo = ""
			}
			m.searchFilter = x.filter
		}
		m.searchID, m.searchTotal, m.searchFound, m.searchNext = x.page.ID, x.page.Total, x.page.FoundTotal, x.page.NextCursor
	case browseMsg:
		m.entries, m.err = x.entries, errText(x.err)
		m.cursor, m.selected = 0, map[int]bool{}
		m.loading = false
	case transferMsg:
		m.transfers, m.err = x.transfers, errText(x.err)
	case sharesMsg:
		m.shares, m.err = x.shares, errText(x.err)
	case settingsMsg:
		m.err = errText(x.err)
		if x.err == nil {
			return m, m.loadStatus()
		}
	case transferActionMsg:
		m.err = errText(x.err)
		if x.err == nil {
			return m, m.loadTransfers()
		}
	case tea.PasteMsg:
		text := strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, x.Content)
		if m.setup {
			m.setupVals[m.setupField] += text
		} else if m.editing {
			m.input += text
		}
		return m, nil
	case tea.KeyPressMsg:
		return m, m.key(x)
	}
	return m, nil
}
func errText(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}

func (m *model) key(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if m.help {
		if s == "?" || s == "esc" {
			m.help = false
		}
		return nil
	}
	if m.setup {
		return m.setupKey(k)
	}
	if m.confirm {
		if s == "y" || s == "enter" {
			return tea.Quit
		}
		if s == "n" || s == "esc" {
			m.confirm = false
		}
		return nil
	}
	if m.editing {
		return m.editKey(k)
	}
	switch s {
	case "q", "ctrl+c":
		if m.transient && m.active() {
			m.confirm = true
			return nil
		}
		return tea.Quit
	case "?":
		m.help = true
	case "tab":
		m.workspace = (m.workspace + 1) % 5
		m.cursor, m.selected = 0, map[int]bool{}
	case "shift+tab":
		m.workspace = (m.workspace + 4) % 5
		m.cursor, m.selected = 0, map[int]bool{}
	case "right":
		if m.workspace == 4 {
			m.settingsSection = (m.settingsSection + 1) % 3
		} else {
			m.workspace = (m.workspace + 1) % 5
		}
		m.cursor, m.selected = 0, map[int]bool{}
	case "left":
		if m.workspace == 4 {
			m.settingsSection = (m.settingsSection + 2) % 3
		} else {
			m.workspace = (m.workspace + 4) % 5
		}
		m.cursor, m.selected = 0, map[int]bool{}
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor+1 < m.rows() {
			m.cursor++
		} else if m.workspace == 0 && !m.loadingMore && m.searchNext > 0 {
			m.loadingMore = true
			return m.loadSearchPage()
		}
	case "space":
		m.toggle()
	case "enter":
		if m.workspace == 4 {
			m.beginEdit()
			return nil
		}
		return m.enter()
	case "d":
		if m.workspace == 0 {
			return m.queueResult()
		}
		if m.workspace == 1 {
			return m.queueBrowse()
		}
		if m.workspace == 2 {
			return m.action("cancel")
		}
		if m.workspace == 3 {
			return m.removeShare()
		}
	case "r":
		if m.workspace == 2 {
			return m.action("retry")
		}
		if m.workspace == 3 {
			return m.rescanShares()
		}
	case "c":
		if m.workspace == 0 {
			filter := m.searchFilterUndo
			if m.searchFilter != "" {
				filter = ""
			}
			if m.searchID == "" {
				m.searchFilter, m.searchFilterUndo = filter, m.searchFilter
				return nil
			}
			m.loading = true
			return m.filterSearch(filter)
		}
		if m.workspace == 2 {
			return m.action("clear")
		}
	case "s":
		if m.workspace == 4 {
			return m.saveSettings()
		}
	case "f":
		if m.workspace == 0 {
			m.editing, m.filterEditing, m.input = true, true, m.searchFilter
			return nil
		}
	case "/":
		m.beginEdit()
		return nil
	}
	return nil
}

func (m *model) beginEdit() {
	m.editing, m.filterEditing = true, false
	switch m.workspace {
	case 0:
		m.input = m.query
	case 1:
		m.input = m.browseUser
	case 4:
		fields := m.settingFields()
		if m.cursor < len(fields) {
			m.input = fields[m.cursor].value
		}
	default:
		m.input = ""
	}
}
func (m *model) editKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if m.filterEditing && (s == "tab" || s == "shift+tab") {
		m.input = completeFilter(m.input, s == "shift+tab")
		return nil
	}
	if s == "esc" {
		m.editing, m.filterEditing = false, false
		m.input = ""
		return nil
	}
	if s == "enter" {
		filterEditing := m.filterEditing
		m.editing, m.filterEditing = false, false
		if filterEditing {
			filter := strings.TrimSpace(m.input)
			if err := daemon.ValidateSearchFilter(filter); err != nil {
				m.err = err.Error()
				return nil
			}
			if m.searchID == "" {
				m.searchFilter = filter
				m.err = ""
				return nil
			}
			m.loading = true
			return m.filterSearch(filter)
		}
		if m.workspace == 0 {
			m.query = strings.TrimSpace(m.input)
			if m.query == "" {
				return nil
			}
			m.loading, m.loadingMore = true, false
			return m.search()
		}
		if m.workspace == 1 {
			m.browseUser = strings.TrimSpace(m.input)
			if m.browseUser == "" {
				return nil
			}
			m.loading = true
			return m.browse()
		}
		if m.workspace == 3 {
			return m.addShare()
		}
		if m.workspace == 4 {
			value := m.input
			if m.settingsSection != 0 || m.cursor != 1 {
				value = strings.TrimSpace(value)
			}
			m.setSettingValue(value)
			return nil
		}
		return nil
	}
	if s == "backspace" {
		m.input = popRune(m.input)
		return nil
	}
	if t := k.Key().Text; t != "" {
		m.input += t
	}
	return nil
}

var filterFields = []string{"in:", "out:", "type:", "size:", "bitrate:", "duration:", "free:", "public:"}
var filterTypes = []string{"audio", "video", "image", "document", "text", "archive", "executable"}
var filterComparisons = []string{">=", "<=", "=", "==", "!=", ">", "<"}

func completeFilter(input string, reverse bool) string {
	start := strings.LastIndexAny(input, " \t\n") + 1
	token := input[start:]
	field, value, hasValue := strings.Cut(token, ":")
	if !hasValue {
		return input[:start] + cycleCompletion(token, filterFields, reverse)
	}
	var options []string
	switch field {
	case "type":
		valueStart := strings.LastIndex(value, ",") + 1
		prefix := value[valueStart:]
		negated := strings.HasPrefix(prefix, "!")
		prefix = strings.TrimPrefix(prefix, "!")
		completed := cycleCompletion(prefix, filterTypes, reverse)
		if negated {
			completed = "!" + completed
		}
		return input[:start] + field + ":" + value[:valueStart] + completed
	case "free", "public":
		options = []string{"true", "false"}
	case "size", "bitrate", "duration":
		options = filterComparisons
	case "in", "out":
		if value == "" {
			return input[:start] + cycleCompletion(token, filterFields, reverse)
		}
		return input
	default:
		return input
	}
	return input[:start] + field + ":" + cycleCompletion(value, options, reverse)
}

func cycleCompletion(prefix string, options []string, reverse bool) string {
	for i, option := range options {
		if option == prefix {
			if reverse {
				return options[(i+len(options)-1)%len(options)]
			}
			return options[(i+1)%len(options)]
		}
	}
	if reverse {
		for i := len(options) - 1; i >= 0; i-- {
			if strings.HasPrefix(options[i], prefix) {
				return options[i]
			}
		}
	} else {
		for _, option := range options {
			if strings.HasPrefix(option, prefix) {
				return option
			}
		}
	}
	return prefix
}

func filterCompletionHint(input string) string {
	start := strings.LastIndexAny(input, " \t\n") + 1
	field, _, ok := strings.Cut(input[start:], ":")
	if !ok {
		return "tab complete: " + strings.Join(filterFields, "  ")
	}
	switch field {
	case "type":
		return "tab complete: " + strings.Join(filterTypes, "  ")
	case "free", "public":
		return "tab complete: true  false"
	case "size", "bitrate", "duration":
		return "tab complete: " + strings.Join(filterComparisons, "  ")
	case "in", "out":
		return "value: case-insensitive regular expression"
	default:
		return "tab complete: " + strings.Join(filterFields, "  ")
	}
}
func (m *model) enter() tea.Cmd {
	if m.workspace == 0 && len(m.results) > 0 {
		m.selected[m.cursor] = true
		return m.queueResult()
	}
	if m.workspace == 1 && len(m.entries) > 0 {
		m.selected[m.cursor] = true
		return m.queueBrowse()
	}
	return nil
}
func (m *model) toggle() {
	if m.workspace == 0 && m.cursor < len(m.results) {
		m.selected[m.cursor] = !m.selected[m.cursor]
	}
	if m.workspace == 1 && m.cursor < len(m.entries) {
		m.selected[m.cursor] = !m.selected[m.cursor]
	}
}
func (m *model) queueResult() tea.Cmd {
	byUser := make(map[string][]daemon.DownloadItem)
	for i, x := range m.results {
		if m.selected[i] && !x.directory {
			byUser[x.user] = append(byUser[x.user], daemon.DownloadItem{Filename: x.path, Size: x.size})
		}
	}
	if len(byUser) == 0 && m.cursor < len(m.results) && !m.results[m.cursor].directory {
		x := m.results[m.cursor]
		byUser[x.user] = []daemon.DownloadItem{{Filename: x.path, Size: x.size}}
	}
	if len(byUser) == 0 {
		return nil
	}
	requests := make([]daemon.DownloadRequest, 0, len(byUser))
	for user, files := range byUser {
		requests = append(requests, daemon.DownloadRequest{Username: user, Files: files})
	}
	return func() tea.Msg {
		_, err := m.client.QueueDownloads(m.ctx, requests)
		return statusMsg{err: err}
	}
}
func (m *model) queueBrowse() tea.Cmd {
	chosen := make(map[string]download)
	add := func(entry entry) {
		if !entry.directory {
			chosen[entry.name] = download{entry.name, entry.size}
			return
		}
		prefix := strings.TrimRight(entry.name, "/\\") + "\\"
		for _, child := range m.entries {
			if !child.directory && strings.HasPrefix(strings.ReplaceAll(child.name, "/", "\\"), prefix) {
				chosen[child.name] = download{child.name, child.size}
			}
		}
	}
	for i, item := range m.entries {
		if m.selected[i] {
			add(item)
		}
	}
	if len(chosen) == 0 && m.cursor < len(m.entries) {
		add(m.entries[m.cursor])
	}
	if len(chosen) == 0 {
		return nil
	}
	files := make([]download, 0, len(chosen))
	for _, file := range chosen {
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].filename < files[j].filename })
	return m.queue(files, m.browseUser)
}
func (m *model) addShare() tea.Cmd {
	p := strings.SplitN(strings.TrimSpace(m.input), ":", 2)
	if len(p) != 2 || strings.TrimSpace(p[0]) == "" || strings.TrimSpace(p[1]) == "" {
		m.err = "share format is name:path"
		return nil
	}
	return func() tea.Msg {
		x, e := m.client.AddShare(m.ctx, config.Share{Name: strings.TrimSpace(p[0]), Path: strings.TrimSpace(p[1])})
		return sharesMsg{shares: toShares(x), err: e}
	}
}
func (m *model) removeShare() tea.Cmd {
	if m.cursor >= len(m.shares) {
		return nil
	}
	name := m.shares[m.cursor].name
	return func() tea.Msg {
		var out []config.Share
		e := m.client.Do(m.ctx, "DELETE", "/v1/shares/"+url.PathEscape(name), nil, &out)
		return sharesMsg{shares: toShares(out), err: e}
	}
}

func (m *model) queue(files []download, user string) tea.Cmd {
	return func() tea.Msg {
		items := make([]daemon.DownloadItem, len(files))
		for i, f := range files {
			items[i] = daemon.DownloadItem{Filename: f.filename, Size: f.size}
		}
		_, e := m.client.QueueDownloads(m.ctx, []daemon.DownloadRequest{{Username: user, Files: items}})
		return statusMsg{err: e}
	}
}
func (m model) action(action string) tea.Cmd {
	if m.cursor >= len(m.transfers) {
		return nil
	}
	id := m.transfers[m.cursor].id
	return func() tea.Msg { return transferActionMsg{m.client.TransferAction(m.ctx, id, action)} }
}
func (m model) active() bool {
	for _, x := range m.transfers {
		if x.state == "queued" || x.state == "incomplete" || x.state == "running" {
			return true
		}
	}
	return false
}
