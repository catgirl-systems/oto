package daemon

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func storeService() *Service { return &Service{searches: map[string]*storedSearch{}} }

func liveSearch(s *Service, id string, now time.Time) *storedSearch {
	_, cancel := context.WithCancel(context.Background())
	st := &storedSearch{Search: Search{ID: id, Query: "q", SearchContext: SearchContext{Scope: "global"}}, searching: true, cancel: cancel, done: make(chan struct{})}
	s.searchMu.Lock()
	s.storeSearchLocked(st, now)
	s.searchMu.Unlock()
	return st
}

func TestStreamingSearchKeepsServedPagesStable(t *testing.T) {
	s := storeService()
	st := liveSearch(s, "1", time.Now())
	// Before the first page, arrivals are ranked together: free slots first.
	s.appendSearchResults("1", []soulseek.SearchResult{{Username: "queued", Path: "a.flac", Size: 1}})
	s.appendSearchResults("1", []soulseek.SearchResult{{Username: "free", Path: "b.flac", Size: 1, SlotFree: true}})
	first, err := s.SearchPage("1", 0, "")
	must(t, err)
	failIfFmt(t, len(first.Results) != 2 || first.Results[0].Username != "free" || !first.Searching, "first page %+v", first)
	// After serving, a better late result is appended rather than reordering
	// what the client already holds.
	s.appendSearchResults("1", []soulseek.SearchResult{{Username: "late", Path: "c.flac", Size: 1, SlotFree: true}})
	more, err := s.SearchPage("1", len(first.Results), "")
	must(t, err)
	failIfFmt(t, len(more.Results) != 1 || more.Results[0].Username != "late" || more.FoundTotal != 3, "appended page %+v", more)
	again, _ := s.SearchPage("1", 0, "")
	failIfFmt(t, again.Results[0].Username != "free" || again.Results[1].Username != "queued", "served order changed: %+v", again.Results)

	// Cached filter views extend with new arrivals.
	flac, _ := s.SearchPage("1", 0, "type:mp3")
	failIf(t, flac.Total != 0, "unexpected mp3 match")
	s.appendSearchResults("1", []soulseek.SearchResult{{Username: "mp3", Path: "d.mp3", Size: 1}})
	mp3, _ := s.SearchPage("1", 0, "type:mp3")
	failIfFmt(t, mp3.Total != 1 || mp3.Results[0].Username != "mp3", "view did not extend: %+v", mp3)
	failIf(t, len(st.views) != 2, "views not cached per filter")

	s.finishSearch("1", nil)
	done, _ := s.SearchPage("1", 0, "")
	failIf(t, done.Searching, "finished search still searching")
	s.appendSearchResults("1", []soulseek.SearchResult{{Username: "after", Path: "e.flac"}})
	failIf(t, len(st.Results) != 4, "results accepted after the search finished")
}

func TestScopedSearchWithoutResultsExplainsMissingFallback(t *testing.T) {
	s := storeService()
	st := liveSearch(s, "1", time.Now())
	st.Scope = "buddies"
	s.finishSearch("1", nil)
	page, _ := s.SearchPage("1", 0, "")
	failIf(t, page.Warning == "", "empty scoped search lost its warning")
}

func TestSearchStoreBoundsMemory(t *testing.T) {
	s := storeService()
	start := time.Now()
	var first *storedSearch
	for i := range maxStoredSearches + 5 {
		st := liveSearch(s, fmt.Sprint(i), start.Add(time.Duration(i)*time.Second))
		if i == 0 {
			first = st
		}
	}
	s.searches[wishlistSearchID("w-1")] = &storedSearch{Search: Search{ID: wishlistSearchID("w-1")}, accessed: start.Add(-48 * time.Hour), done: closedDone()}
	liveSearch(s, "next", start.Add(time.Minute))
	ordinary := 0
	for id := range s.searches {
		if !isWishlistSearch(id) {
			ordinary++
		}
	}
	failIfFmt(t, ordinary != maxStoredSearches, "%d ordinary searches kept, want %d", ordinary, maxStoredSearches)
	failIf(t, s.searches["0"] != nil, "least recently used search survived")
	failIf(t, s.searches[wishlistSearchID("w-1")] == nil, "wishlist search was evicted")
	_ = first

	// Idle searches expire on the next store.
	liveSearch(s, "much later", start.Add(searchIdleTTL+2*time.Hour))
	failIfFmt(t, len(s.searches) != 2, "idle searches survived: %d", len(s.searches))

	// A search stops growing at maxSearchResults and stops collecting.
	ctx, cancel := context.WithCancel(context.Background())
	big := &storedSearch{Search: Search{ID: "big"}, searching: true, served: true, cancel: cancel, done: make(chan struct{})}
	s.searches["big"] = big
	batch := make([]soulseek.SearchResult, maxSearchResults+10)
	s.appendSearchResults("big", batch)
	failIfFmt(t, len(big.Results) != maxSearchResults || ctx.Err() == nil, "cap not enforced: %d results, collecting=%v", len(big.Results), ctx.Err() == nil)
}

func TestCloseSearchStopsCollection(t *testing.T) {
	s := storeService()
	ctx, cancel := context.WithCancel(context.Background())
	s.searches["1"] = &storedSearch{Search: Search{ID: "1"}, searching: true, cancel: cancel, done: make(chan struct{})}
	must(t, s.CloseSearch("1"))
	failIf(t, ctx.Err() == nil || s.searches["1"] != nil, "closed search kept collecting or stayed stored")
	failIf(t, s.CloseSearch("1") == nil, "closing an unknown search succeeded")
	s.searches[wishlistSearchID("w")] = &storedSearch{Search: Search{ID: wishlistSearchID("w")}, done: closedDone()}
	must(t, s.CloseSearch(wishlistSearchID("w")))
	failIf(t, s.searches[wishlistSearchID("w")] == nil, "wishlist search closed by a client")
}

func TestEmptyScopedWarningClearsWhenLateResultsArrive(t *testing.T) {
	s := storeService()
	st := liveSearch(s, "1", time.Now())
	st.Scope = "users"
	s.searchMu.Lock()
	warnIfEmptyScopedLocked(st) // the first page came back empty
	s.searchMu.Unlock()
	early, _ := s.SearchPage("1", 0, "")
	failIf(t, early.Warning == "" || !early.Searching, "empty first page of a scoped search had no warning")
	s.appendSearchResults("1", []soulseek.SearchResult{{Username: "late", Path: "x.flac"}})
	late, _ := s.SearchPage("1", 0, "")
	failIf(t, late.Warning != "" || late.Total != 1, "late results left the no-results warning up")
}
