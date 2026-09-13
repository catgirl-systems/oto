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
	var results []soulseek.SearchResult
	if scope.Scope == "global" {
		results, err = client.Search(ctx, req.Query)
	} else {
		results, err = client.SearchScoped(ctx, req.Query, users, scope.Rooms)
	}
	if err != nil {
		return SearchPage{}, err
	}
	out := fromSoulseekResults(results)
	sortSearchResults(out)
	if scope.Scope != "global" && len(out) == 0 {
		scope.Warning = "No results; scoped searches have no server acknowledgement, so support cannot be inferred. No global search was sent."
	}
	search := Search{SearchContext: scope, Usernames: users, ID: fmt.Sprintf("%d", time.Now().UnixNano()), Query: req.Query, Results: out}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, scope.CommunityIdentity); err != nil {
		return SearchPage{}, err
	}
	if s.client != client {
		return SearchPage{}, ErrCommunitySession
	}
	s.searches[search.ID] = search
	return filteredSearchPage(search, filter, 0), nil
}
