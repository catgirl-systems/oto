package daemon

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestSearchScopeCaptureAndValidation(t *testing.T) {
	s := downloadService(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.community.identity
	for i := range 100 {
		user := fmt.Sprintf("buddy %03d", i)
		s.community.buddies[user] = CommunityBuddy{}
	}
	s.community.rooms["lounge"] = &communityRoomState{joined: true}
	s.community.rooms["pending"] = &communityRoomState{wanted: true}
	scope, users, err := s.captureSearchLocked(context.Background(), ScopedSearchRequest{CommunityIdentity: id, Scope: "buddies"})
	if err != nil || scope.TargetCount != 100 || len(users) != 100 {
		t.Fatal(scope, len(users), err)
	}
	delete(s.community.buddies, users[0])
	if len(users) != 100 {
		t.Fatal("target snapshot changed")
	}
	rooms := []string{"lounge", "lounge"}
	scope, _, err = s.captureSearchLocked(context.Background(), ScopedSearchRequest{CommunityIdentity: id, Scope: "rooms", Rooms: rooms})
	rooms[0] = "pending"
	if err != nil || len(scope.Rooms) != 1 || scope.Rooms[0] != "lounge" {
		t.Fatal(scope, err)
	}
	for _, req := range []ScopedSearchRequest{
		{Scope: "global", Usernames: []string{"Alice"}}, {Scope: "global", Rooms: []string{"lounge"}},
		{Scope: "users"}, {Scope: "users", Rooms: []string{"lounge"}, Usernames: []string{"Alice"}},
		{Scope: "buddies", Usernames: []string{"Alice"}}, {Scope: "rooms"},
		{Scope: "rooms", Rooms: []string{"pending"}}, {Scope: "rooms", Rooms: []string{"missing"}},
		{Scope: "invalid"}, {Rooms: []string{"lounge"}},
	} {
		req.CommunityIdentity = id
		if _, _, err := s.captureSearchLocked(context.Background(), req); err == nil {
			t.Fatal("invalid scope accepted", req)
		}
	}
	scope, users, err = s.captureSearchLocked(context.Background(), ScopedSearchRequest{Usernames: []string{" Alice ", "Alice"}})
	if err != nil || scope.Scope != "users" || len(users) != 1 || users[0] != "Alice" {
		t.Fatal("legacy meaning changed", scope, users, err)
	}
	id.Session++
	if _, _, err := s.captureSearchLocked(context.Background(), ScopedSearchRequest{CommunityIdentity: id, Scope: "buddies"}); !errors.Is(err, ErrCommunitySession) {
		t.Fatal("stale session", err)
	}
}

func TestSearchTargetMetadataBoundAndIsolation(t *testing.T) {
	search := Search{SearchContext: SearchContext{Scope: "buddies", TargetCount: 300}, Usernames: make([]string, 300)}
	for i := range search.Usernames {
		search.Usernames[i] = fmt.Sprint(i)
	}
	filter, err := parseSearchFilter("")
	if err != nil {
		t.Fatal(err)
	}
	page := filteredSearchPage(search, filter, 0)
	if !page.TargetsTruncated || page.TargetCount != 300 || len(page.Usernames) != 200 {
		t.Fatal(page)
	}
	page.Usernames[0] = "changed"
	if search.Usernames[0] != "0" {
		t.Fatal("page mutated snapshot")
	}
}
