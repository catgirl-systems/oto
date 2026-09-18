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
	must(t, err)
	identity := summary.CommunityIdentity

	rooms, err := client.CommunityRooms(ctx, daemon.CommunityRoomsRequest{CommunityIdentity: identity, Limit: 200})
	failIfFmt(t, err != nil || rooms.CommunityIdentity != identity || rooms.Rooms == nil, "rooms: %+v %v", rooms, err)
	members, err := client.CommunityRoomMembers(ctx, daemon.CommunityRoomMembersRequest{CommunityIdentity: identity, Room: "room"})
	failIfFmt(t, err != nil || members.CommunityIdentity != identity || members.Members == nil, "members: %+v %v", members, err)
	feed, err := client.CommunityFeed(ctx, daemon.CommunityFeedRequest{CommunityIdentity: identity, Limit: 200})
	failIfFmt(t, err != nil || feed.CommunityIdentity != identity || feed.Messages == nil, "feed: %+v %v", feed, err)
	must(t, client.SetCommunityFeed(ctx, daemon.CommunityFeedSubscription{CommunityIdentity: identity, Enabled: true}))
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
	must(t, err)
	_ = response.Body.Close()
	failIfFmt(t, response.StatusCode != http.StatusBadRequest, "invalid room query status %d", response.StatusCode)
}
