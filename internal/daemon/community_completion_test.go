package daemon

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestCommunityCompletionSourcesAndBounds(t *testing.T) {
	s := downloadService(t)
	ctx := context.Background()
	id := s.community.identity
	_, err := s.SetCommunityAlias(ctx, CommunityAliasRequest{CommunityIdentity: id, Name: "hello", Expansion: "help"})
	must(t, err)
	out, err := s.CompleteCommunity(ctx, CommunityCompletionRequest{CommunityIdentity: id, Kind: "command", Prefix: "he"})
	if err != nil || !reflect.DeepEqual(out.Candidates, []string{"hello", "help"}) {
		t.Fatal(out, err)
	}
	s.mu.Lock()
	s.community.buddies["Alice Smith"] = CommunityBuddy{}
	s.community.watches["frontend:test"] = userWatchLease{users: []string{"Alice Watch"}}
	s.community.buddies["alice"] = CommunityBuddy{}
	s.community.rooms["lounge"] = &communityRoomState{joined: true, members: map[string]soulseek.RoomUser{"Alfred": {}}}
	s.mu.Unlock()
	out, err = s.CompleteCommunity(ctx, CommunityCompletionRequest{CommunityIdentity: id, Kind: "user", Prefix: "al", Room: "lounge"})
	if err != nil || !reflect.DeepEqual(out.Candidates, []string{"Alfred", "Alice Smith", "Alice Watch", "alice"}) {
		t.Fatal(out, err)
	}
	s.mu.Lock()
	for i := 1; i <= 200; i++ {
		s.community.buddies[strings.Repeat("&", i)] = CommunityBuddy{}
	}
	s.mu.Unlock()
	out, err = s.CompleteCommunity(ctx, CommunityCompletionRequest{CommunityIdentity: id, Kind: "user"})
	failIf(t, err != nil || len(out.Candidates) > 200 || !out.Truncated, len(out.Candidates), err)
	id.Session++
	if _, err := s.CompleteCommunity(ctx, CommunityCompletionRequest{CommunityIdentity: id, Kind: "user"}); err == nil {
		t.Fatal("stale completion accepted")
	}
}
