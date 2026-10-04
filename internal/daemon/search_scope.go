package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

type ScopedSearchRequest struct {
	CommunityIdentity
	Query     string   `json:"query"`
	Filter    string   `json:"filter"`
	Scope     string   `json:"scope,omitempty"`
	Usernames []string `json:"usernames,omitempty"`
	Rooms     []string `json:"rooms,omitempty"`
}
type SearchContext struct {
	CommunityIdentity
	Scope            string   `json:"scope,omitempty"`
	Rooms            []string `json:"rooms,omitempty"`
	TargetCount      int      `json:"target_count,omitempty"`
	TargetsTruncated bool     `json:"targets_truncated,omitempty"`
	Warning          string   `json:"warning,omitempty"`
}

// Capture targets under the same lock as session authority. Buddy edits or room
// selection changes after submission do not silently change an active search.
func (s *Service) captureSearchLocked(ctx context.Context, req ScopedSearchRequest) (SearchContext, []string, error) {
	identity := req.CommunityIdentity
	if identity == (CommunityIdentity{}) && req.Scope != "buddies" && req.Scope != "rooms" {
		identity = s.community.identity
	}
	out := SearchContext{CommunityIdentity: identity, Scope: req.Scope}
	if err := s.checkCommunityIdentityLocked(ctx, identity); err != nil {
		return out, nil, err
	}
	if out.Scope == "" {
		out.Scope = "global"
		if len(req.Usernames) > 0 {
			out.Scope = "users"
		}
	}
	var users []string
	switch out.Scope {
	case "global":
		if len(req.Usernames) > 0 || len(req.Rooms) > 0 {
			return out, nil, errors.New("search: global scope cannot contain targets")
		}
	case "users":
		if len(req.Usernames) == 0 || len(req.Rooms) > 0 {
			return out, nil, errors.New("search: users scope requires usernames only")
		}
		var err error
		users, err = soulseek.NormalizeSearchUsers(req.Usernames)
		if err != nil {
			return out, nil, err
		}
	case "buddies":
		if len(req.Usernames) > 0 || len(req.Rooms) > 0 {
			return out, nil, errors.New("search: buddies scope captures the full buddy set; explicit targets conflict")
		}
		for user := range s.community.buddies {
			users = append(users, user)
		}
		slices.Sort(users)
		if len(users) == 0 {
			return out, nil, errors.New("search: no buddies to search")
		}
	case "rooms":
		if len(req.Usernames) > 0 || len(req.Rooms) == 0 {
			return out, nil, errors.New("search: select joined rooms explicitly")
		}
		out.Rooms = slices.Clone(req.Rooms)
		slices.Sort(out.Rooms)
		out.Rooms = slices.Compact(out.Rooms)
		for _, name := range out.Rooms {
			if err := soulseek.ValidateRoomName(name); err != nil {
				return out, nil, err
			}
			room := s.community.rooms[name]
			if room == nil || !room.joined {
				return out, nil, fmt.Errorf("search: room %q is not joined", name)
			}
		}
	default:
		return out, nil, errors.New("search: unsupported scope")
	}
	out.TargetCount = len(users) + len(out.Rooms)
	if out.TargetCount > soulseek.MaxScopedSearchTargets {
		return out, nil, errors.New("search: scoped target budget exceeded; no search submitted")
	}
	return out, users, nil
}

func (s *Service) SearchScoped(ctx context.Context, req ScopedSearchRequest) (SearchPage, error) {
	filter, err := parseSearchFilter(req.Filter)
	if err != nil {
		return SearchPage{}, err
	}
	s.mu.RLock()
	scope, users, err := s.captureSearchLocked(ctx, req)
	client := s.client
	s.mu.RUnlock()
	if err != nil {
		return SearchPage{}, err
	}
	if client == nil {
		return SearchPage{}, ErrNotStarted
	}
	s.mu.Lock()
	if err := s.checkCommunityIdentityLocked(ctx, scope.CommunityIdentity); err != nil {
		s.mu.Unlock()
		return SearchPage{}, err
	}
	if s.client != client {
		s.mu.Unlock()
		return SearchPage{}, ErrCommunitySession
	}
	parent := s.runCtx
	s.mu.Unlock()
	if parent == nil {
		parent = context.Background()
	}
	// Collection outlives this request: it belongs to the daemon and ends
	// after searchLifetime, on eviction, or when the client closes it.
	collectCtx, cancel := context.WithTimeout(parent, searchLifetime)
	now := time.Now()
	st := &storedSearch{Search: Search{SearchContext: scope, Usernames: users, ID: fmt.Sprintf("%d", now.UnixNano()), Query: req.Query}, searching: true, cancel: cancel, done: make(chan struct{})}
	s.searchMu.Lock()
	s.storeSearchLocked(st, now)
	s.searchMu.Unlock()
	targets, rooms := users, scope.Rooms
	if scope.Scope == "global" {
		targets, rooms = nil, nil
	}
	go func() {
		defer cancel()
		err := client.StreamSearch(collectCtx, req.Query, targets, rooms, func(batch []soulseek.SearchResult) {
			s.appendSearchResults(st.ID, batch)
		})
		s.finishSearch(st.ID, err)
	}()
	window := time.NewTimer(searchInitialWindow)
	defer window.Stop()
	select {
	case <-window.C:
	case <-st.done:
	case <-ctx.Done():
	}
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	if st.err != nil {
		// Sending failed: nothing was searched, so report it like before.
		s.dropSearchLocked(st.ID)
		return SearchPage{}, st.err
	}
	if s.searches[st.ID] != st {
		return SearchPage{}, ErrSearchNotFound
	}
	warnIfEmptyScopedLocked(st)
	return s.pageLocked(st, req.Filter, filter, 0, time.Now()), nil
}
