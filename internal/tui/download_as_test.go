package tui

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/ipc"
)

func TestDownloadAsDialogAndRequests(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	type request struct {
		path      string
		downloads []daemon.DownloadRequest
		browse    daemon.BrowseDownloadRequest
	}
	requests := make(chan request, 8)
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "ipc.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := request{path: r.URL.Path}
		if r.URL.Path == "/v1/downloads" {
			err := json.NewDecoder(r.Body).Decode(&req.downloads)
			if err != nil {
				t.Error(err)
			}
			requests <- req
			_ = json.NewEncoder(w).Encode([]daemon.Download{})
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&req.browse); err != nil {
			t.Error(err)
		}
		requests <- req
		if strings.HasSuffix(req.browse.Destination, "old.flac") {
			http.NotFound(w, r) // Old daemon: no renamed-browse endpoint.
			return
		}
		if strings.HasSuffix(req.browse.Destination, "denied.flac") {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "stale browse revision"})
			return
		}
		_ = json.NewEncoder(w).Encode(daemon.BrowseDownloadResult{Queued: 1})
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	for _, source := range []string{"search", "saved", "paged"} {
		t.Run(source, func(t *testing.T) {
			m := model{ctx: context.Background(), client: ipc.NewClient(listener.Addr().String()), width: 80, height: 24, selected: map[int]bool{}}
			if source == "search" {
				m.workspace = workspaceSearch
				m.results = []result{{user: "Alice Smith", path: `Album\original.flac`, size: 8}}
				m.searchTree, _ = buildSearchTree(m.results, treeState{}, 0)
				m.cursor = m.searchTree.cursorForSource(0)
			} else {
				m.workspace, m.browseUser = workspaceBrowse, "Alice Smith"
				m.entries = []entry{{name: `Album\original.flac`, size: 8}}
				m.browseTree, _ = buildBrowseTree(m.entries, "", treeState{}, 0)
				m.cursor = m.browseTree.cursorForSource(0)
				if source == "paged" {
					m.browsePaged, m.browseRevision = true, 7
					m.browseTree = treeState{nodes: []treeNode{{kind: treeFile, id: remoteNodeID(99), source: 0}}, visible: []int{0}}
					m.cursor = 0
				}
			}
			if cmd := m.key(key("D")); cmd != nil || m.downloadAs == nil || m.downloadAs.name != "original.flac" {
				t.Fatal("Download as did not open without queueing")
			}
			for _, name := range []string{"", ".", "..", "../escape", `dir\escape`, "bad\nname"} {
				m.downloadAs.name = name
				if cmd := m.key(key("enter")); cmd != nil || m.downloadAs.pending || m.downloadAs.err == "" {
					t.Fatalf("invalid name submitted: %q", name)
				}
			}
			m.downloadAs.name, m.downloadAs.cursor = "", 0
			next, _ := m.Update(tea.PasteMsg{Content: "日本語.flac"})
			m = next.(model)
			if m.downloadAs.name != "日本語.flac" {
				t.Fatal("Unicode paste failed")
			}
			next, _ = m.Update(tea.PasteMsg{Content: "bad\nname"})
			m = next.(model)
			if m.downloadAs.name != "日本語.flac" || m.downloadAs.err == "" {
				t.Fatal("control paste accepted")
			}
			m.downloadAs.err = ""
			for _, width := range []int{40, 80} {
				m.width = width
				view := m.downloadAsView()
				if !strings.Contains(view, "Save as:") || !strings.Contains(view, "日本語.flac") {
					t.Fatal("missing editable filename")
				}
				for _, line := range strings.Split(view, "\n") {
					if lipgloss.Width(line) > width {
						t.Fatalf("dialog overflow at %d: %q", width, line)
					}
				}
			}
			if cmd := m.key(key("esc")); cmd != nil || m.downloadAs != nil || len(requests) != 0 {
				t.Fatal("cancel queued a download")
			}
			m.key(key("D"))
			m.downloadAs.name = "renamed.flac"
			m.browseRevision = 8 // The form must retain the revision shown when opened.
			cmd := m.key(key("enter"))
			if cmd == nil || !m.downloadAs.pending || m.key(key("enter")) != nil {
				t.Fatal("duplicate submission")
			}
			m.key(key("esc"))
			if m.downloadAs == nil {
				t.Fatal("claimed to cancel an already submitted queue")
			}
			msg := cmd().(downloadAsMsg)
			if msg.err != nil {
				t.Fatal(msg.err)
			}
			req := <-requests
			if source == "paged" {
				if req.path != "/v1/browse/download-as" || req.browse.Revision != 7 || !req.browse.Selection[99] || len(req.browse.Selection) != 1 || req.browse.Destination != "Alice_Smith/Album/renamed.flac" {
					t.Fatalf("paged rename: %+v", req)
				}
			} else {
				if req.path != "/v1/downloads" || len(req.downloads) != 1 || len(req.downloads[0].Files) != 1 {
					t.Fatalf("direct rename: %+v", req)
				}
				file := req.downloads[0].Files[0]
				if file.Filename != `Album\original.flac` || file.Destination != "Alice_Smith/Album/renamed.flac" || file.Size != 8 {
					t.Fatalf("changed remote request: %+v", file)
				}
			}
			next, _ = m.Update(msg)
			m = next.(model)
			if m.downloadAs != nil {
				t.Fatal("success retained dialog")
			}
			cmd = m.key(key("d"))
			if cmd == nil {
				t.Fatal("ordinary download changed")
			}
			_ = cmd()
			req = <-requests
			if source == "paged" {
				if req.path != "/v1/browse/download" || req.browse.Destination != "" {
					t.Fatal("ordinary browse renamed")
				}
			} else if req.downloads[0].Files[0].Destination != "" {
				t.Fatal("ordinary download renamed")
			}
			if source == "paged" {
				for _, name := range []string{"denied.flac", "old.flac"} {
					m.key(key("D"))
					m.downloadAs.name = name
					msg = m.key(key("enter"))().(downloadAsMsg)
					if msg.err == nil {
						t.Fatal("server rejection ignored")
					}
					<-requests
					next, _ = m.Update(msg)
					m = next.(model)
					if m.downloadAs == nil || m.downloadAs.pending || m.downloadAs.name != name || m.downloadAs.err == "" || len(requests) != 0 {
						t.Fatal("error lost input or fell back to an ordinary download")
					}
					m.key(key("esc"))
				}
			}
		})
	}
}

func TestDownloadAsRejectsFoldersAndMultipleSelections(t *testing.T) {
	m := model{workspace: workspaceSearch, results: []result{{user: "peer", path: `Album\a.flac`}, {user: "peer", path: `Album\b.flac`}}}
	m.searchTree, _ = buildSearchTree(m.results, treeState{}, 0)
	m.key(key("D")) // User/folder row.
	if m.downloadAs != nil {
		t.Fatal("folder accepted")
	}
	m.cursor = m.searchTree.cursorForSource(0)
	m.selected = map[int]bool{0: true, 1: true}
	m.key(key("D"))
	if m.downloadAs != nil {
		t.Fatal("multiple selections accepted")
	}
	m.selected = map[int]bool{0: true}
	m.key(key("D"))
	if m.downloadAs == nil {
		t.Fatal("one selected file rejected")
	}
}

func TestDownloadAsCaretAndRemoteControlCharacters(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	name := strings.Repeat("長", 70) + ".flac"
	m := model{width: 40, height: 24, downloadAs: &downloadAsForm{name: name, cursor: len([]rune(name))}}
	if view := m.downloadAsView(); !strings.Contains(view, ".flac█") {
		t.Fatalf("long filename hid the caret: %q", view)
	}
	m.key(key("home"))
	if view := m.downloadAsView(); !strings.Contains(view, "█長") {
		t.Fatalf("home did not scroll to caret: %q", view)
	}
	m.key(key("esc"))
	m.workspace = workspaceSearch
	m.results = []result{{user: "peer", path: "Album/bad\x1b[31m.flac"}}
	m.searchTree, _ = buildSearchTree(m.results, treeState{}, 0)
	m.cursor = m.searchTree.cursorForSource(0)
	m.openDownloadAs()
	if m.downloadAs == nil || m.downloadAs.name != "" || m.downloadAs.err == "" || strings.Contains(m.downloadAsView(), "\x1b") {
		t.Fatal("peer control characters reached the filename input")
	}
}

func TestFolderRenameDialogAndRequests(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	type request struct {
		daemon.FolderDownloadRequest
		Revision uint64 `json:"revision"`
		route    string
	}
	requests := make(chan request, 1)
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "ipc.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := request{route: r.URL.Path}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		requests <- req
		if strings.HasSuffix(req.Destination, "/Unsupported") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(daemon.BrowseDownloadResult{Queued: 1})
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	for _, source := range []string{"search", "saved", "paged"} {
		t.Run(source, func(t *testing.T) {
			m := model{ctx: context.Background(), client: ipc.NewClient(listener.Addr().String()), width: 80, height: 24, browseUser: "peer", browseRevision: 7}
			m.cfg.DownloadDir = "/downloads"
			if source == "search" {
				m.workspace = workspaceSearch
				m.results = []result{{user: "peer", path: `Music\Album\a.flac`, size: 3}, {user: "peer", path: `Music\Album\Disc\b.flac`, size: 8}}
				m.searchTree, _ = buildSearchTree(m.results, treeState{}, 0)
			} else {
				m.workspace = workspaceBrowse
				m.entries = []entry{{name: `Music\Album\a.flac`, size: 3}, {name: `Music\Album\Disc\b.flac`, size: 8}}
				m.browseTree, _ = buildBrowseTree(m.entries, "", treeState{}, 0)
				if source == "paged" {
					m.browsePaged = true
					m.entries = nil
					m.browseTree = treeState{nodes: []treeNode{{kind: treeFolder, id: remoteNodeID(1), path: `Music\Album`}}, visible: []int{0}}
				}
			}
			for cursor, index := range m.currentTree().visible {
				if m.currentTree().nodes[index].path == `Music\Album` {
					m.cursor = cursor
					break
				}
			}
			m.key(key("d"))
			if !m.folderMenu || m.folderMenuName != "Album" {
				t.Fatal("folder name not prefilled")
			}
			cmd := m.key(key("enter"))
			if cmd == nil {
				t.Fatal("ordinary folder download blocked")
			}
			if msg := cmd().(folderDownloadMsg); msg.err != nil {
				t.Fatal(msg.err)
			}
			req := <-requests
			if req.Destination != "" || (source == "paged" && req.route != "/v1/browse/download") || (source != "paged" && req.route != "/v1/folder-downloads") {
				t.Fatalf("ordinary download changed: %+v", req)
			}
			m.key(key("d"))
			m.key(key("n"))
			m.key(key("ctrl+u"))
			next, _ := m.Update(tea.PasteMsg{Content: "日本語"})
			m = next.(model)
			if m.folderMenuName != "日本語" || m.folderMenuDownloadDir != "/downloads" {
				t.Fatal("rename changed download root")
			}
			next, _ = m.Update(tea.PasteMsg{Content: "bad\nname"})
			m = next.(model)
			if m.folderMenuName != "日本語" || m.folderMenuError == "" {
				t.Fatal("control paste accepted")
			}
			m.key(key("enter")) // Finish editing, not queue.
			for _, name := range []string{"", "..", "../escape", `dir\name`} {
				m.folderMenuName = name
				if m.key(key("enter")) != nil || !m.folderMenu || m.folderMenuError == "" {
					t.Fatalf("invalid folder submitted: %q", name)
				}
			}
			m.folderMenuName, m.folderMenuError = "日本語", ""
			for _, width := range []int{40, 80} {
				m.width = width
				view := m.folderMenuView()
				if !strings.Contains(view, "Folder name") || !strings.Contains(view, "日本語") {
					t.Fatal("missing folder input")
				}
				for _, line := range strings.Split(view, "\n") {
					if lipgloss.Width(line) > width {
						t.Fatalf("folder dialog overflow: %q", line)
					}
				}
			}
			m.key(key("esc"))
			if m.folderMenu || len(requests) != 0 {
				t.Fatal("cancel submitted rename")
			}
			for _, recursive := range []bool{false, true} {
				m.key(key("d"))
				if m.folderMenuName != "Album" {
					t.Fatal("previous rename leaked into next dialog")
				}
				m.folderMenuName = "日本語"
				if recursive {
					m.key(key("down"))
				}
				revision := m.browseRevision
				m.browseRevision++ // Must use the revision when the dialog opened.
				cmd = m.key(key("enter"))
				if cmd == nil {
					t.Fatal("rename not submitted")
				}
				if msg := cmd().(folderDownloadMsg); msg.err != nil {
					t.Fatal(msg.err)
				}
				req = <-requests
				if req.Username != "peer" || req.Folder != `Music\Album` || req.Destination != "peer/Music/日本語" || req.DownloadDir != "/downloads" || req.Recursive != recursive {
					t.Fatalf("rename request: %+v", req)
				}
				if source == "paged" {
					if req.route != "/v1/browse/download-as" || req.Revision != revision || len(req.Files) != 0 {
						t.Fatal("paged rename lost revision or flattened files")
					}
				} else {
					want := map[string]uint64{`Music\Album\a.flac`: 3}
					if recursive {
						want[`Music\Album\Disc\b.flac`] = 8
					}
					if req.route != "/v1/folder-downloads/as" || len(req.Files) != len(want) {
						t.Fatalf("folder rename changed source: %+v", req)
					}
					for _, file := range req.Files {
						if want[file.Filename] != file.Size || file.Destination != "" {
							t.Fatalf("changed remote file: %+v", file)
						}
					}
				}
			}
			m.key(key("d"))
			m.folderMenuName = "Unsupported"
			msg := m.key(key("enter"))().(folderDownloadMsg)
			<-requests
			if msg.err == nil || len(requests) != 0 {
				t.Fatal("old daemon silently accepted rename or client fell back")
			}
		})
	}
}
