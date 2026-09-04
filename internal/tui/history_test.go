package tui

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/config"
)

func historySettings() config.Search {
	return config.Search{RememberSearches: true, SearchHistoryLimit: 200, RememberFilters: true, FilterHistoryLimit: 50}
}

func TestHistoryPersistenceMergeLimitsAndClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "history.json")
	settings := historySettings()
	settings.SearchHistoryLimit = 2
	settings.FilterHistoryLimit = 1
	var first historyState
	if err := first.record(path, " one ", false, settings); err != nil {
		t.Fatal(err)
	}
	if err := first.record(path, "two", false, settings); err != nil {
		t.Fatal(err)
	}
	if err := first.record(path, "one", false, settings); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Searches, []string{"one", "two"}) {
		t.Fatalf("duplicate was not moved to newest: %#v", first.Searches)
	}
	stale := first
	if err := first.record(path, "three", false, settings); err != nil {
		t.Fatal(err)
	}
	if err := stale.record(path, "four", false, settings); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stale.Searches, []string{"four", "three"}) {
		t.Fatalf("latest-file merge/limit: %#v", stale.Searches)
	}
	if err := stale.record(path, "audio", true, settings); err != nil {
		t.Fatal(err)
	}
	if err := stale.record(path, "video", true, settings); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stale.Filters, []string{"video"}) {
		t.Fatalf("independent filter limit: %#v", stale.Filters)
	}
	if err := first.clear(path, false, settings); err != nil {
		t.Fatal(err)
	}
	if len(first.Searches) != 0 || !reflect.DeepEqual(first.Filters, []string{"video"}) {
		t.Fatalf("clear did not preserve filters: %#v", first)
	}
	if err := stale.record(path, "after clear", false, settings); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stale.Searches, []string{"after clear"}) {
		t.Fatalf("stale entries resurrected: %#v", stale.Searches)
	}

	if stat, err := os.Stat(path); err != nil || stat.Mode().Perm() != 0600 {
		t.Fatalf("history mode: %v %v", stat, err)
	}
	if stat, err := os.Stat(filepath.Dir(path)); err != nil || stat.Mode().Perm() != 0700 {
		t.Fatalf("history dir mode: %v %v", stat, err)
	}
}

func TestHistoryNormalizationUnlimitedAndMalformedRepair(t *testing.T) {
	items := normalizeHistory([]string{" one ", "two", "one", "", "three"}, 2)
	if !reflect.DeepEqual(items, []string{"one", "two"}) {
		t.Fatalf("normalized history: %#v", items)
	}
	items = normalizeHistory([]string{"one", "two", "three"}, 0)
	if len(items) != 3 {
		t.Fatalf("unlimited history truncated: %#v", items)
	}

	path := filepath.Join(t.TempDir(), "history.json")
	if history, err := loadHistory(path); err != nil || len(history.Searches) != 0 {
		t.Fatalf("missing history: %#v %v", history, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	history := historyState{Filters: []string{"kept"}}
	if err := history.record(path, "in memory", false, historySettings()); err == nil || !reflect.DeepEqual(history.Searches, []string{"in memory"}) {
		t.Fatalf("malformed record should remain usable in memory: %#v %v", history, err)
	}
	if err := history.clear(path, false, historySettings()); err != nil {
		t.Fatalf("explicit clear should repair malformed state: %v", err)
	}
	repaired, err := loadHistory(path)
	if err != nil || len(repaired.Searches) != 0 || !reflect.DeepEqual(repaired.Filters, []string{"kept"}) {
		t.Fatalf("repaired history: %#v %v", repaired, err)
	}
}

func TestHistoryCursorCyclesAndRestoresDraft(t *testing.T) {
	items := []string{"current", "newer", "older"}
	var cursor historyCursor
	cursor.reset("current")
	value, ok := cursor.move("current", items, true)
	if !ok || value != "newer" {
		t.Fatalf("duplicate was not skipped: %q %v", value, ok)
	}
	value, ok = cursor.move(value, items, true)
	if !ok || value != "older" {
		t.Fatalf("older recall: %q %v", value, ok)
	}
	value, ok = cursor.move(value, items, false)
	if !ok || value != "newer" {
		t.Fatalf("newer recall: %q %v", value, ok)
	}
	value, ok = cursor.move(value, items, false)
	if !ok || value != "current" {
		t.Fatalf("draft restore: %q %v", value, ok)
	}
	cursor.reset("edited")
	value, ok = cursor.move("edited", items, true)
	if !ok || value != "current" {
		t.Fatalf("edited draft did not restart recall: %q %v", value, ok)
	}
}

func TestModelRecordsAndRecallsIndependentHistories(t *testing.T) {
	cfg := config.Default()
	m := newModel(context.Background(), nil, "", false, cfg)
	m.historyPath = filepath.Join(t.TempDir(), "history.json")
	m.history = historyState{Searches: []string{"current", "newer", "older"}, Filters: []string{"type:audio", "free:true"}}
	m.workspace, m.query = workspaceSearch, "current"
	m.beginEdit()
	m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if m.input != "newer" {
		t.Fatalf("search recall did not skip current value: %q", m.input)
	}
	m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	if m.input != "current" {
		t.Fatalf("search draft not restored: %q", m.input)
	}
	m.editKey(key("x"))
	m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if m.input != "current" {
		t.Fatalf("editing did not restart recall: %q", m.input)
	}

	m.editing = false
	m.key(key("f"))
	m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if m.input != "type:audio" {
		t.Fatalf("filter history was not independent: %q", m.input)
	}

	m.editing = false
	m.activeSearch.RememberFilters = false
	m.key(key("f"))
	m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if m.input != m.searchFilter {
		t.Fatalf("disabled filter history recalled %q", m.input)
	}
	m.activeSearch.RememberSearches = false
	before := append([]string(nil), m.history.Searches...)
	m.openSearch("not stored")
	if !reflect.DeepEqual(m.history.Searches, before) {
		t.Fatalf("disabled search history recorded an entry: %#v", m.history.Searches)
	}
}

func TestModelRecordsValidSubmissionsAndSurfacesHistoryErrors(t *testing.T) {
	cfg := config.Default()
	m := newModel(context.Background(), nil, "", false, cfg)
	m.historyPath = filepath.Join(t.TempDir(), "history.json")
	if cmd := m.openSearch("  Mixed Case  "); cmd == nil {
		t.Fatal("search did not launch")
	}
	m.editing, m.filterEditing, m.input = true, true, "type:audio"
	m.editKey(key("enter"))
	m.editing, m.filterEditing, m.input = true, true, "size:nope"
	m.editKey(key("enter"))
	if !reflect.DeepEqual(m.history.Searches, []string{"Mixed Case"}) || !reflect.DeepEqual(m.history.Filters, []string{"type:audio"}) {
		t.Fatalf("submission history: %#v", m.history)
	}

	if err := os.WriteFile(m.historyPath, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	m.err = ""
	m.initializeHistory()
	if m.historyErr == "" || !strings.Contains(m.errorText(), "history") {
		t.Fatal("malformed history warning was not durable")
	}
	m.openSearch("still works")
	if !reflect.DeepEqual(m.history.Searches, []string{"still works"}) || m.historyErr == "" {
		t.Fatalf("malformed history blocked in-memory search or lost warning: %#v %q", m.history, m.historyErr)
	}
	m.clearHistory(false)
	if m.historyErr != "" || m.notice != "search history cleared" {
		t.Fatalf("clear did not repair history: error=%q notice=%q", m.historyErr, m.notice)
	}
}

func TestHistoryReloadsForNewTUI(t *testing.T) {
	cfg := config.Default()
	path := filepath.Join(t.TempDir(), "history.json")
	first := newModel(context.Background(), nil, "", false, cfg)
	first.historyPath = path
	first.openSearch("restart search")
	first.editing, first.filterEditing, first.input = true, true, "free:true"
	first.editKey(key("enter"))

	second := newModel(context.Background(), nil, "", false, cfg)
	second.historyPath = path
	second.initializeHistory()
	second.workspace = workspaceSearch
	second.beginEdit()
	second.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if second.input != "restart search" {
		t.Fatalf("search history did not reload: %q", second.input)
	}
	second.editing = false
	second.key(key("f"))
	second.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if second.input != "free:true" {
		t.Fatalf("filter history did not reload: %q", second.input)
	}
}

func TestTypedSearchSettingsStageApplyTrimAndClear(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := config.Default()
	m := newModel(context.Background(), nil, "", false, cfg)
	m.historyPath = filepath.Join(t.TempDir(), "history.json")
	m.history = historyState{Searches: []string{"three", "two", "one"}, Filters: []string{"audio"}}
	if err := config.SaveJSON(m.historyPath, m.history); err != nil {
		t.Fatal(err)
	}
	m.workspace, m.settingsSection, m.cursor = workspaceSettings, settingsSearch, 3
	m.key(key("enter"))
	if m.cfg.Search.RememberSearches || !m.activeSearch.RememberSearches {
		t.Fatal("boolean setting did not remain staged")
	}
	m.cursor = 4
	m.key(key("enter"))
	m.input = "1"
	m.editKey(key("enter"))
	if m.cfg.Search.SearchHistoryLimit != 1 {
		t.Fatal("integer setting was not edited")
	}
	updated, _ := m.Update(settingsMsg{search: m.cfg.Search})
	m = updated.(model)
	if m.activeSearch != m.cfg.Search || !reflect.DeepEqual(m.history.Searches, []string{"three"}) {
		t.Fatalf("saved settings were not activated and trimmed: active=%+v history=%#v", m.activeSearch, m.history.Searches)
	}

	m.cursor = 4
	m.key(key("enter"))
	m.input = "0"
	m.editKey(key("enter"))
	if m.cfg.Search.SearchHistoryLimit != 0 || !strings.Contains(m.renderSettings(80, 10), "Unlimited") {
		t.Fatal("zero limit was not shown as unlimited")
	}
	m.key(key("enter"))
	m.input = "-1"
	m.editKey(key("enter"))
	if m.err == "" {
		t.Fatal("negative history limit was accepted")
	}

	m.cursor = 9
	if !strings.Contains(m.renderSettings(80, 4), "Clear search history") {
		t.Fatal("selected setting row was not scrolled into view")
	}
	m.key(key("/"))
	if m.editing {
		t.Fatal("action setting opened a text editor")
	}
	m.key(key("enter"))
	if len(m.history.Searches) != 0 || m.notice != "search history cleared" {
		t.Fatal("search clear action failed")
	}
	m.cursor = 10
	m.key(key("enter"))
	if len(m.history.Filters) != 0 || m.notice != "filter history cleared" {
		t.Fatal("filter clear action failed")
	}
}

func TestIncomingSearchSettingsStageRenderAndValidate(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := config.Default()
	m := newModel(context.Background(), nil, "", false, cfg)
	m.workspace, m.settingsSection = workspaceSettings, settingsSearch

	view := m.renderSettings(100, 8)
	if !strings.Contains(view, "Respond to incoming searches") || !strings.Contains(view, "Minimum incoming search length") {
		t.Fatalf("incoming search settings missing: %s", view)
	}
	m.cursor = 0
	m.key(key("enter"))
	if m.cfg.Search.RespondToIncomingSearches || !m.activeSearch.RespondToIncomingSearches {
		t.Fatal("incoming response toggle did not remain staged")
	}
	m.cursor = 1
	if err := m.setSettingValue("0"); err != nil || !strings.Contains(m.renderSettings(100, 5), "No minimum") {
		t.Fatalf("zero minimum: %v", err)
	}
	if err := m.setSettingValue("51"); err == nil {
		t.Fatal("oversized incoming minimum accepted")
	}
	m.cursor = 2
	if err := m.setSettingValue("49"); err == nil {
		t.Fatal("small incoming maximum accepted")
	}
	if err := m.setSettingValue("10001"); err == nil {
		t.Fatal("oversized incoming maximum accepted")
	}
	if err := m.setSettingValue("500"); err != nil || m.cfg.Search.MaximumIncomingSearchResults != 500 {
		t.Fatalf("valid incoming maximum: %d %v", m.cfg.Search.MaximumIncomingSearchResults, err)
	}
	updated, _ := m.Update(settingsMsg{search: m.cfg.Search})
	if updated.(model).activeSearch != m.cfg.Search {
		t.Fatal("saved incoming search settings were not activated")
	}
}
