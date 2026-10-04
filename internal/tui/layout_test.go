package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/charmbracelet/x/ansi"
)

func layoutModel(width, height int) model {
	m := newModel(context.Background(), nil, "", false, config.Default())
	m.width, m.height = width, height
	m.community.summary.Unread = 5
	m.results = []result{{user: "peer", path: `a\b\c.flac`, size: 1 << 20}}
	m.searchTree, _ = buildSearchTree(m.results, treeState{}, 0)
	m.transfers = []transfer{{id: "1", filename: `a\b.flac`, direction: "download", state: "running", done: 3, total: 10, speed: 9}}
	m.transferTrees[transferDownloads], _ = buildTransferTree(m.transfers, "download", treeState{}, 0)
	return m
}

func TestEveryScreenFitsTheTerminal(t *testing.T) {
	for _, color := range []string{"", "1"} {
		t.Setenv("NO_COLOR", color)
		for width := 20; width <= 160; width += 7 {
			for _, height := range []int{6, 8, 12, 24, 40} {
				for ws := workspaceSearch; ws < workspaceCount; ws++ {
					m := layoutModel(width, height)
					m.switchWorkspace(ws)
					for _, help := range []bool{false, true} {
						m.help = help
						lines := strings.Split(m.View().Content, "\n")
						failIfFmt(t, len(lines) > height, "NO_COLOR=%q %dx%d workspace %d help=%v: %d lines", color, width, height, ws, help, len(lines))
						for _, line := range lines {
							failIfFmt(t, lipgloss.Width(line) > width, "NO_COLOR=%q %dx%d workspace %d help=%v: %q", color, width, height, ws, help, line)
						}
					}
				}
			}
		}
	}
}

func TestHeaderCollapsesToOneRowWhenWide(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	narrow, wide := layoutModel(100, 24), layoutModel(160, 24)
	failIf(t, len(narrow.headerLines()) != 2 || len(wide.headerLines()) != 1, "header rows did not follow width")
	failIf(t, !strings.Contains(wide.headerLines()[0], "[1 Search]") || !strings.Contains(wide.headerLines()[0], "5 Community 5"), "wide header lost tabs or badges")
}

func TestDialogsOverlayTheDimmedWorkspace(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	m := layoutModel(100, 24)
	m.statusMenu = true
	view := ansi.Strip(m.View().Content)
	failIf(t, !strings.Contains(view, "Soulseek status") || !strings.Contains(view, "Search"), "dialog did not keep the workspace visible behind it")
	t.Setenv("NO_COLOR", "1")
	failIf(t, strings.Contains(m.View().Content, "Wishlist"), "NO_COLOR dialog drew the workspace behind it")
}

func TestBracketKeysSwitchTabsEverywhere(t *testing.T) {
	m := layoutModel(100, 24)
	m.switchWorkspace(workspaceTransfers)
	m.key(key("]"))
	failIf(t, m.transferTab != transferUploads, "] did not open uploads")
	m.key(key("["))
	failIf(t, m.transferTab != transferDownloads, "[ did not open downloads")

	m.switchWorkspace(workspaceSettings)
	m.key(key("]"))
	failIf(t, m.settingsSection != settingsConnection, "] did not change settings section")
	m.key(key("h"))
	failIf(t, m.settingsSection != settingsAccount, "h did not change settings section")

	m.switchWorkspace(workspaceStats)
	m.key(key("]"))
	failIf(t, m.stats.page != 1, "] did not change stats page")
	m.key(key("<"))
	failIf(t, m.stats.edit != "from", "< did not edit the from date")
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))

	m.switchWorkspace(workspaceCommunity)
	m.key(key("]"))
	failIf(t, m.community.view != 1, "] did not change community view")
}

func TestListKeysAndMarks(t *testing.T) {
	m := layoutModel(100, 24)
	m.switchWorkspace(workspaceSearch)
	m.key(key("G"))
	failIf(t, m.cursor != m.rows()-1, "G did not jump to the last row")
	m.key(key("g"))
	failIf(t, m.cursor != 0, "g did not jump to the first row")
	m.key(key(" "))
	failIf(t, m.markedCount() == 0, "space did not mark")
	failIf(t, !strings.Contains(m.statusLine(), "marked"), "mark count not shown")
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	failIf(t, m.markedCount() != 0, "esc did not clear marks")

	m.switchWorkspace(workspaceWishlist)
	m.key(key("a"))
	failIf(t, !m.editing, "a did not start adding a wishlist item")
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	m.wishlist = []daemon.WishlistItem{{ID: "w"}}
	failIf(t, m.key(key("x")) == nil, "x did not remove the wishlist item")

	m.switchWorkspace(workspaceTransfers)
	m.cursor = m.transferTrees[transferDownloads].cursorForSource(0)
	failIf(t, m.key(key("x")) == nil, "x did not cancel the download")
	m.key(key("/"))
	failIfFmt(t, m.workspace != workspaceSearch || !m.editing || m.input != "b", "/ in Transfers did not prepare a search: %q", m.input)
}

func TestHelpOpensAtTheCurrentWorkspace(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := layoutModel(100, 40)
	m.switchWorkspace(workspaceTransfers)
	lines := m.helpLines(80)
	failIf(t, !strings.HasPrefix(lines[0], "TRANSFERS"), "help did not lead with the current workspace: "+lines[0])
	m.key(key("?"))
	m.key(key("q"))
	failIf(t, m.help, "q did not close help")
}

func TestSettingsGroupHeadersAdaptToHeight(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := layoutModel(100, 30)
	m.switchWorkspace(workspaceSettings)
	m.settingsSection = settingsUploads
	tall := m.View().Content
	for _, header := range []string{"SCHEDULING", "PRIORITY", "CLEANUP", "PER-USER QUOTAS", "SLOTS"} {
		failIfFmt(t, !strings.Contains(tall, header), "missing group header %q:\n%s", header, tall)
	}
	failIf(t, !strings.Contains(tall, "Upload slots"), "headers pushed fields out of a tall view")

	// Short views keep their headers and scroll to the cursor.
	m.settingsSection, m.height, m.cursor = settingsSearch, 18, 11
	short := m.View().Content
	failIf(t, !strings.Contains(short, "RESULTS") || !strings.Contains(short, "› Default result filter"), "short view lost headers or the cursor field:\n"+short)
	m.cursor = 0

	// A section that scrolls regardless keeps headers, and the cursor field's
	// header stays visible.
	m.settingsSection, m.width, m.height, m.cursor = settingsUploads, 60, 24, 5
	narrow := m.View().Content
	failIf(t, !strings.Contains(narrow, "CLEANUP") || !strings.Contains(narrow, "› Auto-clear new completed uploads"), "scrolling view lost the cursor's group header:\n"+narrow)
}
