package daemon

import (
	"context"
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

// Searches stream: results keep arriving for searchLifetime after the request,
// because peers that can only answer through an indirect connection reply
// late. The first page waits searchInitialWindow; clients see Searching and
// fetch the rest as it lands.
//
// Ordering keeps cursors stable. Results gathered before the first page is
// served are ranked together; later batches are ranked among themselves and
// appended, so a page a client already holds never shifts.
//
// Memory is bounded three ways: at most maxSearchResults per search, at most
// maxStoredSearches ordinary searches (least recently used evicted first,
// cancelling its collection), and searchIdleTTL without a page request.
// Wishlist searches follow their item's lifecycle instead.
const (
	searchLifetime      = 45 * time.Second
	searchInitialWindow = 3 * time.Second
	maxSearchResults    = 25000
	maxStoredSearches   = 24
	searchIdleTTL       = time.Hour
	maxSearchViews      = 8
)

// storedSearch is a search plus its collection state and cached filter views.
// All fields are guarded by Service.searchMu.
type storedSearch struct {
	Search
	searching bool
	served    bool
	cancel    context.CancelFunc
	accessed  time.Time
	views     map[string]*searchView
	done      chan struct{} // closed when collection ends
	err       error
}

// searchView caches the result indexes matching one filter expression and
// extends them as results are appended (#10: pages no longer re-filter the
// whole result set).
type searchView struct {
	filter  searchFilter
	indexes []int
	scanned int
	used    time.Time
}

func closedDone() chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

func isWishlistSearch(id string) bool { return strings.HasPrefix(id, wishlistSearchID("")) }

// storeSearchLocked records st, evicting expired and surplus ordinary
// searches. Caller holds searchMu.
func (s *Service) storeSearchLocked(st *storedSearch, now time.Time) {
	st.accessed = now
	s.searches[st.ID] = st
	var ordinary []*storedSearch
	for id, other := range s.searches {
		if isWishlistSearch(id) || other == st {
			continue
		}
		if now.Sub(other.accessed) > searchIdleTTL {
			s.dropSearchLocked(id)
			continue
		}
		ordinary = append(ordinary, other)
	}
	for len(ordinary) >= maxStoredSearches {
		oldest := 0
		for i, other := range ordinary {
			if other.accessed.Before(ordinary[oldest].accessed) {
				oldest = i
			}
		}
		s.dropSearchLocked(ordinary[oldest].ID)
		ordinary = append(ordinary[:oldest], ordinary[oldest+1:]...)
	}
}

func (s *Service) dropSearchLocked(id string) {
	if st := s.searches[id]; st != nil && st.cancel != nil {
		st.cancel()
	}
	delete(s.searches, id)
}

// CloseSearch forgets an ordinary search and stops collecting its results.
// Wishlist searches belong to their item and are left alone.
func (s *Service) CloseSearch(id string) error {
	if isWishlistSearch(id) {
		return nil
	}
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	if _, ok := s.searches[id]; !ok {
		return ErrSearchNotFound
	}
	s.dropSearchLocked(id)
	return nil
}

// appendSearchResults adds one response's matches to a live search.
func (s *Service) appendSearchResults(id string, batch []soulseek.SearchResult) {
	converted := fromSoulseekResults(batch)
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	st := s.searches[id]
	if st == nil || !st.searching {
		return
	}
	if room := maxSearchResults - len(st.Results); len(converted) > room {
		converted = converted[:max(0, room)]
		if st.cancel != nil {
			st.cancel() // full: stop collecting
		}
	}
	if len(converted) == 0 {
		return
	}
	if st.Warning == scopedNoResults {
		st.Warning = "" // late results disprove it
	}
	if st.served {
		sortSearchResults(converted)
		st.Results = append(st.Results, converted...)
		return
	}
	st.Results = append(st.Results, converted...)
	sortSearchResults(st.Results)
}

// scopedNoResults explains an empty scoped search. It shows as soon as the
// first page is empty and clears if results arrive later.
const scopedNoResults = "No results; scoped searches have no server acknowledgement, so support cannot be inferred. No global search was sent."

// warnIfEmptyScopedLocked sets scopedNoResults on an empty scoped search.
// Caller holds searchMu.
func warnIfEmptyScopedLocked(st *storedSearch) {
	if st.Scope != "global" && len(st.Results) == 0 && st.err == nil {
		st.Warning = scopedNoResults
	}
}

// finishSearch marks collection over. A scoped search that found nothing
// explains why no global fallback was sent.
func (s *Service) finishSearch(id string, err error) {
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	st := s.searches[id]
	if st == nil {
		return
	}
	st.searching, st.err = false, err
	warnIfEmptyScopedLocked(st)
	close(st.done)
}

// pageLocked serves one filtered page of a stored search through its cached
// view. Caller holds searchMu.
func (s *Service) pageLocked(st *storedSearch, expression string, filter searchFilter, cursor int, now time.Time) SearchPage {
	st.accessed, st.served = now, true
	view := st.views[expression]
	if view == nil {
		if st.views == nil {
			st.views = make(map[string]*searchView)
		}
		if len(st.views) >= maxSearchViews {
			var oldest string
			for key, other := range st.views {
				if oldest == "" || other.used.Before(st.views[oldest].used) {
					oldest = key
				}
			}
			delete(st.views, oldest)
		}
		view = &searchView{filter: filter}
		st.views[expression] = view
	}
	view.used = now
	for ; view.scanned < len(st.Results); view.scanned++ {
		if view.filter.matches(st.Results[view.scanned]) {
			view.indexes = append(view.indexes, view.scanned)
		}
	}
	cursor = max(0, min(cursor, len(view.indexes)))
	end := min(cursor+searchPageSize, len(view.indexes))
	results := make([]SearchResult, 0, end-cursor)
	for _, i := range view.indexes[cursor:end] {
		results = append(results, st.Results[i])
	}
	page := searchPageHeader(st.Search)
	page.Results, page.Cursor, page.Total, page.FoundTotal, page.Searching = results, cursor, len(view.indexes), len(st.Results), st.searching
	if end < len(view.indexes) {
		page.NextCursor = end
	}
	return page
}
