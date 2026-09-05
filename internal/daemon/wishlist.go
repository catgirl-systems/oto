package daemon

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

var (
	ErrWishlistNotFound  = errors.New("daemon: wishlist item not found")
	ErrWishlistNoResults = errors.New("daemon: wishlist item has no cached results")
)

type WishlistItem struct {
	ID                       string    `json:"id"`
	Query                    string    `json:"query"`
	Filter                   string    `json:"filter,omitempty"`
	AddedAt                  time.Time `json:"added_at"`
	LastRunAt                time.Time `json:"last_run_at,omitempty"`
	ResultCount              int       `json:"result_count"`
	Running                  bool      `json:"running"`
	Error                    string    `json:"error,omitempty"`
	Unread                   bool      `json:"unread"`
	NotificationSequence     uint64    `json:"notification_sequence"`
	EffectiveIntervalSeconds int64     `json:"effective_interval_seconds"`
	AutomaticAvailable       bool      `json:"automatic_available"`
}

type wishlistEntry struct {
	WishlistItem
	ResultSignature string `json:"result_signature,omitempty"`
	generation      uint64
}

func (s *Service) wishlistIntervalLocked() (time.Duration, bool) {
	if s.cfg.Search.WishlistIntervalMinutes == 0 || s.client == nil || s.wishlistServerInterval <= 0 || len(s.wishlist) == 0 {
		return 0, false
	}
	configured := time.Duration(s.cfg.Search.WishlistIntervalMinutes) * time.Minute
	return max(configured, s.wishlistServerInterval), true
}

func (s *Service) Wishlist() []WishlistItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	interval, available := s.wishlistIntervalLocked()
	out := make([]WishlistItem, len(s.wishlist))
	for i := range s.wishlist {
		out[i] = s.wishlist[i].WishlistItem
		out[i].EffectiveIntervalSeconds = int64(interval / time.Second)
		out[i].AutomaticAvailable = available
	}
	return out
}

func (s *Service) PutWishlist(query, expression string) (WishlistItem, error) {
	query, expression = strings.TrimSpace(query), strings.TrimSpace(expression)
	if query == "" {
		return WishlistItem{}, errors.New("daemon: wishlist query is required")
	}
	filter, err := parseSearchFilter(expression)
	if err != nil {
		return WishlistItem{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if i := slices.IndexFunc(s.wishlist, func(item wishlistEntry) bool { return item.Query == query }); i >= 0 {
		item := &s.wishlist[i]
		previous := *item
		item.Filter, item.generation = expression, item.generation+1
		item.Running, item.Unread, item.ResultSignature = false, false, ""
		if search, ok := s.searches[wishlistSearchID(item.ID)]; ok {
			matching := matchingSearchResults(search.Results, filter)
			item.ResultCount, item.ResultSignature = len(matching), searchResultSignature(matching)
		}
		if err := s.persistWishlistLocked(item, false); err != nil {
			*item = previous
			return WishlistItem{}, err
		}
		s.wakeWishlist()
		return item.WishlistItem, nil
	}

	s.wishlistNextID++
	entry := wishlistEntry{WishlistItem: WishlistItem{ID: "w-" + strconv.FormatUint(s.wishlistNextID, 10), Query: query, Filter: expression, AddedAt: time.Now().UTC()}, generation: 1}
	s.wishlist = append(s.wishlist, entry)
	if err := s.persistWishlistLocked(&entry, true); err != nil {
		s.wishlist = s.wishlist[:len(s.wishlist)-1]
		s.wishlistNextID--
		return WishlistItem{}, err
	}
	s.wakeWishlist()
	return entry.WishlistItem, nil
}

func (s *Service) RemoveWishlist(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.wishlist, func(item wishlistEntry) bool { return item.ID == id })
	if i < 0 {
		return ErrWishlistNotFound
	}
	previous := slices.Clone(s.wishlist)
	s.wishlist = append(s.wishlist[:i], s.wishlist[i+1:]...)
	if err := s.deleteWishlistLocked(id); err != nil {
		s.wishlist = previous
		return err
	}
	delete(s.searches, wishlistSearchID(id))
	if s.wishlistCursor > i {
		s.wishlistCursor--
	}
	s.wakeWishlist()
	return nil
}

func wishlistSearchID(id string) string { return "wishlist:" + id }

func matchingSearchResults(results []SearchResult, filter searchFilter) []SearchResult {
	matching := make([]SearchResult, 0, len(results))
	for _, result := range results {
		if filter.matches(result) {
			matching = append(matching, result)
		}
	}
	return matching
}

func searchResultSignature(results []SearchResult) string {
	if len(results) == 0 {
		return ""
	}
	identities := make([]string, len(results))
	for i, result := range results {
		identities[i] = result.Username + "\x00" + result.Path + "\x00" + strconv.FormatUint(result.Size, 10)
	}
	slices.Sort(identities)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(identities, "\x00")+"\x00")))
}

func fromSoulseekResults(results []soulseek.SearchResult) []SearchResult {
	out := make([]SearchResult, len(results))
	for i, result := range results {
		out[i] = SearchResult{Username: result.Username, Path: result.Path, Extension: result.Extension, CountryCode: result.CountryCode, Size: result.Size, Directory: result.IsDirectory, SlotFree: result.SlotFree, Speed: result.Speed, Queue: result.QueueLength, Bitrate: result.Bitrate, Duration: result.Duration, VBR: result.VBR, VBRKnown: result.VBRKnown, SampleRate: result.SampleRate, BitDepth: result.BitDepth, Public: result.Public}
	}
	return out
}

func (s *Service) runWishlist(ctx context.Context, id string, automatic bool) (SearchPage, error) {
	s.mu.Lock()
	index := slices.IndexFunc(s.wishlist, func(item wishlistEntry) bool { return item.ID == id })
	if index < 0 {
		s.mu.Unlock()
		return SearchPage{}, ErrWishlistNotFound
	}
	item := &s.wishlist[index]
	if item.Running {
		s.mu.Unlock()
		return SearchPage{}, errors.New("daemon: wishlist item is already running")
	}
	client, query, expression, generation := s.client, item.Query, item.Filter, item.generation
	if client == nil {
		s.mu.Unlock()
		return SearchPage{}, ErrNotStarted
	}
	item.Running, item.Error = true, ""
	s.mu.Unlock()

	results, err := s.wishlistSearch(ctx, client, query, automatic)
	now := time.Now().UTC()

	s.mu.Lock()
	index = slices.IndexFunc(s.wishlist, func(item wishlistEntry) bool { return item.ID == id })
	if index < 0 || s.wishlist[index].generation != generation {
		s.mu.Unlock()
		return SearchPage{}, ErrWishlistNotFound
	}
	item = &s.wishlist[index]
	item.Running, item.LastRunAt = false, now
	if err != nil {
		item.Error = err.Error()
		_ = s.persistWishlistLocked(item, false)
		s.mu.Unlock()
		return SearchPage{}, err
	}

	converted := fromSoulseekResults(results)
	sortSearchResults(converted)
	filter, _ := parseSearchFilter(expression)
	matching := matchingSearchResults(converted, filter)
	signature := searchResultSignature(matching)
	search := Search{ID: wishlistSearchID(id), Query: query, Results: converted}
	s.searches[search.ID] = search
	item.Error, item.ResultCount = "", len(matching)
	notify := false
	if automatic {
		if len(matching) == 0 {
			item.Unread = false
		} else if signature != item.ResultSignature {
			item.Unread = true
			item.NotificationSequence++
			notify = s.cfg.Search.WishlistNotifications
		}
	} else {
		item.Unread = false
	}
	item.ResultSignature = signature
	page := filteredSearchPage(search, filter, 0)
	saveErr := s.persistWishlistLocked(item, false)
	s.mu.Unlock()
	if saveErr != nil {
		return SearchPage{}, saveErr
	}
	if notify {
		if err := s.wishlistNotify(ctx, query, len(matching)); err != nil {
			log.Printf("wishlist notification: %v", err)
		}
	}
	return page, nil
}

func (s *Service) RunWishlist(ctx context.Context, id string) (SearchPage, error) {
	return s.runWishlist(ctx, id, false)
}

func (s *Service) OpenWishlist(id string) (SearchPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.wishlist, func(item wishlistEntry) bool { return item.ID == id })
	if i < 0 {
		return SearchPage{}, ErrWishlistNotFound
	}
	item := &s.wishlist[i]
	search, ok := s.searches[wishlistSearchID(id)]
	if !ok {
		return SearchPage{}, ErrWishlistNoResults
	}
	filter, _ := parseSearchFilter(item.Filter)
	wasUnread := item.Unread
	item.Unread = false
	if err := s.persistWishlistLocked(item, false); err != nil {
		item.Unread = wasUnread
		return SearchPage{}, err
	}
	return filteredSearchPage(search, filter, 0), nil
}

func (s *Service) wakeWishlist() {
	select {
	case s.wishlistWake <- struct{}{}:
	default:
	}
}

func (s *Service) nextWishlistID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.wishlist) == 0 {
		return ""
	}
	s.wishlistCursor %= len(s.wishlist)
	id := s.wishlist[s.wishlistCursor].ID
	s.wishlistCursor = (s.wishlistCursor + 1) % len(s.wishlist)
	return id
}

func (s *Service) wishlistLoop(ctx context.Context) {
	defer s.wg.Done()
	var lastRequest time.Time
	for {
		s.mu.RLock()
		delay, ready := s.wishlistIntervalLocked()
		s.mu.RUnlock()
		if !ready {
			lastRequest = time.Time{}
			select {
			case <-ctx.Done():
				return
			case <-s.wishlistWake:
			}
			continue
		}
		wait := delay
		if !lastRequest.IsZero() {
			wait = max(time.Duration(0), delay-time.Since(lastRequest))
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.wishlistWake:
			if !timer.Stop() {
				<-timer.C
			}
			continue
		case <-timer.C:
		}
		if id := s.nextWishlistID(); id != "" {
			lastRequest = time.Now()
			if _, err := s.runWishlist(ctx, id, true); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrNotStarted) && !errors.Is(err, ErrWishlistNotFound) {
				log.Printf("wishlist search: %v", err)
			}
		}
	}
}

func notifyWishlist(ctx context.Context, query string, count int) error {
	return notifyDesktop(ctx, "Wishlist results found", fmt.Sprintf("%q found %d matching results", query, count))
}
