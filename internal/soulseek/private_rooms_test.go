package soulseek

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/testutil"
)

func TestCommunityPrivateRoomProtocol(t *testing.T) {
	requests := map[string]Message{
		"wall-set":                      RoomWallRequest{Room: "oto test", Text: "hello 世界"},
		"wall-clear":                    RoomWallRequest{Room: "oto test"},
		"room-invitations-true-client":  RoomInvitations{Enabled: true},
		"room-invitations-false-client": RoomInvitations{},
	}
	updates := map[string]any{
		"role-members":                  RoomRoleList{Room: "oto test", Users: []string{"Alice", "Bob"}},
		"role-operators":                RoomRoleList{Room: "oto test", Operators: true, Users: []string{"Alice", "Bob"}},
		"room-invitations-true-server":  RoomInvitations{Enabled: true},
		"room-invitations-false-server": RoomInvitations{},
		"wall-snapshot":                 RoomWallSnapshot{Room: "oto test", Entries: []RoomWallEntry{{Username: "Bob", Text: "hello 世界"}}},
		"wall-added":                    RoomWallUpdate{Room: "oto test", RoomWallEntry: RoomWallEntry{Username: "Bob", Text: "hello 世界"}},
		"wall-removed":                  RoomWallUpdate{Room: "oto test", RoomWallEntry: RoomWallEntry{Username: "Bob"}, Remove: true},
	}
	for _, action := range []RoomRoleAction{RoomAddMember, RoomRemoveMember, RoomAddOperator, RoomRemoveOperator, RoomCancelMembership, RoomCancelOwnership} {
		req := RoomRoleRequest{Room: "oto test", Action: action}
		if action != RoomCancelMembership && action != RoomCancelOwnership {
			req.Username = "Bob"
			updates["role-"+string(action)] = RoomRoleUpdate{Room: "oto test", Username: "Bob", Action: action}
		}
		requests["role-"+string(action)+"-request"] = req
	}
	for _, action := range []RoomRoleAction{RoomMembershipGranted, RoomMembershipRevoked, RoomOperatorshipGranted, RoomOperatorshipRevoked, RoomCreationRejected} {
		updates["role-"+string(action)] = RoomRoleUpdate{Room: "oto test", Action: action}
	}
	for name, req := range requests {
		t.Run(name, func(t *testing.T) {
			fixture := communityFixture(t, name)
			encoded, err := EncodeMessage(req)
			code, payload, decodeErr := ReadFrame(bytes.NewReader(encoded))
			if err != nil || decodeErr != nil || code != fixture.Code || !bytes.Equal(payload, fixture.Payload(t)) {
				t.Fatal("independent encoder mismatch", err, decodeErr)
			}
		})
	}
	for name, want := range updates {
		t.Run(name, func(t *testing.T) {
			fixture := communityFixture(t, name)
			payload := fixture.Payload(t)
			got, err := DecodeServerMessage(fixture.Code, payload)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("reference fields: %#v %v", got, err)
			}
			if _, ok := got.(SocialMessage); !ok {
				t.Fatal("role/wall update not authoritative")
			}
			for n := range len(payload) {
				if _, err := DecodeServerMessage(fixture.Code, payload[:n]); err == nil {
					t.Fatalf("truncated at %d", n)
				}
			}
			if _, err := DecodeServerMessage(fixture.Code, append(bytes.Clone(payload), 0)); err == nil {
				t.Fatal("trailing bytes accepted")
			}
		})
	}
}
func TestCommunityPrivateRoomValidation(t *testing.T) {
	for _, req := range []Message{
		RoomRoleRequest{Room: "room", Action: "transfer-ownership", Username: "Bob"},
		RoomRoleRequest{Room: "room", Action: RoomCancelOwnership, Username: "Bob"},
		RoomRoleRequest{Room: "room", Action: RoomAddMember},
		RoomRoleRequest{Room: "invalid  room", Action: RoomAddMember, Username: "Bob"},
		RoomWallRequest{Room: "room", Text: "two\nlines"},
		RoomWallRequest{Room: "room", Text: strings.Repeat("x", MaxChatBytes+1)},
	} {
		if _, err := EncodeMessage(req); err == nil {
			t.Fatalf("invalid room request encoded: %T", req)
		}
	}
	client := NewClient(ClientConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, send := range []func() (bool, error){
		func() (bool, error) {
			return client.ChangeRoomRole(ctx, RoomRoleRequest{Room: "oto test", Action: RoomAddMember, Username: "Bob"}, nil)
		},
		func() (bool, error) { return client.SetRoomWall(ctx, "oto test", "hello", nil) },
		func() (bool, error) { return client.SetRoomInvitations(ctx, true, nil) },
	} {
		attempted, err := send()
		if attempted || err == nil {
			t.Fatal("cancelled/disconnected request attempted write", attempted, err)
		}
	}
}
func FuzzCommunityPrivateRoomDecode(f *testing.F) {
	for _, fixture := range testutil.SocialFixtures(f) {
		if fixture.Direction == "server" && (strings.HasPrefix(fixture.Name, "role-") || strings.HasPrefix(fixture.Name, "wall-") || strings.HasPrefix(fixture.Name, "room-invitations-")) {
			f.Add(fixture.Code, fixture.Payload(f))
		}
	}
	f.Fuzz(func(t *testing.T, code uint32, payload []byte) {
		switch code {
		case ServerRoomMembers, ServerRoomOperators, ServerAddRoomMember, ServerRemoveRoomMember, ServerAddRoomOperator, ServerRemoveRoomOperator, ServerRoomMembershipGranted, ServerRoomMembershipRevoked, ServerRoomOperatorshipGranted, ServerRoomOperatorshipRevoked, ServerCannotCreateRoom, ServerRoomInvitations, ServerRoomWallSnapshot, ServerRoomWallAdded, ServerRoomWallRemoved:
			_, _ = DecodeServerMessage(code, payload)
		}
	})
}
