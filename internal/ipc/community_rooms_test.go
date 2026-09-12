package ipc

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestCommunityRoomIPCResources(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "u", "p"
	client, _ := communityIPC(t, cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
	ctx := context.Background()
	summary, err := client.CommunitySummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	identity := summary.CommunityIdentity

	rooms, err := client.CommunityRooms(ctx, daemon.CommunityRoomsRequest{CommunityIdentity: identity, Limit: 200})
	if err != nil || rooms.CommunityIdentity != identity || rooms.Rooms == nil {
		t.Fatalf("rooms: %+v %v", rooms, err)
	}
	members, err := client.CommunityRoomMembers(ctx, daemon.CommunityRoomMembersRequest{CommunityIdentity: identity, Room: "room"})
	if err != nil || members.CommunityIdentity != identity || members.Members == nil {
		t.Fatalf("members: %+v %v", members, err)
	}
	feed, err := client.CommunityFeed(ctx, daemon.CommunityFeedRequest{CommunityIdentity: identity, Limit: 200})
	if err != nil || feed.CommunityIdentity != identity || feed.Messages == nil {
		t.Fatalf("feed: %+v %v", feed, err)
	}
	if err := client.SetCommunityFeed(ctx, daemon.CommunityFeedSubscription{CommunityIdentity: identity, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := client.RefreshCommunityRooms(ctx, identity); err == nil {
		t.Fatal("refresh unexpectedly succeeded while disconnected")
	}
	if _, err := client.CommunityRoomAction(ctx, daemon.CommunityRoomActionRequest{CommunityIdentity: identity, Room: "room", Action: "join", RequestID: "join"}); err == nil {
		t.Fatal("join unexpectedly succeeded while disconnected")
	}
	if _, err := client.SendCommunityRoom(ctx, daemon.CommunityRoomSendRequest{CommunityIdentity: identity, Room: "room", Text: "hello", RequestID: "send"}); err == nil {
		t.Fatal("send unexpectedly succeeded while disconnected")
	}

	response, err := client.http.Do(mustRequest(http.MethodGet, "http://oto.local/v1/community/rooms?session=no", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid room query status %d", response.StatusCode)
	}
}
