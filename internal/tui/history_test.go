package tui

import (
	"context"
	"reflect"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/storage"
)

func historySettings() config.Search {
	return config.Search{RememberSearches: true, SearchHistoryLimit: 200, RememberFilters: true, FilterHistoryLimit: 50}
}

func openHistoryDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.Open(t.TempDir() + "/state.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestHistoryPersistenceMergeLimitsAndClear(t *testing.T) {
	db := openHistoryDB(t)
	settings := historySettings()
	settings.SearchHistoryLimit, settings.FilterHistoryLimit = 2, 1
	var first, stale historyState
	if err := first.record(db, " one ", false, settings); err != nil {
		t.Fatal(err)
	}
	if err := first.record(db, "two", false, settings); err != nil {
		t.Fatal(err)
	}
	if err := first.record(db, "one", false, settings); err != nil {
		t.Fatal(err)
	}
	if got, err := loadHistory(db); err != nil || !reflect.DeepEqual(got.Searches, []string{"one", "two"}) {
		t.Fatalf("history = %#v, err=%v", got, err)
	}
	if err := first.record(db, "three", false, settings); err != nil {
		t.Fatal(err)
	}
	if err := stale.record(db, "four", false, settings); err != nil {
		t.Fatal(err)
	}
	if got, err := loadHistory(db); err != nil || !reflect.DeepEqual(got.Searches, []string{"four", "three"}) {
		t.Fatalf("latest-file merge/limit: %#v %v", got, err)
	}
	if err := stale.record(db, "audio", true, settings); err != nil {
		t.Fatal(err)
	}
	if err := stale.record(db, "video", true, settings); err != nil {
		t.Fatal(err)
	}
	if err := first.clear(db, false, settings); err != nil {
		t.Fatal(err)
	}
	if got, err := loadHistory(db); err != nil || len(got.Searches) != 0 || !reflect.DeepEqual(got.Filters, []string{"video"}) {
		t.Fatalf("clear = %#v, err=%v", got, err)
	}
	if err := stale.record(db, "after clear", false, settings); err != nil {
		t.Fatal(err)
	}
	got, err := loadHistory(db)
	if err != nil || !reflect.DeepEqual(got.Searches, []string{"after clear"}) {
		t.Fatalf("stale entries resurrected: %#v %v", got, err)
	}
}

func TestHistoryConcurrentConnectionsAndUnlimited(t *testing.T) {
	path := t.TempDir() + "/state.sqlite3"
	first, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	settings := historySettings()
	settings.SearchHistoryLimit = 0
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h := historyState{}
			db := first
			if i%2 != 0 {
				db = second
			}
			if err := h.record(db, "value-"+string(rune('a'+i)), false, settings); err != nil {
				t.Errorf("record: %v", err)
			}
		}(i)
	}
	wg.Wait()
	got, err := loadHistory(first)
	if err != nil || len(got.Searches) != 20 {
		t.Fatalf("concurrent history: %d %v", len(got.Searches), err)
	}
}

func TestHistoryMemoryErrorsDoNotBlockUse(t *testing.T) {
	settings := historySettings()
	var history historyState
	if err := history.record(nil, " in memory ", false, settings); err != nil || !reflect.DeepEqual(history.Searches, []string{"in memory"}) {
		t.Fatalf("memory history: %#v %v", history, err)
	}
	if err := history.clear(nil, false, settings); err != nil || len(history.Searches) != 0 {
		t.Fatalf("memory clear: %#v %v", history, err)
	}
}

func TestHistoryCursorCyclesAndRestoresDraft(t *testing.T) {
	items := []string{"current", "newer", "older"}
	var cursor historyCursor
	cursor.reset("current")
	value, ok := cursor.move("current", items, true)
	if !ok || value != "newer" {
		t.Fatalf("newer: %q %v", value, ok)
	}
	value, ok = cursor.move(value, items, true)
	if !ok || value != "older" {
		t.Fatalf("older: %q %v", value, ok)
	}
	value, ok = cursor.move(value, items, false)
	if !ok || value != "newer" {
		t.Fatalf("back: %q %v", value, ok)
	}
	value, ok = cursor.move(value, items, false)
	if !ok || value != "current" {
		t.Fatalf("draft: %q %v", value, ok)
	}
}

func TestModelRecordsIndependentHistories(t *testing.T) {
	cfg := config.Default()
	m := newModel(context.Background(), nil, "", false, cfg)
	m.stateDB = openHistoryDB(t)
	m.workspace, m.query = workspaceSearch, "current"
	m.beginEdit()
	m.editKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	m.editing = false
	m.activeSearch.RememberFilters = false
	m.openSearch("not stored")
	if m.historyErr != "" {
		t.Fatal(m.historyErr)
	}
}
