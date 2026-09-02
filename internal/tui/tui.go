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
type spinnerTickMsg time.Time
type statusMsg struct {
	snapshot daemon.Snapshot
	err      error
}
type searchMsg struct {
	page         daemon.SearchPage
	request      uint64
	operation    uint64
	append       bool
	filter       string
	filterChange bool
	err          error
}
type browseMsg struct {
	user    string
	request uint64
	entries []entry
	err     error
}
type folderDownloadMsg struct{ err error }
type transferMsg struct {
	transfers []transfer
	at        time.Time
	err       error
}
type sharesMsg struct {
	shares []share
	err    error
	reset  bool
}
type shareBrowseMsg struct {
	nodeID              string
	generation, request uint64
	entries             []entry
	err                 error
}
type settingsMsg struct{ err error }
type transferActionMsg struct {
	transfers []transfer
	err       error
}

func tick() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }
func spinnerTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg { return spinnerTickMsg(t) })
}
func (m model) loadStatus() tea.Cmd {
	return func() tea.Msg { s, e := m.client.Status(m.ctx); return statusMsg{s, e} }
}
func (m model) loadTransfers() tea.Cmd {
	return func() tea.Msg {
		x, err := m.client.Transfers(m.ctx)
		return transferMsg{transfers: toTransfers(x), at: time.Now(), err: err}
	}
}

func setTransferSpeeds(next, previous []transfer, elapsed time.Duration) {
	if elapsed <= 0 {
		return
	}
	old := make(map[string]uint64, len(previous))
	for _, transfer := range previous {
		old[transfer.id] = transfer.done
	}
	for i := range next {
		done, ok := old[next[i].id]
		if ok && next[i].state == "running" && next[i].done > done {
			next[i].speed = uint64(float64(next[i].done-done) / elapsed.Seconds())
		}
	}
}
func (m model) loadShares() tea.Cmd {
	return func() tea.Msg { x, e := m.client.Shares(m.ctx); return sharesMsg{shares: toShares(x), err: e} }
}
func (m *model) rescanShares() tea.Cmd {
	m.shareGeneration++
	for i := range m.shareTree.nodes {
		m.shareTree.nodes[i].loading = false
	}
	return func() tea.Msg {
		x, e := m.client.Rescan(m.ctx)
		return sharesMsg{shares: toShares(x), reset: e == nil, err: e}
	}
}

func (m model) browseShare(nodeID, path string, generation, request uint64) tea.Cmd {
	return func() tea.Msg {
		entries, err := m.client.BrowseShares(m.ctx, path)
		return shareBrowseMsg{nodeID: nodeID, generation: generation, request: request, entries: toEntries(entries), err: err}
	}
}
func (m model) search(query, filter string, request, operation uint64) tea.Cmd {
	return func() tea.Msg {
		page, err := m.client.Search(m.ctx, query, filter)
		return searchMsg{page: page, request: request, operation: operation, filter: filter, err: err}
	}
}
func (m model) loadSearchPage() tea.Cmd {
	id, cursor, filter := m.searchID, m.searchNext, m.searchFilter
	request, operation := uint64(0), uint64(0)
	if m.searchTabIndex >= 0 && m.searchTabIndex < len(m.searchTabs) {
		request, operation = m.searchTabs[m.searchTabIndex].request, m.searchTabs[m.searchTabIndex].operation
	}
	return func() tea.Msg {
		page, err := m.client.SearchPage(m.ctx, id, cursor, filter)
		return searchMsg{page: page, request: request, operation: operation, append: true, err: err}
	}
}
func (m *model) filterSearch(filter string) tea.Cmd {
	id := m.searchID
	request, operation := uint64(0), uint64(0)
	m.loading, m.loadingMore = true, false
	if m.searchTabIndex >= 0 && m.searchTabIndex < len(m.searchTabs) {
		m.searchOperation++
		tab := &m.searchTabs[m.searchTabIndex]
		tab.operation, tab.loading, tab.loadingMore = m.searchOperation, true, false
		request, operation = tab.request, tab.operation
	}
	return func() tea.Msg {
		page, err := m.client.SearchPage(m.ctx, id, 0, filter)
		return searchMsg{page: page, request: request, operation: operation, filter: filter, filterChange: true, err: err}
	}
}

func (m *model) refilterPendingSearch(tab *searchTab, page daemon.SearchPage) tea.Cmd {
	m.searchOperation++
	tab.id, tab.operation = page.ID, m.searchOperation
	tab.results, tab.total, tab.found, tab.next = nil, 0, page.FoundTotal, 0
	tab.cursor, tab.selected, tab.tree = 0, map[int]bool{}, treeState{}
	tab.loading, tab.loadingMore, tab.err = true, false, ""
	id, filter, request, operation := tab.id, tab.filter, tab.request, tab.operation
	return func() tea.Msg {
		filtered, err := m.client.SearchPage(m.ctx, id, 0, filter)
		return searchMsg{page: filtered, request: request, operation: operation, filter: filter, filterChange: true, err: err}
	}
}

func (m *model) saveSearchTab() {
	if m.searchTabIndex < 0 || m.searchTabIndex >= len(m.searchTabs) {
		return
	}
	tab := &m.searchTabs[m.searchTabIndex]
	tab.query, tab.id, tab.filter, tab.filterUndo, tab.err = m.query, m.searchID, m.searchFilter, m.searchFilterUndo, m.err
	tab.results, tab.total, tab.found, tab.next = m.results, m.searchTotal, m.searchFound, m.searchNext
	tab.cursor, tab.selected, tab.loading, tab.loadingMore = m.cursor, m.selected, m.loading, m.loadingMore
	tab.tree = m.searchTree
}

func (m *model) loadSearchTab(index int) {
	if index < 0 || index >= len(m.searchTabs) {
		m.query, m.searchID, m.searchFilter, m.searchFilterUndo, m.err = "", "", "", "", ""
		m.results, m.searchTotal, m.searchFound, m.searchNext = nil, 0, 0, 0
		m.cursor, m.selected, m.loading, m.loadingMore = 0, map[int]bool{}, false, false
		m.searchTree = treeState{}
		return
	}
	m.searchTabIndex = index
	tab := &m.searchTabs[index]
	if tab.selected == nil {
		tab.selected = map[int]bool{}
	}
	m.query, m.searchID, m.searchFilter, m.searchFilterUndo, m.err = tab.query, tab.id, tab.filter, tab.filterUndo, tab.err
	m.results, m.searchTotal, m.searchFound, m.searchNext = tab.results, tab.total, tab.found, tab.next
	m.cursor, m.selected, m.loading, m.loadingMore = tab.cursor, tab.selected, tab.loading, tab.loadingMore
	m.searchTree = tab.tree
}

func (m *model) switchSearchTab(delta int) {
	if len(m.searchTabs) < 2 {
		return
	}
	m.saveSearchTab()
	m.loadSearchTab((m.searchTabIndex + delta + len(m.searchTabs)) % len(m.searchTabs))
}

func (m *model) closeSearchTab() {
	if len(m.searchTabs) == 0 {
		return
	}
	m.saveSearchTab()
	m.searchTabs = append(m.searchTabs[:m.searchTabIndex], m.searchTabs[m.searchTabIndex+1:]...)
	if len(m.searchTabs) == 0 {
		m.loadSearchTab(-1)
		return
	}
	m.loadSearchTab(min(m.searchTabIndex, len(m.searchTabs)-1))
}

func (m *model) openSearch(query string) tea.Cmd {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	if m.workspace == 0 {
		m.saveSearchTab()
	}
	m.searchRequest++
	m.searchOperation++
	tab := searchTab{query: query, filter: m.searchFilter, selected: map[int]bool{}, loading: true, request: m.searchRequest, operation: m.searchOperation}
	m.searchTabs = append(m.searchTabs, tab)
	m.workspace = 0
	m.loadSearchTab(len(m.searchTabs) - 1)
	return m.search(tab.query, tab.filter, tab.request, tab.operation)
}

func applySearchMsg(tab *searchTab, message searchMsg) {
	tab.err = errText(message.err)
	if message.append {
		tab.loadingMore = false
	} else {
		tab.loading = false
	}
	if message.err != nil {
		return
	}
	results := toResults(message.page.Results)
	if message.append {
		tab.results = append(tab.results, results...)
	} else {
		tab.results, tab.selected = results, map[int]bool{}
	}
	tab.tree, tab.cursor = buildSearchTree(tab.results, tab.tree, tab.cursor)
	if message.filterChange {
		if message.filter == "" && tab.filter != "" {
			tab.filterUndo = tab.filter
		} else if tab.filter == "" && message.filter == tab.filterUndo {
			tab.filterUndo = ""
		}
		tab.filter = message.filter
	}
	tab.id, tab.total, tab.found, tab.next = message.page.ID, message.page.Total, message.page.FoundTotal, message.page.NextCursor
}
func (m model) browse(user string, request uint64) tea.Cmd {
	return func() tea.Msg {
		x, err := m.client.Browse(m.ctx, user)
		return browseMsg{user: user, request: request, entries: toEntries(x), err: err}
	}
}

func normalizeBrowsePath(path string) string {
	return strings.Trim(strings.ReplaceAll(path, "/", "\\"), "\\")
}

func browseTargetCursor(entries []entry, target string) int {
	target = normalizeBrowsePath(target)
	if target == "" {
		return 0
	}
	for i, item := range entries {
		if item.directory && strings.EqualFold(normalizeBrowsePath(item.name), target) {
			return i
		}
	}
	prefix := strings.ToLower(target + "\\")
	for i, item := range entries {
		if strings.HasPrefix(strings.ToLower(normalizeBrowsePath(item.name)), prefix) {
			return i
		}
	}
	return 0
}

func (m *model) saveBrowseTab() {
	if m.browseTabIndex < 0 || m.browseTabIndex >= len(m.browseTabs) {
		return
	}
	tab := &m.browseTabs[m.browseTabIndex]
	tab.entries, tab.cursor, tab.selected, tab.loading, tab.tree = m.entries, m.cursor, m.selected, m.loading, m.browseTree
}

func (m *model) loadBrowseTab(index int) {
	if index < 0 || index >= len(m.browseTabs) {
		m.browseUser, m.entries, m.cursor, m.loading = "", nil, 0, false
		m.selected, m.err = map[int]bool{}, ""
		m.browseTree = treeState{}
		return
	}
	m.browseTabIndex = index
	tab := &m.browseTabs[index]
	if tab.selected == nil {
		tab.selected = map[int]bool{}
	}
	m.browseUser, m.entries, m.cursor, m.selected, m.loading, m.browseTree = tab.user, tab.entries, tab.cursor, tab.selected, tab.loading, tab.tree
	m.err = tab.err
}

func (m *model) switchWorkspace(workspace int) {
	if m.workspace == 0 {
		m.saveSearchTab()
	} else if m.workspace == 1 {
		m.saveBrowseTab()
	} else if m.workspace == 2 {
		m.transferCursors[m.transferTab] = m.cursor
	} else if m.workspace == 3 {
		m.shareCursor = m.cursor
	}
	m.workspace = (workspace + 5) % 5
	if m.workspace == 0 {
		if len(m.searchTabs) > 0 {
			m.loadSearchTab(m.searchTabIndex)
		} else {
			m.cursor, m.selected, m.loading = 0, map[int]bool{}, false
		}
		return
	}
	if m.workspace == 1 {
		m.loadBrowseTab(m.browseTabIndex)
		return
	}
	if m.workspace == 2 {
		m.cursor = max(0, min(m.transferCursors[m.transferTab], len(m.transferTrees[m.transferTab].visible)-1))
		m.selected, m.loading = map[int]bool{}, false
		return
	}
	if m.workspace == 3 {
		m.cursor = max(0, min(m.shareCursor, len(m.shareTree.visible)-1))
		m.selected, m.loading = map[int]bool{}, false
		return
	}
	m.cursor, m.selected = 0, map[int]bool{}
	m.loading = false
}

func (m *model) switchBrowseTab(delta int) {
	if len(m.browseTabs) < 2 {
		return
	}
	m.saveBrowseTab()
	m.loadBrowseTab((m.browseTabIndex + delta + len(m.browseTabs)) % len(m.browseTabs))
}

func (m *model) switchTransferTab(tab int) {
	m.transferCursors[m.transferTab] = m.cursor
	m.transferTab = (tab + 2) % 2
	m.cursor = max(0, min(m.transferCursors[m.transferTab], len(m.transferTrees[m.transferTab].visible)-1))
	m.selected = map[int]bool{}
}

func (m *model) openTreeNode(toggle bool) tea.Cmd {
	tree := m.currentTree()
	if tree == nil {
		return nil
	}
	_, node := tree.node(m.cursor)
	if node == nil || node.kind == treeFile {
		return nil
	}
	if m.workspace == 3 && !node.loaded && !node.loading {
		m.shareRequest++
		node.loading, node.request = true, m.shareRequest
		tree.expanded[node.id] = true
		tree.rebuildVisible()
		return m.browseShare(node.id, node.path, m.shareGeneration, node.request)
	}
	if toggle {
		m.cursor = tree.toggle(m.cursor)
	} else {
		m.cursor = tree.right(m.cursor)
	}
	return nil
}

func (m *model) closeBrowseTab() {
	if len(m.browseTabs) == 0 {
		return
	}
	m.saveBrowseTab()
	m.browseTabs = append(m.browseTabs[:m.browseTabIndex], m.browseTabs[m.browseTabIndex+1:]...)
	if len(m.browseTabs) == 0 {
		m.loadBrowseTab(-1)
		return
	}
	m.loadBrowseTab(min(m.browseTabIndex, len(m.browseTabs)-1))
}

func (m *model) openBrowse(user, target string, refresh bool) tea.Cmd {
	user = strings.TrimSpace(user)
	if user == "" {
		return nil
	}
	if m.workspace == 1 {
		m.saveBrowseTab()
	}
	index := -1
	for i := range m.browseTabs {
		if strings.EqualFold(m.browseTabs[i].user, user) {
			index = i
			break
		}
	}
	if index < 0 {
		m.browseTabs = append(m.browseTabs, browseTab{user: user, selected: map[int]bool{}})
		index = len(m.browseTabs) - 1
	}
	tab := &m.browseTabs[index]
	tab.target = normalizeBrowsePath(target)
	request := refresh || (len(tab.entries) == 0 && !tab.loading)
	if request {
		m.browseRequest++
		tab.request, tab.loading = m.browseRequest, true
	} else if tab.target != "" {
		tab.cursor = browseTargetCursor(tab.entries, tab.target)
		tab.target = ""
	}
	m.workspace = 1
	m.loadBrowseTab(index)
	if request {
		return m.browse(tab.user, tab.request)
	}
	return nil
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
	return tea.Batch(m.loadStatus(), m.loadTransfers(), m.loadShares(), tick(), spinnerTick())
}
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch x := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = x.Width, x.Height
	case tickMsg:
		return m, tea.Batch(m.loadStatus(), m.loadTransfers(), m.loadShares(), tick())
	case spinnerTickMsg:
		m.spinner++
		return m, spinnerTick()
	case statusMsg:
		if x.err != nil {
			m.status.err = x.err.Error()
		} else {
			m.status = snapshot{status: x.snapshot.Status, user: x.snapshot.Config.Soulseek.Username, err: x.snapshot.Error}
		}
	case searchMsg:
		if x.request != 0 {
			if m.workspace == 0 {
				m.saveSearchTab()
			}
			for i := range m.searchTabs {
				tab := &m.searchTabs[i]
				if tab.request != x.request || tab.operation != x.operation {
					continue
				}
				if !x.append && !x.filterChange && x.err == nil && tab.filter != x.filter {
					cmd := m.refilterPendingSearch(tab, x.page)
					if m.workspace == 0 && i == m.searchTabIndex {
						m.loadSearchTab(i)
					}
					return m, cmd
				}
				applySearchMsg(tab, x)
				if m.workspace == 0 && i == m.searchTabIndex {
					m.loadSearchTab(i)
				}
				break
			}
			break
		}
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
		m.searchTree, m.cursor = buildSearchTree(m.results, m.searchTree, m.cursor)
	case browseMsg:
		user := x.user
		if user == "" {
			user = m.browseUser
		}
		for i := range m.browseTabs {
			tab := &m.browseTabs[i]
			if !strings.EqualFold(tab.user, user) || tab.request != x.request {
				continue
			}
			tab.loading, tab.err = false, errText(x.err)
			if x.err == nil {
				target := browseTargetCursor(x.entries, tab.target)
				tab.entries, tab.selected = x.entries, map[int]bool{}
				tab.tree, tab.cursor = buildBrowseTree(tab.entries, tab.tree, tab.cursor)
				if tab.target != "" {
					tab.cursor = tab.tree.cursorForSource(target)
				}
			}
			tab.target = ""
			if m.workspace == 1 && i == m.browseTabIndex {
				m.loadBrowseTab(i)
			}
			break
		}
	case folderDownloadMsg:
		m.err = errText(x.err)
	case transferMsg:
		if m.workspace == 2 {
			m.transferCursors[m.transferTab] = m.cursor
		}
		if x.err == nil {
			setTransferSpeeds(x.transfers, m.transfers, x.at.Sub(m.transferSampleAt))
			m.transferSampleAt = x.at
		}
		m.transfers, m.err = x.transfers, errText(x.err)
		for tab, direction := range []string{"download", "upload"} {
			m.transferTrees[tab], m.transferCursors[tab] = buildTransferTree(m.transfers, direction, m.transferTrees[tab], m.transferCursors[tab])
		}
		if m.workspace == 2 {
			m.cursor = m.transferCursors[m.transferTab]
		}
	case sharesMsg:
		m.err = errText(x.err)
		if x.err != nil {
			break
		}
		m.shares = x.shares
		changed := len(m.shareTree.nodes) == 0 || !sameShareRoots(m.shares, m.shareTree)
		if x.reset || changed {
			m.shareGeneration++
			cursor := m.shareCursor
			if m.workspace == 3 {
				cursor = m.cursor
			}
			m.shareTree, m.shareCursor = buildShareRoots(m.shares, m.shareTree, cursor, x.reset)
			if m.workspace == 3 {
				m.cursor = m.shareCursor
			}
		}
	case shareBrowseMsg:
		if x.generation != m.shareGeneration {
			break
		}
		index, ok := m.shareTree.byID[x.nodeID]
		if !ok || m.shareTree.nodes[index].request != x.request {
			break
		}
		m.shareTree.nodes[index].loading = false
		m.err = errText(x.err)
		cursor := m.shareCursor
		if m.workspace == 3 {
			cursor = m.cursor
		}
		if x.err != nil {
			m.shareTree.expanded[x.nodeID] = false
			m.shareTree.rebuildVisible()
			m.shareCursor = max(0, min(cursor, len(m.shareTree.visible)-1))
		} else {
			m.shareCursor = m.shareTree.addShareChildren(x.nodeID, x.entries, cursor)
		}
		if m.workspace == 3 {
			m.cursor = m.shareCursor
		}
	case settingsMsg:
		m.err = errText(x.err)
		if x.err == nil {
			return m, m.loadStatus()
		}
	case transferActionMsg:
		m.err = errText(x.err)
		if m.workspace == 2 {
			m.transferCursors[m.transferTab] = m.cursor
		}
		m.transfers = x.transfers
		for tab, direction := range []string{"download", "upload"} {
			m.transferTrees[tab], m.transferCursors[tab] = buildTransferTree(m.transfers, direction, m.transferTrees[tab], m.transferCursors[tab])
		}
		if m.workspace == 2 {
			m.cursor = m.transferCursors[m.transferTab]
		}
	case tea.PasteMsg:
		text := strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, x.Content)
		if m.setup {
			m.setupVals[m.setupField], m.inputCursor = insertText(m.setupVals[m.setupField], text, m.inputCursor)
		} else if m.editing {
			m.input, m.inputCursor = insertText(m.input, text, m.inputCursor)
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
	if m.details {
		if s == "i" || s == "esc" {
			m.details = false
		}
		return nil
	}
	if m.folderMenu {
		return m.folderMenuKey(k)
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
	case "ctrl+pgup":
		if m.workspace == 0 {
			m.switchSearchTab(-1)
		} else if m.workspace == 1 {
			m.switchBrowseTab(-1)
		} else if m.workspace == 2 {
			m.switchTransferTab(0)
		}
	case "ctrl+pgdown":
		if m.workspace == 0 {
			m.switchSearchTab(1)
		} else if m.workspace == 1 {
			m.switchBrowseTab(1)
		} else if m.workspace == 2 {
			m.switchTransferTab(1)
		}
	case "ctrl+w":
		if m.workspace == 0 {
			m.closeSearchTab()
		} else if m.workspace == 1 {
			m.closeBrowseTab()
		}
	case "tab":
		m.switchWorkspace(m.workspace + 1)
	case "shift+tab":
		m.switchWorkspace(m.workspace - 1)
	case "right":
		if m.workspace == 4 {
			m.settingsSection = (m.settingsSection + 1) % 3
			m.cursor, m.selected = 0, map[int]bool{}
		} else {
			return m.openTreeNode(false)
		}
	case "left":
		if m.workspace == 4 {
			m.settingsSection = (m.settingsSection + 2) % 3
			m.cursor, m.selected = 0, map[int]bool{}
		} else if tree := m.currentTree(); tree != nil {
			m.cursor = tree.left(m.cursor)
		}
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
	case "b":
		if m.workspace == 0 {
			_, node := m.searchTree.node(m.cursor)
			if node != nil {
				folder := node.path
				if node.kind == treeFile {
					folder, _ = resultPath(folder)
				}
				return m.openBrowse(node.user, folder, false)
			}
		}
	case "i":
		if m.workspace == 0 || m.workspace == 1 {
			_, node := m.currentTree().node(m.cursor)
			m.details = node != nil && node.kind == treeFile && node.source >= 0
		}
	case "d":
		if m.workspace == 0 {
			if m.openFolderMenu() {
				return nil
			}
			return m.queueResult()
		}
		if m.workspace == 1 {
			if m.openFolderMenu() {
				return nil
			}
			return m.queueBrowse()
		}
		if m.workspace == 2 {
			return m.action("cancel")
		}
		if m.workspace == 3 {
			return m.removeShare()
		}
	case "r":
		if m.workspace == 1 && m.browseUser != "" {
			return m.openBrowse(m.browseUser, "", true)
		}
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
			m.inputCursor = len([]rune(m.input))
			return nil
		}
	case "/":
		m.beginEdit()
		return nil
	}
	return nil
}

func (m *model) openFolderMenu() bool {
	for _, selected := range m.selected {
		if selected {
			return false
		}
	}
	tree := m.currentTree()
	if tree == nil {
		return false
	}
	index, node := tree.node(m.cursor)
	if node == nil || node.kind != treeFolder || normalizeBrowsePath(node.path) == "" {
		return false
	}
	user := node.user
	if user == "" && m.workspace == 1 {
		user = m.browseUser
	}
	if user == "" {
		return false
	}
	fileAt := func(source int) (download, bool) {
		if m.workspace == 0 && source >= 0 && source < len(m.results) && !m.results[source].directory {
			return download{filename: m.results[source].path, size: m.results[source].size}, true
		}
		if m.workspace == 1 && source >= 0 && source < len(m.entries) && !m.entries[source].directory {
			return download{filename: m.entries[source].name, size: m.entries[source].size}, true
		}
		return download{}, false
	}
	var directFiles []download
	for _, child := range node.children {
		if tree.nodes[child].kind == treeFile {
			if file, ok := fileAt(tree.nodes[child].source); ok {
				directFiles = append(directFiles, file)
			}
		}
	}
	allFiles := make([]download, 0, len(node.leaves))
	for _, source := range node.leaves {
		if file, ok := fileAt(source); ok {
			allFiles = append(allFiles, file)
		}
	}
	var subfolders []string
	var visit func(int)
	visit = func(parent int) {
		for _, child := range tree.nodes[parent].children {
			if tree.nodes[child].kind != treeFolder {
				continue
			}
			subfolders = append(subfolders, normalizeBrowsePath(tree.nodes[child].path))
			visit(child)
		}
	}
	visit(index)
	m.folderMenu, m.folderMenuChoice = true, 0
	m.folderMenuUser, m.folderMenuPath = user, normalizeBrowsePath(node.path)
	m.folderMenuSubfolders, m.folderMenuFiles = subfolders, [2][]download{directFiles, allFiles}
	return true
}

func (m *model) folderMenuRequest() daemon.FolderDownloadRequest {
	recursive := m.folderMenuChoice == 1
	req := daemon.FolderDownloadRequest{Username: m.folderMenuUser, Folder: m.folderMenuPath, Recursive: recursive}
	if recursive {
		req.Subfolders = append([]string(nil), m.folderMenuSubfolders...)
	}
	for _, file := range m.folderMenuFiles[m.folderMenuChoice] {
		req.Files = append(req.Files, daemon.DownloadItem{Filename: file.filename, Size: file.size})
	}
	return req
}

func (m *model) folderMenuKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.folderMenu = false
	case "up", "k":
		m.folderMenuChoice = max(0, m.folderMenuChoice-1)
	case "down", "j":
		m.folderMenuChoice = min(1, m.folderMenuChoice+1)
	case "enter":
		req := m.folderMenuRequest()
		m.folderMenu = false
		return m.queueFolder(req)
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
	m.inputCursor = len([]rune(m.input))
}
func (m *model) editKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if m.filterEditing && (s == "tab" || s == "shift+tab") {
		before := inputBeforeCursor(m.input, m.inputCursor)
		after := []rune(m.input)[len([]rune(before)):]
		completed := completeFilter(before, s == "shift+tab")
		m.input = completed + string(after)
		m.inputCursor = len([]rune(completed))
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
			return m.openSearch(m.input)
		}
		if m.workspace == 1 {
			return m.openBrowse(m.input, "", false)
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
	m.input, m.inputCursor, _ = editText(m.input, m.inputCursor, k)
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
	tree := m.currentTree()
	if tree == nil {
		return nil
	}
	_, node := tree.node(m.cursor)
	if node == nil {
		return nil
	}
	if node.kind != treeFile {
		return m.openTreeNode(true)
	}
	if node.source >= 0 && (m.workspace == 0 || m.workspace == 1) {
		m.selected[node.source] = true
		if m.workspace == 0 {
			return m.queueResult()
		}
		return m.queueBrowse()
	}
	return nil
}

func (m *model) toggle() {
	if m.workspace != 0 && m.workspace != 1 {
		return
	}
	tree := m.currentTree()
	index, node := tree.node(m.cursor)
	if node == nil {
		return
	}
	chosen, total := tree.selection(index, m.selected)
	selectAll := chosen != total
	for _, source := range node.leaves {
		m.selected[source] = selectAll
	}
}
func (m *model) queueResult() tea.Cmd {
	byUser := make(map[string][]daemon.DownloadItem)
	for i, x := range m.results {
		if m.selected[i] && !x.directory {
			byUser[x.user] = append(byUser[x.user], daemon.DownloadItem{Filename: x.path, Size: x.size})
		}
	}
	if len(byUser) == 0 {
		_, node := m.searchTree.node(m.cursor)
		if node != nil {
			for _, source := range node.leaves {
				x := m.results[source]
				byUser[x.user] = append(byUser[x.user], daemon.DownloadItem{Filename: x.path, Size: x.size})
			}
		}
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
	for source, item := range m.entries {
		if m.selected[source] && !item.directory {
			chosen[item.name] = download{item.name, item.size}
		}
	}
	if len(chosen) == 0 {
		_, node := m.browseTree.node(m.cursor)
		if node != nil {
			for _, source := range node.leaves {
				item := m.entries[source]
				chosen[item.name] = download{item.name, item.size}
			}
		}
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
	_, node := m.shareTree.node(m.cursor)
	if node == nil || node.kind != treeShareRoot || node.source < 0 || node.source >= len(m.shares) {
		return nil
	}
	name := m.shares[node.source].name
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

func (m *model) queueFolder(req daemon.FolderDownloadRequest) tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.QueueFolder(m.ctx, req)
		return folderDownloadMsg{err: err}
	}
}

func (m model) transferActionIDs() []string {
	tree := &m.transferTrees[m.transferTab]
	_, node := tree.node(m.cursor)
	if node == nil {
		return nil
	}
	ids := make([]string, 0, len(node.leaves))
	for _, source := range node.leaves {
		ids = append(ids, m.transfers[source].id)
	}
	return ids
}
func (m model) action(action string) tea.Cmd {
	ids := m.transferActionIDs()
	if len(ids) == 0 {
		return nil
	}
	return func() tea.Msg {
		var first error
		for _, id := range ids {
			if err := m.client.TransferAction(m.ctx, id, action); err != nil && first == nil {
				first = err
			}
		}
		transfers, err := m.client.Transfers(m.ctx)
		view := toTransfers(transfers)
		if err != nil {
			view = m.transfers
		}
		if first == nil {
			first = err
		}
		return transferActionMsg{transfers: view, err: first}
	}
}
func (m model) active() bool {
	for _, x := range m.transfers {
		if x.state == "queued" || x.state == "incomplete" || x.state == "running" {
			return true
		}
	}
	return false
}
