package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
)

// TestFooterHintsGolden pins the footer hints of every workspace, so moving
// key handling and hints around cannot silently change what users see.
// Set OTO_UPDATE_GOLDEN=1 to rewrite testdata/footer_hints.golden.
func TestFooterHintsGolden(t *testing.T) {
	var out strings.Builder
	record := func(label string, m model) {
		fmt.Fprintf(&out, "%s: %s\n", label, strings.Join(m.footerHints(), " | "))
	}
	results := []result{
		{user: "alice", path: `Music\Album\01.flac`, size: 1 << 20},
		{user: "alice", path: `Music\Album\02.flac`, size: 1 << 20},
		{user: "bob", path: `Other\track.mp3`, size: 1 << 10},
	}
	entries := []entry{
		{name: `Music\Album\01.flac`, size: 1 << 20},
		{name: `Music\Album\02.flac`, size: 1 << 20},
	}
	base := model{cfg: config.Default()}

	for _, tabs := range []int{1, 2} {
		m := base
		m.workspace, m.results, m.query = workspaceSearch, results, "album"
		m.searchTabs = make([]searchTab, tabs)
		m.searchTree, _ = buildSearchTree(m.results, treeState{}, 0)
		for c := 0; c < len(m.searchTree.visible); c++ {
			m.cursor = c
			record(fmt.Sprintf("search tabs=%d cursor=%d", tabs, c), m)
		}
	}

	m := base
	m.workspace = workspaceBrowse
	record("browse saved list", m)
	for _, tabs := range []int{1, 2} {
		m := base
		m.workspace, m.entries, m.browseUser, m.browseLoaded = workspaceBrowse, entries, "alice", true
		m.browseTabs = make([]browseTab, tabs)
		m.browseTree, _ = buildBrowseTree(m.entries, "", treeState{}, 0)
		for c := 0; c < len(m.browseTree.visible); c++ {
			m.cursor = c
			record(fmt.Sprintf("browse tabs=%d cursor=%d", tabs, c), m)
		}
	}

	m = base
	m.workspace = workspaceWishlist
	record("wishlist", m)
	for tab := transferTab(0); tab < transferTabCount; tab++ {
		m := base
		m.workspace, m.transferTab = workspaceTransfers, tab
		record(fmt.Sprintf("transfers tab=%d", tab), m)
	}
	m = base
	m.workspace = workspaceShares
	record("shares empty", m)
	for page := range statsPages {
		m := base
		m.workspace, m.stats.page = workspaceStats, page
		record(fmt.Sprintf("stats page=%d", page), m)
	}
	for section := settingsSection(0); section < settingsSectionCount; section++ {
		m := base
		m.workspace, m.settingsSection = workspaceSettings, section
		for c := range m.settingFields() {
			m.cursor = c
			record(fmt.Sprintf("settings section=%d cursor=%d", section, c), m)
		}
	}
	m = base
	m.workspace, m.shareExclusions.open = workspaceSettings, true
	record("settings share exclusions", m)
	for _, ws := range []workspace{workspaceSearch, workspaceBrowse, workspaceWishlist, workspaceShares} {
		m := base
		m.workspace, m.editing = ws, true
		record(fmt.Sprintf("editing workspace=%d", ws), m)
		m.filterEditing = true
		record(fmt.Sprintf("editing filter workspace=%d", ws), m)
	}
	m = base
	m.choiceChoosing = true
	record("choice", m)
	m = base
	m.stats.prune = true
	record("stats prune", m)

	path := filepath.Join("testdata", "footer_hints.golden")
	if os.Getenv("OTO_UPDATE_GOLDEN") != "" {
		must(t, os.WriteFile(path, []byte(out.String()), 0o644))
	}
	want, err := os.ReadFile(path)
	must(t, err)
	if got := out.String(); got != string(want) {
		t.Fatalf("footer hints changed; rerun with OTO_UPDATE_GOLDEN=1 if intended\n--- got\n%s", got)
	}
}
