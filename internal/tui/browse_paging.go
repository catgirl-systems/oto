package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
)

type browsePageState struct {
	folder, query, err  string
	entries, ancestors  []daemon.BrowseEntry
	cursor, next, total int
	history             []int
	loading, loaded     bool
	request             uint64
}

type browsePageMsg struct {
	user, folder, query string
	request, revision   uint64
	page                daemon.BrowsePage
	err                 error
}

func browsePageKey(folder, query string) string {
	return normalizeBrowsePath(folder) + "\x00" + normalizeBrowseQuery(query)
}
func remoteNodeID(id int) string { return "remote:" + strconv.Itoa(id) }
func remoteNodeIDValue(id string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(id, "remote:"))
	return n
}
func browseEntryLabel(path string) string {
	path = normalizeBrowsePath(path)
	if i := strings.LastIndexByte(path, '\\'); i >= 0 {
		return path[i+1:]
	}
	return path
}
func remoteKind(item daemon.BrowseEntry) treeNodeKind {
	if item.Directory {
		return treeFolder
	}
	return treeFile
}

func rebuildRemoteBrowse(tab *browseTab) {
	oldID := tab.tree.cursorID(tab.cursor)
	byID := map[int]daemon.BrowseEntry{}
	for _, page := range tab.pages {
		if normalizeBrowseQuery(page.query) != normalizeBrowseQuery(tab.filter) {
			continue
		}
		if tab.filter == "" {
			for _, item := range page.ancestors {
				byID[item.ID] = item
			}
		}
		for _, item := range page.entries {
			byID[item.ID] = item
		}
	}
	ids := make([]int, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	tab.remote = make([]daemon.BrowseEntry, 0, len(ids))
	tab.entries = make([]entry, 0, len(ids))
	t := newTree(false, tab.tree)
	for _, id := range ids {
		item := byID[id]
		source := len(tab.remote)
		tab.remote = append(tab.remote, item)
		tab.entries = append(tab.entries, toEntry(item.ShareEntry))
		parent, label := "", browseEntryLabel(item.Name)
		if tab.filter == "" && len(item.Ancestors) > 0 {
			parent = remoteNodeID(item.Ancestors[len(item.Ancestors)-1])
		}
		if tab.filter != "" {
			label = normalizeBrowsePath(item.Name)
		}
		t.add(remoteNodeID(id), parent, label, normalizeBrowsePath(item.Name), "", "", remoteKind(item), source)
	}
	t.sortChildren(true)
	keys := make([]string, 0, len(tab.pages))
	for key := range tab.pages {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		page := tab.pages[key]
		if normalizeBrowseQuery(page.query) != normalizeBrowseQuery(tab.filter) {
			continue
		}
		parent := ""
		if tab.filter == "" && len(page.ancestors) > 0 {
			parent = remoteNodeID(page.ancestors[len(page.ancestors)-1].ID)
		}
		add := func(cursor int, label string) {
			if page.loading {
				label = "Loading…"
			}
			t.add("browse-page:"+key+":"+strconv.Itoa(cursor), parent, label, page.folder, "", page.query, treePage, cursor)
		}
		if len(page.history) > 0 {
			add(page.history[len(page.history)-1], "‹ Previous page")
		}
		if page.next > 0 {
			add(page.next, fmt.Sprintf("Next page › (%d–%d of %d)", page.cursor+1, page.cursor+len(page.entries), page.total))
		}
	}
	// Unlike the eager trees, remote folder selection uses daemon IDs, not copied descendant lists.
	t.rebuildVisible()
	tab.tree = t
	tab.cursor = max(0, min(tab.cursor, len(t.visible)-1))
	for i, index := range t.visible {
		if t.nodes[index].id == oldID {
			tab.cursor = i
			break
		}
	}
}

func applyBrowsePage(tab *browseTab, msg browsePageMsg) {
	if tab.pages == nil {
		tab.pages = map[string]browsePageState{}
	}
	key := browsePageKey(msg.folder, msg.query)
	old := tab.pages[key]
	history := old.history
	if msg.page.Cursor > old.cursor {
		history = append(history, old.cursor)
	}
	for len(history) > 0 && history[len(history)-1] >= msg.page.Cursor {
		history = history[:len(history)-1]
	}
	// Replacing a directory page evicts descendants of its previous page, rather than accumulating the entire share.
	if msg.page.Cursor != tab.pages[key].cursor {
		for oldKey, old := range tab.pages {
			if oldKey != key && old.query == msg.query && (msg.folder == "" || strings.HasPrefix(old.folder, msg.folder+"\\")) {
				evictBrowsePage(tab.pages, oldKey)
			}
		}
	}
	delete(tab.pages, key)
	key = browsePageKey(msg.page.Folder, msg.page.Query)
	tab.pages[key] = browsePageState{folder: msg.page.Folder, query: msg.page.Query, entries: msg.page.Entries, ancestors: msg.page.Ancestors, cursor: msg.page.Cursor, next: msg.page.NextCursor, total: msg.page.Total, history: history, loaded: true}
	tab.loaded, tab.paged, tab.loading = true, true, false
	tab.revision, tab.cached, tab.savedAt = msg.page.Revision, msg.page.Cached, msg.page.SavedAt
	tab.pageTotal = msg.page.TotalEntries
	if tab.tree.expanded == nil {
		tab.tree.expanded = map[string]bool{}
	}
	if msg.query == "" {
		for _, item := range msg.page.Ancestors {
			tab.tree.expanded[remoteNodeID(item.ID)] = true
		}
	}
	rebuildRemoteBrowse(tab)
	if msg.page.Cursor != old.cursor && len(msg.page.Entries) > 0 {
		for i, index := range tab.tree.visible {
			if tab.tree.nodes[index].id == remoteNodeID(msg.page.Entries[0].ID) {
				tab.cursor = i
				break
			}
		}
	}
	if tab.target != "" {
		for i, index := range tab.tree.visible {
			if strings.EqualFold(tab.tree.nodes[index].path, normalizeBrowsePath(tab.target)) {
				tab.cursor = i
				break
			}
		}
		tab.target = ""
	}
}

func (m *model) requestRemotePage(folder, query string, cursor int) tea.Cmd {
	if m.browseTabIndex < 0 || m.browseTabIndex >= len(m.browseTabs) || m.browseRevision == 0 {
		return nil
	}
	m.saveBrowseTab()
	tab := &m.browseTabs[m.browseTabIndex]
	if tab.pages == nil {
		tab.pages = map[string]browsePageState{}
	}
	key := browsePageKey(folder, query)
	state := tab.pages[key]
	if state.loading {
		return nil
	}
	m.browseRequest++
	state.folder, state.query, state.loading, state.request = folder, query, true, m.browseRequest
	state.err = ""
	tab.pages[key] = state
	m.loadBrowseTab(m.browseTabIndex)
	user, revision, request := tab.user, tab.revision, state.request
	return func() tea.Msg {
		page, err := m.client.BrowsePage(m.ctx, daemon.BrowsePageRequest{Username: user, Revision: revision, Folder: folder, Query: query, Cursor: cursor})
		return browsePageMsg{user: user, request: request, revision: revision, folder: folder, query: query, page: page, err: err}
	}
}

func (m model) queueRemoteBrowse(selection map[int]bool, folder string, recursive bool, destination string) tea.Cmd {
	user, revision, dir := m.browseUser, m.browseRevision, m.cfg.DownloadDir
	if folder != "" {
		user, revision, dir = m.folderMenuUser, m.folderMenuRevision, m.folderMenuDownloadDir
	}
	rules := make(map[int]bool, len(selection))
	for id, value := range selection {
		rules[id] = value
	}
	return func() tea.Msg {
		_, err := m.client.QueueBrowse(m.ctx, daemon.BrowseDownloadRequest{Username: user, Revision: revision, Selection: rules, Folder: folder, Recursive: recursive, DownloadDir: dir, Destination: destination})
		return folderDownloadMsg{err: err}
	}
}

func (m *model) remoteNodeChosen(index int) bool {
	if index < 0 || index >= len(m.browseTree.nodes) {
		return false
	}
	node := m.browseTree.nodes[index]
	if node.kind == treePage || node.source < 0 || node.source >= len(m.browseRemote) {
		return false
	}
	item := m.browseRemote[node.source]
	if value, ok := m.selected[item.ID]; ok {
		return value
	}
	for i := len(item.Ancestors) - 1; i >= 0; i-- {
		if value, ok := m.selected[item.Ancestors[i]]; ok {
			return value
		}
	}
	return false
}
func (m *model) remoteMark(index int) string {
	node := m.browseTree.nodes[index]
	if node.kind == treePage {
		return " "
	}
	chosen := m.remoteNodeChosen(index)
	if node.source >= 0 && node.source < len(m.browseRemote) {
		id := m.browseRemote[node.source].ID
		for rule, ancestors := range m.browseRuleAncestors {
			if m.selected[rule] == chosen {
				continue
			}
			for _, ancestor := range ancestors {
				if ancestor == id {
					return "◐"
				}
			}
		}
	}
	if chosen {
		return "●"
	}
	return "○"
}
