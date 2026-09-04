package tui

import (
	"context"
	"errors"
	"fmt"
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
	m.initializeHistory()
	if strings.TrimSpace(cfg.Soulseek.Username) == "" || strings.TrimSpace(cfg.Soulseek.Password) == "" {
		m.setup = true
	}
	_, err = tea.NewProgram(m).Run()
	return err
}

type tickMsg time.Time
type activityTickMsg time.Time

const (
	noticeDuration   = 2500 * time.Millisecond
	activityInterval = 100 * time.Millisecond
)

type statusMsg struct {
	snapshot daemon.Snapshot
	err      error
}
type portCheckMsg struct {
	port   uint16
	result daemon.ListeningPortCheck
	err    error
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
type wishlistMsg struct {
	items []daemon.WishlistItem
	err   error
}
type wishlistActionMsg struct {
	items         []daemon.WishlistItem
	page          *daemon.SearchPage
	query, filter string
	notice        string
	err           error
}
type browseMsg struct {
	user     string
	request  uint64
	entries  []entry
	cached   bool
	savedAt  time.Time
	revision uint64
	err      error
}
type browseProgressMsg struct {
	user     string
	request  uint64
	progress *daemon.BrowseProgress
	err      error
}
type savedBrowsesMsg struct {
	saved []daemon.SavedBrowse
	err   error
}
type saveBrowseMsg struct {
	user     string
	revision uint64
	saved    daemon.SavedBrowse
	err      error
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
type settingsMsg struct {
	search config.Search
	err    error
}
type networkInterfacesMsg struct {
	names []string
	err   error
}
type passwordMsg struct {
	password string
	result   daemon.PasswordChangeResult
	err      error
}
type presenceMsg struct {
	presence daemon.Presence
	err      error
}
type transferActionMsg struct {
	transfers []transfer
	err       error
}

func tick() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }
func activityTick() tea.Cmd {
	return tea.Tick(activityInterval, func(t time.Time) tea.Msg { return activityTickMsg(t) })
}

func (m *model) withActivity(cmd tea.Cmd) tea.Cmd {
	if m.activityRunning {
		return cmd
	}
	m.activityRunning = true
	return tea.Batch(cmd, activityTick())
}

func (m model) loadBrowseProgress(operation activity) tea.Cmd {
	return func() tea.Msg {
		progress, err := m.client.BrowseProgress(m.ctx, operation.label)
		return browseProgressMsg{user: operation.label, request: operation.request, progress: progress, err: err}
	}
}

func (m *model) setNotice(message string) {
	m.notice = message
	m.noticeUntil = time.Now().Add(noticeDuration)
}
func (m model) loadStatus() tea.Cmd {
	return func() tea.Msg { s, e := m.client.Status(m.ctx); return statusMsg{s, e} }
}

func (m model) loadWishlist() tea.Cmd {
	return func() tea.Msg { items, err := m.client.Wishlist(m.ctx); return wishlistMsg{items, err} }
}

func (m model) putWishlist(query, filter, notice string) tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.PutWishlist(m.ctx, query, filter)
		if err != nil {
			return wishlistActionMsg{err: err}
		}
		items, err := m.client.Wishlist(m.ctx)
		return wishlistActionMsg{items: items, notice: notice, err: err}
	}
}

func (m model) removeWishlist(id string) tea.Cmd {
	return func() tea.Msg {
		if err := m.client.RemoveWishlist(m.ctx, id); err != nil {
			return wishlistActionMsg{err: err}
		}
		items, err := m.client.Wishlist(m.ctx)
		return wishlistActionMsg{items: items, notice: "Wishlist item removed", err: err}
	}
}

func (m model) openWishlist(item daemon.WishlistItem, rerun bool) tea.Cmd {
	return func() tea.Msg {
		var page daemon.SearchPage
		var err error
		if rerun {
			page, err = m.client.RunWishlist(m.ctx, item.ID)
		} else {
			page, err = m.client.OpenWishlist(m.ctx, item.ID)
		}
		if err != nil {
			if !rerun && strings.Contains(err.Error(), "no cached results") {
				return wishlistActionMsg{items: m.wishlist, notice: "No cached results; press r to run this item"}
			}
			return wishlistActionMsg{err: err}
		}
		items, err := m.client.Wishlist(m.ctx)
		return wishlistActionMsg{items: items, page: &page, query: item.Query, filter: item.Filter, err: err}
	}
}

func ringBell() tea.Cmd {
	return func() tea.Msg { _, _ = os.Stderr.WriteString("\a"); return nil }
}

func (m model) checkListeningPort() tea.Cmd {
	port := m.status.publicPort
	return func() tea.Msg {
		result, err := m.client.CheckListeningPort(m.ctx)
		return portCheckMsg{port: port, result: result, err: err}
	}
}
func (m model) loadNetworkInterfaces() tea.Cmd {
	if m.client == nil {
		return nil
	}
	return func() tea.Msg {
		names, err := m.client.NetworkInterfaces(m.ctx)
		return networkInterfacesMsg{names: names, err: err}
	}
}
func (m model) setPresence(presence daemon.Presence) tea.Cmd {
	return func() tea.Msg { return presenceMsg{presence, m.client.SetPresence(m.ctx, presence)} }
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
	m.recordHistory(query, false)
	if m.workspace == workspaceSearch {
		m.saveSearchTab()
	}
	m.searchRequest++
	m.searchOperation++
	tab := searchTab{query: query, filter: m.searchFilter, selected: map[int]bool{}, loading: true, searching: true, request: m.searchRequest, operation: m.searchOperation}
	m.searchTabs = append(m.searchTabs, tab)
	m.workspace = workspaceSearch
	m.loadSearchTab(len(m.searchTabs) - 1)
	return m.withActivity(m.search(tab.query, tab.filter, tab.request, tab.operation))
}

func (m *model) openSearchPage(query, filter string, page daemon.SearchPage) {
	if m.workspace == workspaceSearch {
		m.saveSearchTab()
	}
	m.searchRequest++
	m.searchOperation++
	tab := searchTab{query: query, filter: filter, selected: map[int]bool{}, request: m.searchRequest, operation: m.searchOperation}
	applySearchMsg(&tab, searchMsg{page: page, request: tab.request, operation: tab.operation, filter: filter})
	m.searchTabs = append(m.searchTabs, tab)
	m.workspace = workspaceSearch
	m.loadSearchTab(len(m.searchTabs) - 1)
}

func applySearchMsg(tab *searchTab, message searchMsg) {
	tab.err = errText(message.err)
	if message.append {
		tab.loadingMore = false
	} else {
		tab.loading, tab.searching = false, false
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
		result, err := m.client.Browse(m.ctx, user)
		return browseMsg{user: user, request: request, entries: toEntries(result.Entries), cached: result.Cached, savedAt: result.SavedAt, revision: result.Revision, err: err}
	}
}

func (m model) loadSavedBrowses() tea.Cmd {
	return func() tea.Msg {
		saved, err := m.client.SavedBrowses(m.ctx)
		return savedBrowsesMsg{saved: saved, err: err}
	}
}

func (m model) saveBrowse() tea.Cmd {
	user, revision := m.browseUser, m.browseRevision
	return func() tea.Msg {
		saved, err := m.client.SaveBrowse(m.ctx, user, revision)
		return saveBrowseMsg{user: user, revision: revision, saved: saved, err: err}
	}
}

func (m *model) upsertSavedBrowse(saved daemon.SavedBrowse) {
	for i := range m.savedBrowses {
		if strings.EqualFold(m.savedBrowses[i].Username, saved.Username) {
			m.savedBrowses[i] = saved
			sort.Slice(m.savedBrowses, func(i, j int) bool { return m.savedBrowses[i].Username < m.savedBrowses[j].Username })
			return
		}
	}
	m.savedBrowses = append(m.savedBrowses, saved)
	sort.Slice(m.savedBrowses, func(i, j int) bool { return m.savedBrowses[i].Username < m.savedBrowses[j].Username })
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
	tab.loaded, tab.cached, tab.revision, tab.savedAt = m.browseLoaded, m.browseCached, m.browseRevision, m.browseSavedAt
}

func (m *model) loadBrowseTab(index int) {
	if index < 0 || index >= len(m.browseTabs) {
		m.browseTabIndex = -1
		m.browseUser, m.entries, m.cursor, m.loading = "", nil, max(0, min(m.savedBrowseCursor, len(m.savedBrowses)-1)), false
		m.browseLoaded, m.browseCached, m.browseRevision, m.browseSavedAt = false, false, 0, time.Time{}
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
	m.browseLoaded, m.browseCached, m.browseRevision, m.browseSavedAt = tab.loaded, tab.cached, tab.revision, tab.savedAt
	m.err = tab.err
}

func (m *model) switchWorkspace(next workspace) {
	if m.workspace == workspaceSearch {
		m.saveSearchTab()
	} else if m.workspace == workspaceWishlist {
		m.wishlistCursor = m.cursor
	} else if m.workspace == workspaceBrowse {
		if len(m.browseTabs) > 0 {
			m.saveBrowseTab()
		} else {
			m.savedBrowseCursor = m.cursor
		}
	} else if m.workspace == workspaceTransfers {
		m.transferCursors[m.transferTab] = m.cursor
	} else if m.workspace == workspaceShares {
		m.shareCursor = m.cursor
	}
	m.workspace = (next + workspaceCount) % workspaceCount
	if m.workspace == workspaceSearch {
		if len(m.searchTabs) > 0 {
			m.loadSearchTab(m.searchTabIndex)
		} else {
			m.cursor, m.selected, m.loading = 0, map[int]bool{}, false
		}
		return
	}
	if m.workspace == workspaceWishlist {
		m.cursor = max(0, min(m.wishlistCursor, len(m.wishlist)-1))
		m.selected, m.loading = map[int]bool{}, false
		return
	}
	if m.workspace == workspaceBrowse {
		m.loadBrowseTab(m.browseTabIndex)
		return
	}
	if m.workspace == workspaceTransfers {
		m.cursor = max(0, min(m.transferCursors[m.transferTab], len(m.transferTrees[m.transferTab].visible)-1))
		m.selected, m.loading = map[int]bool{}, false
		return
	}
	if m.workspace == workspaceShares {
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

func (m *model) switchTransferTab(next transferTab) {
	m.transferCursors[m.transferTab] = m.cursor
	m.transferTab = (next + transferTabCount) % transferTabCount
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
	if m.workspace == workspaceShares && !node.loaded && !node.loading {
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
	if m.workspace == workspaceBrowse {
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
	request := refresh || (!tab.loaded && !tab.loading)
	if request {
		m.browseRequest++
		tab.request, tab.loading = m.browseRequest, true
		tab.received, tab.total = 0, 0
	} else if tab.target != "" {
		tab.cursor = browseTargetCursor(tab.entries, tab.target)
		tab.target = ""
	}
	m.workspace = workspaceBrowse
	m.loadBrowseTab(index)
	if request {
		return m.withActivity(m.browse(tab.user, tab.request))
	}
	return nil
}

func (m model) saveSettings() tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg {
		_, err := m.client.UpdateConfig(m.ctx, cfg)
		return settingsMsg{search: cfg.Search, err: err}
	}
}

func (m model) changePassword(password string) tea.Cmd {
	return func() tea.Msg {
		result, err := m.client.ChangePassword(m.ctx, password)
		return passwordMsg{password: password, result: result, err: err}
	}
}

func (m model) Init() tea.Cmd {
	if m.setup {
		return nil
	}
	return tea.Batch(m.loadStatus(), m.loadTransfers(), m.loadShares(), m.loadSavedBrowses(), m.loadWishlist(), tick())
}
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch x := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = x.Width, x.Height
	case tickMsg:
		m.spinner++
		if m.notice != "" && !time.Time(x).Before(m.noticeUntil) {
			m.notice = ""
		}
		return m, tea.Batch(m.loadStatus(), m.loadTransfers(), m.loadShares(), m.loadWishlist(), tick())
	case activityTickMsg:
		if !m.activityRunning {
			break
		}
		operation, ok := m.currentActivity()
		if !ok {
			m.activityRunning = false
			break
		}
		m.activityFrame++
		if operation.kind == activityBrowse {
			return m, m.loadBrowseProgress(operation)
		}
		return m, activityTick()
	case statusMsg:
		if x.err != nil {
			m.status.err = x.err.Error()
		} else {
			m.status = snapshot{status: x.snapshot.Status, presence: x.snapshot.Presence, user: x.snapshot.Config.Soulseek.Username, publicIP: x.snapshot.PublicIP, publicPort: x.snapshot.PublicPort, err: x.snapshot.Error}
		}
	case portCheckMsg:
		m.portChecking, m.portCheckPort, m.err = false, x.port, errText(x.err)
		if x.err != nil {
			m.portCheckStatus = "unknown"
		} else {
			m.portCheckPort = x.result.Port
			if x.result.Open {
				m.portCheckStatus = "open"
			} else {
				m.portCheckStatus = "closed"
			}
		}
	case networkInterfacesMsg:
		if x.err != nil {
			m.networkInterfaces = nil
			m.err = x.err.Error()
		} else {
			m.networkInterfaces = x.names
			m.err = ""
		}
	case wishlistMsg:
		m.err = errText(x.err)
		if x.err == nil {
			if m.wishlistNotified == nil {
				m.wishlistNotified = map[string]uint64{}
			}
			bell := false
			for _, item := range x.items {
				if item.Unread && item.NotificationSequence > m.wishlistNotified[item.ID] && m.cfg.Search.WishlistNotifications {
					bell = true
				}
				m.wishlistNotified[item.ID] = item.NotificationSequence
			}
			if m.workspace == workspaceWishlist {
				m.wishlistCursor = m.cursor
			}
			m.wishlist = x.items
			m.wishlistCursor = max(0, min(m.wishlistCursor, len(m.wishlist)-1))
			if m.workspace == workspaceWishlist {
				m.cursor = m.wishlistCursor
			}
			if bell {
				return m, ringBell()
			}
		}
	case wishlistActionMsg:
		m.err = errText(x.err)
		if x.err == nil {
			m.wishlist = x.items
			m.wishlistCursor = max(0, min(m.wishlistCursor, len(m.wishlist)-1))
			if x.notice != "" {
				m.setNotice(x.notice)
			}
			if x.page != nil {
				m.openSearchPage(x.query, x.filter, *x.page)
			} else if m.workspace == workspaceWishlist {
				m.cursor = m.wishlistCursor
			}
		}
	case searchMsg:
		if x.request != 0 {
			if m.workspace == workspaceSearch {
				m.saveSearchTab()
			}
			for i := range m.searchTabs {
				tab := &m.searchTabs[i]
				if tab.request != x.request || tab.operation != x.operation {
					continue
				}
				if !x.append && !x.filterChange && x.err == nil && tab.filter != x.filter {
					cmd := m.refilterPendingSearch(tab, x.page)
					if m.workspace == workspaceSearch && i == m.searchTabIndex {
						m.loadSearchTab(i)
					}
					return m, cmd
				}
				applySearchMsg(tab, x)
				if m.workspace == workspaceSearch && i == m.searchTabIndex {
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
				tab.loaded, tab.cached, tab.revision, tab.savedAt = true, x.cached, x.revision, x.savedAt
				tab.tree, tab.cursor = buildBrowseTree(tab.entries, tab.tree, tab.cursor)
				if tab.target != "" {
					tab.cursor = tab.tree.cursorForSource(target)
				}
			}
			tab.target = ""
			if m.workspace == workspaceBrowse && i == m.browseTabIndex {
				m.loadBrowseTab(i)
				if x.err == nil && x.cached {
					m.setNotice("Showing saved share list; press r to retry")
				}
			}
			break
		}
	case browseProgressMsg:
		for i := range m.browseTabs {
			tab := &m.browseTabs[i]
			if !tab.loading || tab.request != x.request || !strings.EqualFold(tab.user, x.user) {
				continue
			}
			if x.err == nil {
				if x.progress == nil {
					tab.received, tab.total = 0, 0
				} else {
					tab.received, tab.total = x.progress.Received, x.progress.Total
				}
			}
			break
		}
		if _, ok := m.currentActivity(); !ok {
			m.activityRunning = false
			break
		}
		return m, activityTick()
	case savedBrowsesMsg:
		m.savedBrowseLoading = false
		m.err = errText(x.err)
		if x.err == nil {
			m.savedBrowses = x.saved
			m.savedBrowseCursor = max(0, min(m.savedBrowseCursor, len(m.savedBrowses)-1))
			if m.workspace == workspaceBrowse && len(m.browseTabs) == 0 {
				m.cursor = m.savedBrowseCursor
			}
		}
	case saveBrowseMsg:
		m.err = errText(x.err)
		if x.err == nil {
			m.upsertSavedBrowse(x.saved)
			for i := range m.browseTabs {
				tab := &m.browseTabs[i]
				if strings.EqualFold(tab.user, x.user) && tab.revision == x.revision {
					tab.savedAt = x.saved.SavedAt
					if m.workspace == workspaceBrowse && i == m.browseTabIndex {
						m.browseSavedAt = x.saved.SavedAt
					}
					break
				}
			}
			m.setNotice("Saved share list for " + x.saved.Username)
		}
	case folderDownloadMsg:
		m.err = errText(x.err)
	case transferMsg:
		if m.workspace == workspaceTransfers {
			m.transferCursors[m.transferTab] = m.cursor
		}
		if x.err == nil {
			setTransferSpeeds(x.transfers, m.transfers, x.at.Sub(m.transferSampleAt))
			m.transferSampleAt = x.at
		}
		m.transfers, m.err = x.transfers, errText(x.err)
		for tab, direction := range transferDirections {
			m.transferTrees[tab], m.transferCursors[tab] = buildTransferTree(m.transfers, direction, m.transferTrees[tab], m.transferCursors[tab])
		}
		if m.workspace == workspaceTransfers {
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
			if m.workspace == workspaceShares {
				cursor = m.cursor
			}
			m.shareTree, m.shareCursor = buildShareRoots(m.shares, m.shareTree, cursor, x.reset)
			if m.workspace == workspaceShares {
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
		if m.workspace == workspaceShares {
			cursor = m.cursor
		}
		if x.err != nil {
			m.shareTree.expanded[x.nodeID] = false
			m.shareTree.rebuildVisible()
			m.shareCursor = max(0, min(cursor, len(m.shareTree.visible)-1))
		} else {
			m.shareCursor = m.shareTree.addShareChildren(x.nodeID, x.entries, cursor)
		}
		if m.workspace == workspaceShares {
			m.cursor = m.shareCursor
		}
	case settingsMsg:
		m.err = errText(x.err)
		if x.err == nil {
			m.applyHistorySettings(x.search)
			m.setNotice("Settings saved")
			return m, m.loadStatus()
		}
	case passwordMsg:
		m.passwordChanging = false
		if x.err != nil {
			m.passwordErr = errText(x.err)
			break
		}
		if !x.result.Changed {
			m.passwordErr = "Soulseek did not confirm the password change"
			break
		}
		m.cfg.Soulseek.Password = x.password
		m.closePasswordForm()
		m.setNotice("Password changed")
		m.err = x.result.Warning
	case presenceMsg:
		m.err = errText(x.err)
		if x.err == nil {
			m.setNotice("Status set to " + string(x.presence))
			return m, m.loadStatus()
		}
	case transferActionMsg:
		m.err = errText(x.err)
		if m.workspace == workspaceTransfers {
			m.transferCursors[m.transferTab] = m.cursor
		}
		m.transfers = x.transfers
		for tab, direction := range transferDirections {
			m.transferTrees[tab], m.transferCursors[tab] = buildTransferTree(m.transfers, direction, m.transferTrees[tab], m.transferCursors[tab])
		}
		if m.workspace == workspaceTransfers {
			m.cursor = m.transferCursors[m.transferTab]
		}
	case tea.PasteMsg:
		text := strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, x.Content)
		if m.passwordForm && !m.passwordChanging {
			m.passwordVals[m.passwordField], m.inputCursor = insertText(m.passwordVals[m.passwordField], text, m.inputCursor)
			m.passwordErr = ""
			return m, nil
		}
		if m.setup {
			m.setupVals[m.setupField], m.inputCursor = insertText(m.setupVals[m.setupField], text, m.inputCursor)
		} else if m.editing {
			m.input, m.inputCursor = insertText(m.input, text, m.inputCursor)
			if m.workspace == workspaceSearch {
				m.historyCursor.reset(m.input)
			}
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
