package ipc

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestCommunityPrivateRoomIPCPreferencesAndValidation(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "u", "p"
	client, _ := communityIPC(t, cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
	ctx := context.Background()
	summary, err := client.CommunitySummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := summary.CommunityIdentity
	if _, err = client.OpenCommunityConversation(ctx, daemon.CommunityOpenConversationRequest{CommunityIdentity: id, Room: "oto test"}); err != nil {
		t.Fatal(err)
	}
	page, err := client.CommunityRooms(ctx, daemon.CommunityRoomsRequest{CommunityIdentity: id})
	if err != nil {
		t.Fatal(err)
	}
	if !page.InvitationsEnabled {
		t.Fatal("default invitation preference lost")
	}
	pref := daemon.CommunityRoomInvitationsRequest{CommunityIdentity: id, Revision: page.Revision, Enabled: false}
	if err = client.SetCommunityRoomInvitations(ctx, pref); err != nil {
		t.Fatal(err)
	}
	pref.Enabled = true
	if err = client.SetCommunityRoomInvitations(ctx, pref); err == nil {
		t.Fatal("stale invitation edit accepted")
	}
	page, err = client.CommunityRooms(ctx, daemon.CommunityRoomsRequest{CommunityIdentity: id, Mode: "invitations"})
	if err != nil || page.InvitationsEnabled || page.InvitationsState != "saved; reconnect pending" || len(page.Rooms) != 0 {
		t.Fatal(page, err)
	}
	wallReq := daemon.CommunityRoomWallRequest{CommunityIdentity: id, Room: "oto test", Text: "hello 世界", Revision: page.Revision}
	if err = client.SetCommunityRoomWall(ctx, wallReq); err != nil {
		t.Fatal(err)
	}
	wall, err := client.CommunityRoomWall(ctx, daemon.CommunityRoomMembersRequest{CommunityIdentity: id, Room: "oto test", Limit: 200})
	if err != nil || wall.OwnText != wallReq.Text || wall.Fresh || wall.Entries == nil || wall.Room.RoleFresh {
		t.Fatal(wall, err)
	}
	if err = client.SetCommunityRoomWall(ctx, wallReq); err == nil {
		t.Fatal("stale wall edit accepted")
	}
	wallReq.Text, wallReq.Revision = "", wall.Revision
	if err = client.SetCommunityRoomWall(ctx, wallReq); err == nil {
		t.Fatal("wall clear without confirmation")
	}
	wallReq.Confirm = true
	if err = client.SetCommunityRoomWall(ctx, wallReq); err != nil {
		t.Fatal(err)
	}
	wall, err = client.CommunityRoomWall(ctx, daemon.CommunityRoomMembersRequest{CommunityIdentity: id, Room: "oto test"})
	if err != nil || wall.OwnText != "" {
		t.Fatal(wall, err)
	}
	members, err := client.CommunityRoomMembers(ctx, daemon.CommunityRoomMembersRequest{CommunityIdentity: id, Room: "oto test", Private: true})
	if err != nil || members.MembersFresh || members.Members == nil {
		t.Fatal(members, err)
	}
	if _, err = client.ChangeCommunityRoomRole(ctx, daemon.CommunityRoomRoleRequest{CommunityIdentity: id, Room: "oto test", Action: soulseek.RoomCancelOwnership, RequestID: "role", Revision: wall.Revision, Confirm: true}); err == nil {
		t.Fatal("offline role change accepted")
	}
	if _, err = client.CommunityRoomAction(ctx, daemon.CommunityRoomActionRequest{CommunityIdentity: id, Room: "oto test", Action: "join", Private: true, RequestID: "create"}); err == nil {
		t.Fatal("offline private creation accepted")
	}
	wallReq.CommunityIdentity.Session++
	wallReq.Revision = wall.Revision
	if err = client.SetCommunityRoomWall(ctx, wallReq); err == nil {
		t.Fatal("old session wall edit accepted")
	}
	values := communityRoomValues(id)
	values.Set("room", "oto test")
	values.Set("private", "not-bool")
	for _, url := range []string{
		"/v1/community/rooms/members?" + values.Encode(),
		"/v1/community/rooms/wall?session=invalid",
		"/v1/community/rooms/wall?" + communityRoomValues(id).Encode() + "&room=oto+test&limit=-1",
	} {
		response, err := client.http.Do(mustRequest(http.MethodGet, "http://oto.local"+url, nil))
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatal(url, response.StatusCode)
		}
	}
}
