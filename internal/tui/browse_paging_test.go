package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/ipc"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestRemoteBrowseLazyPagesSelectionAndFiltering(t *testing.T) {
	music := daemon.BrowseEntry{ID: 1, ShareEntry: soulseek.ShareEntry{Name: "Music", Directory: true}, ChildCount: 201, FileCount: 201}
	sub := daemon.BrowseEntry{ID: 2, ShareEntry: soulseek.ShareEntry{Name: `Music\Sub`, Directory: true}, Ancestors: []int{1}, ChildCount: 1, FileCount: 1}
	hidden := daemon.BrowseEntry{ID: 203, ShareEntry: soulseek.ShareEntry{Name: `Music\Sub\hidden.flac`, Size: 42, Private: true, BitDepth: 24}, Ancestors: []int{1, 2}}
	root := daemon.BrowsePage{Entries: []daemon.BrowseEntry{music}, Total: 1, TotalEntries: 203, Revision: 17}
	requests := atomic.Int32{}
	queued := make(chan daemon.BrowseDownloadRequest, 1)
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "ipc.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/v1/browse/download" {
			var req daemon.BrowseDownloadRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			queued <- req
			_ = json.NewEncoder(w).Encode(daemon.BrowseDownloadResult{Queued: 201})
			return
		}
		page := root
		if r.URL.Path == "/v1/browse/page" {
			if r.URL.Query().Get("revision") != "17" {
				t.Error("page omitted snapshot revision")
			}
			page.Folder, page.Query = r.URL.Query().Get("folder"), r.URL.Query().Get("query")
			page.Cursor, _ = strconv.Atoi(r.URL.Query().Get("cursor"))
			page.Entries = nil
			if page.Query != "" {
				page.Entries, page.Total = []daemon.BrowseEntry{hidden}, 1
			} else {
				page.Ancestors, page.Total = []daemon.BrowseEntry{music}, 201
				if page.Cursor == 0 {
					page.Entries, page.NextCursor = []daemon.BrowseEntry{sub}, 200
					for i := 0; i < 199; i++ {
						page.Entries = append(page.Entries, daemon.BrowseEntry{ID: i + 3, ShareEntry: soulseek.ShareEntry{Name: fmt.Sprintf(`Music\f%03d.flac`, i), Size: 10}, Ancestors: []int{1}})
					}
				} else {
					page.Entries = []daemon.BrowseEntry{{ID: 202, ShareEntry: soulseek.ShareEntry{Name: `Music\f199.flac`}, Ancestors: []int{1}}}
				}
			}
		}
		_ = json.NewEncoder(w).Encode(page)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	m := model{ctx: context.Background(), client: ipc.NewClient(listener.Addr().String()), workspace: workspaceBrowse, browseTabIndex: -1, width: 100, height: 30}
	update := func(msg tea.Msg) { next, _ := m.Update(msg); m = next.(model) }
	m.openBrowse("peer", "", false)
	update(browseMsg{user: "peer", request: m.browseTabs[0].request, page: root})
	if len(m.entries) != 1 || len(m.browseTree.nodes) != 1 {
		t.Fatal("opening eagerly populated the share")
	}
	m.toggle() // Select the unopened folder, including files not present in the TUI.
	cmd := m.openTreeNode(false)
	if cmd == nil {
		t.Fatal("folder expansion did not request a page")
	}
	update(cmd())
	if len(m.entries) != 201 || requests.Load() != 1 {
		t.Fatalf("not a single bounded folder page: entries=%d requests=%d", len(m.entries), requests.Load())
	}
	m.cursor = 0
	if cmd := m.openTreeNode(true); cmd != nil {
		t.Fatal("collapse fetched peer")
	}
	if cmd := m.openTreeNode(true); cmd != nil {
		t.Fatal("cached expansion fetched peer")
	}
	for i, index := range m.browseTree.visible {
		if m.browseTree.nodes[index].kind == treePage {
			m.cursor = i
			break
		}
	}
	cmd = m.openTreeNode(true)
	if cmd == nil {
		t.Fatal("next-page row did not request a page")
	}
	update(cmd())
	if len(m.entries) != 2 || m.browsePages[browsePageKey("Music", "")].cursor != 200 {
		t.Fatalf("pages accumulated instead of replacing: %d", len(m.entries))
	}
	index, node := m.browseTree.node(m.cursor)
	if node == nil || node.id != remoteNodeID(202) || !m.remoteNodeChosen(index) {
		t.Fatal("next page lost inherited selection or cursor")
	}
	m.toggle()
	if value, ok := m.selected[202]; !ok || value || m.remoteMark(m.browseTree.byID[remoteNodeID(1)]) != "◐" {
		t.Fatal("file exclusion lost")
	}
	m.cursor = 0
	m.toggle()
	if len(m.selected) != 1 || !m.selected[1] {
		t.Fatal("selecting partial folder did not clear descendant overrides")
	}
	cmd = m.queueBrowse()
	if msg := cmd().(folderDownloadMsg); msg.err != nil {
		t.Fatal(msg.err)
	}
	req := <-queued
	if req.Revision != 17 || len(req.Selection) != 1 || !req.Selection[1] {
		t.Fatalf("queue enumerated loaded files instead of snapshot rules: %+v", req)
	}
	m.editing, m.browseFindEditing, m.input = true, true, "hidden"
	cmd = m.editKey(key("enter"))
	if cmd == nil {
		t.Fatal("find did not query snapshot")
	}
	update(cmd())
	if len(m.entries) != 1 || m.entries[0].name != hidden.Name || !m.entries[0].private {
		t.Fatalf("find missed unloaded/private entry: %+v", m.entries)
	}
	m.selected = map[int]bool{1: true}
	if !m.remoteNodeChosen(0) {
		t.Fatal("flat find result lost unloaded ancestor selection")
	}
	m.openBrowse("other", "", false)
	if m.browseUser != "other" {
		t.Fatal("tab switch failed")
	}
	if cmd := m.openBrowse("peer", "", false); cmd != nil || m.browseFilter != "hidden" || len(m.entries) != 1 {
		t.Fatal("tab did not retain bounded find page")
	}
	old := m.browseTabs[0].request
	m.openBrowse("peer", "", true)
	update(browseMsg{user: "peer", request: old, page: root})
	if m.browseLoaded || len(m.entries) != 0 {
		t.Fatal("stale browse response replaced refresh")
	}
}

func TestBrowsePageHistoryAndStaleReply(t *testing.T) {
	tab := browseTab{user: "peer", selected: map[int]bool{}}
	page := daemon.BrowsePage{Revision: 8, Total: 12, TotalEntries: 12, Entries: []daemon.BrowseEntry{{ID: 1, ShareEntry: soulseek.ShareEntry{Name: "one"}}}, NextCursor: 1}
	applyBrowsePage(&tab, browsePageMsg{page: page})
	page.Cursor, page.NextCursor, page.Entries[0].ID = 1, 2, 2
	applyBrowsePage(&tab, browsePageMsg{page: page})
	if history := tab.pages[browsePageKey("", "")].history; len(history) != 1 || history[0] != 0 {
		t.Fatalf("byte-limited page history: %v", history)
	}
	m := model{workspace: workspaceBrowse, browseTabs: []browseTab{tab}}
	m.loadBrowseTab(0)
	state := m.browsePages[browsePageKey("", "")]
	state.request, state.loading = 9, true
	m.browsePages[browsePageKey("", "")] = state
	next, _ := m.Update(browsePageMsg{user: "peer", request: 9, revision: 7, page: page})
	m = next.(model)
	if !m.browsePages[browsePageKey("", "")].loading {
		t.Fatal("stale revision consumed current request")
	}
}

func TestBrowseFolderPageRetry(t *testing.T) {
	tab := browseTab{user: "peer", selected: map[int]bool{}}
	applyBrowsePage(&tab, browsePageMsg{page: daemon.BrowsePage{Revision: 8, Entries: []daemon.BrowseEntry{{ID: 1, ShareEntry: soulseek.ShareEntry{Name: "Music", Directory: true}}}}})
	m := model{workspace: workspaceBrowse, browseTabs: []browseTab{tab}}
	m.loadBrowseTab(0)
	if cmd := m.openTreeNode(false); cmd == nil {
		t.Fatal("first expansion did not load")
	}
	request := m.browsePages[browsePageKey("Music", "")].request
	next, _ := m.Update(browsePageMsg{user: "peer", folder: "Music", revision: 8, request: request, err: fmt.Errorf("temporary failure")})
	m = next.(model)
	if cmd := m.openTreeNode(false); cmd == nil {
		t.Fatal("failed page became permanently cached")
	}
}
