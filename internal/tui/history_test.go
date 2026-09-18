package tui

import (
	"reflect"
	"sync"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/storage"
)

func historySettings() config.Search {
	return config.Search{RememberSearches: true, SearchHistoryLimit: 200, RememberFilters: true, FilterHistoryLimit: 50}
}

func openHistoryDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.Open(t.TempDir() + "/state.sqlite3")
	must(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestHistoryPersistenceMergeLimitsAndClear(t *testing.T) {
	db := openHistoryDB(t)
	settings := historySettings()
	settings.SearchHistoryLimit, settings.FilterHistoryLimit = 2, 1
	var first, stale historyState
	must(t, first.record(db, " one ", false, settings))
	must(t, first.record(db, "two", false, settings))
	must(t, first.record(db, "one", false, settings))
	if got, err := loadHistory(db); err != nil || !reflect.DeepEqual(got.Searches, []string{"one", "two"}) {
		t.Fatalf("history = %#v, err=%v", got, err)
	}
	must(t, first.record(db, "three", false, settings))
	must(t, stale.record(db, "four", false, settings))
	if got, err := loadHistory(db); err != nil || !reflect.DeepEqual(got.Searches, []string{"four", "three"}) {
		t.Fatalf("latest-file merge/limit: %#v %v", got, err)
	}
	must(t, stale.record(db, "audio", true, settings))
	must(t, stale.record(db, "video", true, settings))
	must(t, first.clear(db, false, settings))
	if got, err := loadHistory(db); err != nil || len(got.Searches) != 0 || !reflect.DeepEqual(got.Filters, []string{"video"}) {
		t.Fatalf("clear = %#v, err=%v", got, err)
	}
	must(t, stale.record(db, "after clear", false, settings))
	got, err := loadHistory(db)
	if err != nil || !reflect.DeepEqual(got.Searches, []string{"after clear"}) {
		t.Fatalf("stale entries resurrected: %#v %v", got, err)
	}
}

func TestHistoryConcurrentConnectionsAndUnlimited(t *testing.T) {
	path := t.TempDir() + "/state.sqlite3"
	first, err := storage.Open(path)
	must(t, err)
	defer first.Close()
	second, err := storage.Open(path)
	must(t, err)
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
	failIfFmt(t, err != nil || len(got.Searches) != 20, "concurrent history: %d %v", len(got.Searches), err)
}

func TestHistoryCursorCyclesAndRestoresDraft(t *testing.T) {
	items := []string{"current", "newer", "older"}
	var cursor historyCursor
	cursor.reset("current")
	value, ok := cursor.move("current", items, true)
	failIfFmt(t, !ok || value != "newer", "newer: %q %v", value, ok)
	value, ok = cursor.move(value, items, true)
	failIfFmt(t, !ok || value != "older", "older: %q %v", value, ok)
	value, ok = cursor.move(value, items, false)
	failIfFmt(t, !ok || value != "newer", "back: %q %v", value, ok)
	value, ok = cursor.move(value, items, false)
	failIfFmt(t, !ok || value != "current", "draft: %q %v", value, ok)
}
