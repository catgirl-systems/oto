package tui

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/charmbracelet/x/ansi"
)

func TestBrowseFailureLifecycle(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := model{workspace: workspaceBrowse, width: 100, height: 24}
	update := func(msg tea.Msg) { next, _ := m.Update(msg); m = next.(model) }
	m.openBrowse("ijustlikemusic1020", "", false)
	request := m.browseTabs[0].request
	update(browseMsg{user: m.browseUser, request: request, err: errors.New("connection timed out")})
	assertFailure := func() {
		t.Helper()
		view := m.renderBrowse(96, 18)
		for _, want := range []string{"! Browse failed — ijustlikemusic1020", "connection timed out", "r retry browse", "/ browse another user", "(error)"} {
			failIfFmt(t, !strings.Contains(view, want), "missing %q:\n%s", want, view)
		}
		for _, unwanted := range []string{"Enter a Soulseek username", "No shared files", "No matching shared files", "unrelated error"} {
			failIfFmt(t, strings.Contains(view, unwanted), "unexpected %q:\n%s", unwanted, view)
		}
	}
	assertFailure()
	m.setNotice("unrelated notice")
	update(tickMsg(time.Now().Add(time.Hour)))
	update(statusMsg{})
	update(wishlistMsg{}) // clears the global error
	m.key(key("j"))
	assertFailure()
	update(wishlistMsg{err: errors.New("unrelated error")})
	m.switchWorkspace(workspaceSearch)
	m.switchWorkspace(workspaceBrowse)
	assertFailure()
	m.openBrowse("other", "", false)
	failIf(t, strings.Contains(m.renderBrowse(96, 18), "! Browse failed"), "error leaked into another user's pane")
	failIf(t, !strings.Contains(m.browseTabsLine(96), "ijustlikemusic1020 (error)"), "inactive failure has no marker")
	m.switchBrowseTab(-1)
	assertFailure()
	m.key(key("r"))
	if view := m.renderBrowse(96, 18); !strings.Contains(view, "Loading shared files") || strings.Contains(view, "Browse failed") {
		t.Fatalf("retry view:\n%s", view)
	}
	update(browseMsg{user: m.browseUser, request: request, err: errors.New("stale failure")})
	if heading, _ := m.browseFailure(); heading != "" || !m.loading {
		t.Fatal("stale reply changed retry")
	}
	request = m.browseTabs[0].request
	update(browseMsg{user: m.browseUser, request: request, err: errors.New("connection reset by peer")})
	failIf(t, !strings.Contains(m.renderBrowse(96, 18), "connection reset by peer"), "second failure missing")
	m.key(key("r"))
	update(browseMsg{user: m.browseUser, request: m.browseTabs[0].request, page: daemon.BrowsePage{Revision: 5}})
	if view := m.renderBrowse(96, 18); !strings.Contains(view, "No shared files.") || strings.Contains(view, "(error)") {
		t.Fatalf("successful empty share:\n%s", view)
	}
}

func TestBrowsePageFailurePersistence(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	tab := browseTab{user: "peer"}
	root := daemon.BrowsePage{Revision: 8, Entries: []daemon.BrowseEntry{{ID: 1, ShareEntry: soulseek.ShareEntry{Name: "Music", Directory: true}}}}
	applyBrowsePage(&tab, browsePageMsg{page: root})
	m := model{workspace: workspaceBrowse, browseTabs: []browseTab{tab}, width: 100, height: 24}
	m.loadBrowseTab(0)
	update := func(msg tea.Msg) { next, _ := m.Update(msg); m = next.(model) }
	m.requestRemotePage("Music", "", 0)
	failed := m.browsePages[browsePageKey("Music", "")].request
	m.openBrowse("other", "", false)
	update(browsePageMsg{user: "peer", folder: "Music", revision: 8, request: failed, err: errors.New("folder unavailable")})
	failIf(t, m.browseUser != "other", "background failure stole focus")
	m.switchBrowseTab(-1)
	view := m.renderBrowse(96, 18)
	for _, want := range []string{"Could not load browse page", "Music: folder unavailable", "FILE", "Music"} {
		failIfFmt(t, !strings.Contains(view, want), "page failure missing %q:\n%s", want, view)
	}
	m.requestRemotePage("", "", 1)
	request := m.browsePages[browsePageKey("", "")].request
	root.Cursor = 1
	update(browsePageMsg{user: "peer", revision: 8, request: request, page: root})
	if _, detail := m.browseFailure(); !strings.Contains(detail, "folder unavailable") {
		t.Fatal("unrelated parent page success erased failure")
	}
	m.requestRemotePage("", "jazz", 0)
	request = m.browsePages[browsePageKey("", "jazz")].request
	update(browsePageMsg{user: "peer", query: "jazz", revision: 8, request: request, err: errors.New("search page unavailable")})
	if _, detail := m.browseFailure(); !strings.Contains(detail, "search: jazz") {
		t.Fatal("newest page failure not selected")
	}
	m.editing, m.browseFindEditing, m.input = true, true, "rock"
	m.editKey(key("enter"))
	if _, detail := m.browseFailure(); !strings.Contains(detail, "search: jazz") {
		t.Fatal("changing filter erased failure")
	}
	m.requestRemotePage("", "jazz", 0)
	if _, detail := m.browseFailure(); !strings.Contains(detail, "folder unavailable") {
		t.Fatal("retrying newest failure erased older failure")
	}
	m.requestRemotePage("Music", "", 0)
	if heading, _ := m.browseFailure(); heading != "" {
		t.Fatal("page retry did not clear its failure")
	}
	update(browsePageMsg{user: "peer", folder: "Music", revision: 8, request: failed, err: errors.New("stale")})
	current := m.browsePages[browsePageKey("Music", "")].request
	update(browsePageMsg{user: "peer", folder: "Music", revision: 7, request: current, err: errors.New("old revision")})
	if heading, _ := m.browseFailure(); heading != "" {
		t.Fatal("stale page reply restored failure")
	}
	update(browsePageMsg{user: "peer", folder: "Music", revision: 8, request: current, page: daemon.BrowsePage{Revision: 8, Folder: "Music"}})
	failIf(t, !m.browsePages[browsePageKey("Music", "")].loaded, "successful retry not loaded")
}

func TestBrowseFailureRenderingBounds(t *testing.T) {
	for _, noColor := range []string{"1", ""} {
		t.Run("NO_COLOR="+noColor, func(t *testing.T) {
			t.Setenv("NO_COLOR", noColor)
			m := model{workspace: workspaceBrowse, browseTabs: []browseTab{{user: "peer", err: "\x1b[31mreset\x1b[0m\x07\r\n" + strings.Repeat("音楽é very-long-detail ", 80)}}}
			m.loadBrowseTab(0)
			failIfFmt(t, strings.IndexFunc(m.err, unicode.IsControl) >= 0, "footer error contains controls: %q", m.err)
			for _, size := range [][2]int{{96, 18}, {36, 6}, {20, 4}, {8, 2}, {1, 1}, {1, 4}} {
				width, height := size[0], size[1]
				heading, detail := m.browseFailure()
				view := strings.Join(browseErrorLines(heading, detail, width, height), "\n")
				assertBrowseBounds(t, view, width, height)
				failIfFmt(t, width >= 20 && !strings.Contains(view, "Browse failed"), "failure missing: %q", view)
				failIf(t, height >= 3 && !strings.Contains(view, "…"), "overflow not marked")
			}
			for _, size := range [][2]int{{96, 18}, {36, 6}, {32, 3}, {12, 1}} {
				view := m.renderBrowse(size[0], size[1])
				assertBrowseBounds(t, view, size[0], size[1])
			}
			m.width, m.height, m.err = 30, 5, "unrelated error"
			view := m.View().Content
			assertBrowseBounds(t, view, 30, 5)
			failIfFmt(t, !strings.Contains(view, "Browse failed") || strings.Contains(view, "unrelated error"), "compact failure: %q", view)
			t.Log("Compact failure:\n" + ansi.Strip(view))
			t.Log("Normal failure:\n" + ansi.Strip(m.renderBrowse(96, 18)))
			page := browsePageState{err: "\x1b]0;bad title\x07reason\t\x00", folder: "Music\x1b[2J\r", query: "jazz\x07"}
			_, detail := (browseTab{pages: map[string]browsePageState{"page": page}}).failure()
			failIfFmt(t, detail != "Music · search: jazz: reason", "unsafe context: %q", detail)
		})
	}
}

func assertBrowseBounds(t *testing.T, view string, width, height int) {
	t.Helper()
	plain := ansi.Strip(view)
	failIfFmt(t, len(strings.Split(plain, "\n")) > height, "height exceeds %d: %q", height, plain)
	for _, line := range strings.Split(plain, "\n") {
		failIfFmt(t, ansi.StringWidth(line) > width, "width exceeds %d: %q", width, line)
		failIfFmt(t, strings.IndexFunc(line, unicode.IsControl) >= 0, "control character: %q", line)
	}
	failIfFmt(t, !colorsEnabled() && strings.Contains(view, "\x1b"), "NO_COLOR contains escape: %q", view)
}

func TestBrowseSuccessStatesRemainDistinct(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, cached := range []bool{false, true} {
		tab := browseTab{user: "peer", filter: "missing"}
		applyBrowsePage(&tab, browsePageMsg{page: daemon.BrowsePage{Revision: 8, Query: "missing", Cached: cached}})
		m := model{workspace: workspaceBrowse, browseTabs: []browseTab{tab}}
		m.loadBrowseTab(0)
		view := m.renderBrowse(96, 18)
		failIfFmt(t, !strings.Contains(view, "No matching shared files") || strings.Contains(view, "(error)"), "success rendered as failure:\n%s", view)
		failIfFmt(t, strings.Contains(view, "(cached)") != cached, "cached distinction lost:\n%s", view)
	}
}
