package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestTransferSearchDraft(t *testing.T) {
	for _, direction := range []string{"download", "upload"} {
		for _, tc := range []struct {
			path, key, want string
			folder          bool
		}{
			{`Music\été Album/song.live.flac`, "s", "song.live", false},
			{`Music\été Album/song.live.flac`, "S", "été Album", false},
			{`Music\été Album/song.live.flac`, "s", "été Album", true},
			{`Music\été Album/song.live.flac`, "S", "été Album", true},
			{"README", "s", "README", false}, {".track", "s", ".track", false},
			{"README", "S", "", false},
			{"Album/song\tname.flac", "s", "song name", false},
		} {
			t.Run(direction+tc.path+tc.key, func(t *testing.T) {
				m := model{workspace: workspaceTransfers, selected: map[int]bool{}, acceptedSearchDefault: "type:audio"}
				if direction == "upload" {
					m.transferTab = transferUploads
				}
				m.transfers = []transfer{{id: "d-1", user: "peer", filename: tc.path, direction: direction, state: "failed"}}
				tree := &m.transferTrees[m.transferTab]
				*tree, _ = buildTransferTree(m.transfers, direction, treeState{}, 0)
				m.cursor = tree.cursorForSource(0)
				if tc.folder {
					m.cursor = tree.left(m.cursor)
				}
				if cmd := m.key(key(tc.key)); cmd != nil {
					t.Fatal("prefill started network work")
				}
				if tc.want == "" {
					if m.workspace != workspaceTransfers || m.notice == "" {
						t.Fatal("missing parent was not rejected")
					}
					return
				}
				if m.workspace != workspaceSearch || !m.editing || m.input != tc.want || len(m.searchTabs) != 0 {
					t.Fatalf("draft: %+v", m)
				}
				if cmd := m.editKey(key("enter")); cmd == nil || len(m.searchTabs) != 1 || m.searchTabs[0].filter != "type:audio" {
					t.Fatal("Enter did not submit with default filter")
				}
				// Existing results must survive a second prefill and Escape.
				m.workspace, m.cursor = workspaceTransfers, tree.cursorForSource(0)
				m.key(key("s"))
				m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
				if len(m.searchTabs) != 1 || m.editing {
					t.Fatal("Escape changed result tabs")
				}
			})
		}
	}
	for _, empty := range []bool{false, true} {
		m := model{workspace: workspaceTransfers}
		if !empty {
			m.transferTrees[0], _ = buildTransferTree([]transfer{{id: "d-1", user: "peer", filename: "song", direction: "download"}}, "download", treeState{}, 0)
		}
		if cmd := m.key(key("s")); cmd != nil || m.workspace != workspaceTransfers || m.notice == "" {
			t.Fatal("empty/user row should only show notice")
		}
	}
}

func TestSavedDefaultFilters(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Search.DefaultFilter = "type:audio"
	m := newModel(context.Background(), nil, "", false, cfg)
	m.openSearch("one")
	m.searchFilter = "free:true"
	m.cfg.Search.DefaultFilter = "type:video" // unsaved draft
	m.openSearch("two")
	if m.searchTabs[0].filter != "free:true" || m.searchTabs[1].filter != "type:audio" {
		t.Fatal("tab filters leaked or draft applied")
	}
	updated, _ := m.Update(settingsMsg{search: m.cfg.Search, err: errors.New("save failed")})
	m = updated.(model)
	m.openSearch("three")
	if m.searchFilter != "type:audio" {
		t.Fatal("failed save changed default")
	}
	updated, _ = m.Update(settingsMsg{search: m.cfg.Search})
	m = updated.(model)
	m.openSearch("four")
	if m.searchFilter != "type:video" {
		t.Fatal("saved default not applied")
	}
	m.openSearchPage("wish", "", daemon.SearchPage{ID: "wish"})
	if m.searchFilter != "" {
		t.Fatal("empty wishlist filter inherited default")
	}
	for _, filter := range []string{"", "country:US"} {
		fresh := newModel(context.Background(), nil, "", false, cfg)
		fresh.key(key("f"))
		fresh.input = filter
		fresh.editKey(key("enter"))
		fresh.openSearch("draft")
		if fresh.searchFilter != filter {
			t.Fatalf("explicit draft %q lost", filter)
		}
		fresh.openSearch("next")
		if fresh.searchFilter != "type:audio" {
			t.Fatal("draft persisted beyond one search")
		}
	}
	// The attached daemon, not a local config file, owns the accepted default.
	updated, _ = m.Update(statusMsg{snapshot: daemon.Snapshot{Config: cfg.Redacted()}})
	m = updated.(model)
	m.openSearch("attached")
	if m.searchFilter != "type:audio" {
		t.Fatal("daemon default not used")
	}
	m.workspace, m.settingsSection = workspaceSettings, settingsSearch
	for i, field := range m.settingFields() {
		if field.id == settingDefaultFilter {
			m.cursor = i
		}
	}
	m.beginEdit()
	m.input = "type:"
	m.editKey(key("tab"))
	if !m.filterEditing || m.input == "type:" {
		t.Fatal("default editor lacks completion")
	}
	before := m.cfg.Search.DefaultFilter
	m.input = "in:["
	m.editKey(key("enter"))
	if m.err == "" || m.cfg.Search.DefaultFilter != before {
		t.Fatal("invalid default accepted")
	}
}

func TestScanAndCompletionSignals(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := config.Default()
	cfg.Shares = []config.Share{{Name: "Music", Path: "/music"}}
	m := model{width: 100, height: 20, workspace: workspaceShares, cfg: cfg}
	snap := daemon.Snapshot{Config: cfg.Redacted(), Shares: cfg.Shares, ShareIndexRevision: 1, DownloadNotification: daemon.DownloadNotification{SessionID: "session", Sequence: 5}}
	updated, cmd := m.Update(statusMsg{snapshot: snap})
	m = updated.(model)
	if cmd != nil || m.notice != "" {
		t.Fatal("initial attach replayed notification")
	}
	root := m.shareTree.nodes[0]
	m.shareTree.add("child", root.id, "song", `Music\song`, "", "", treeFile, 0)
	snap.ShareScan = &daemon.ShareScan{State: "scanning", Root: "Music", Files: 12, Directories: 3, ElapsedMS: 3000}
	updated, _ = m.Update(statusMsg{snapshot: snap})
	m = updated.(model)
	if len(m.shareTree.nodes) != 2 || !strings.Contains(m.renderShares(100, 12), "3s") || !strings.Contains(m.footerView(), "Scanning shares") {
		t.Fatal("scan status/in-progress index incorrect")
	}
	m.searchTabs = []searchTab{{query: "interactive", searching: true, operation: 1}}
	if !strings.Contains(m.footerView(), "interactive") {
		t.Fatal("background scan hid interactive activity")
	}
	m.searchTabs = nil
	snap.ShareIndexRevision = 2
	snap.ShareScan.State = "completed"
	snap.DownloadNotification.Sequence = 8
	snap.DownloadNotification.Message = "Album downloaded"
	updated, cmd = m.Update(statusMsg{snapshot: snap})
	m = updated.(model)
	if cmd == nil || m.notice != "Album downloaded" || len(m.shareTree.nodes) != 1 {
		t.Fatal("publication/notification not reflected")
	}
	updated, cmd = m.Update(statusMsg{snapshot: snap})
	m = updated.(model)
	if cmd != nil {
		t.Fatal("repeated poll rang again")
	}
	snap.DownloadNotification.SessionID = "new-session"
	snap.DownloadNotification.Sequence = 1
	updated, cmd = m.Update(statusMsg{snapshot: snap})
	m = updated.(model)
	if cmd != nil {
		t.Fatal("daemon restart replayed old completion")
	}
	for _, width := range []int{20, 40, 100} {
		m.width = width
		if strings.Contains(m.View().Content, "\x1b[") {
			t.Fatal("NO_COLOR styling")
		}
	}
	m.workspace, m.settingsSection = workspaceSettings, settingsDownloads
	for i, f := range m.settingFields() {
		if f.id == settingFileNotifications {
			m.cursor = i
		}
	}
	m.key(key("enter"))
	if !m.cfg.Downloads.FileNotifications {
		t.Fatal("file notification toggle failed")
	}
}
