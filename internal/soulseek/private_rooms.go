package soulseek

import (
	"context"
	"fmt"
)

// Room management codes are the server namespace, not peer messages. Membership
// grants are the invitations themselves; there is no invitation-accept request.
const (
	ServerRoomWallSnapshot        uint32 = 113
	ServerRoomWallAdded           uint32 = 114
	ServerRoomWallRemoved         uint32 = 115
	ServerSetRoomWall             uint32 = 116
	ServerRoomMembers             uint32 = 133
	ServerAddRoomMember           uint32 = 134
	ServerRemoveRoomMember        uint32 = 135
	ServerCancelRoomMembership    uint32 = 136
	ServerCancelRoomOwnership     uint32 = 137
	ServerRoomMembershipGranted   uint32 = 139
	ServerRoomMembershipRevoked   uint32 = 140
	ServerRoomInvitations         uint32 = 141
	ServerAddRoomOperator         uint32 = 143
	ServerRemoveRoomOperator      uint32 = 144
	ServerRoomOperatorshipGranted uint32 = 145
	ServerRoomOperatorshipRevoked uint32 = 146
	ServerRoomOperators           uint32 = 148
	ServerCannotCreateRoom        uint32 = 1003
)

type RoomRoleAction string

const (
	RoomAddMember           RoomRoleAction = "add-member"
	RoomRemoveMember        RoomRoleAction = "remove-member"
	RoomAddOperator         RoomRoleAction = "add-operator"
	RoomRemoveOperator      RoomRoleAction = "remove-operator"
	RoomCancelMembership    RoomRoleAction = "cancel-membership"
	RoomCancelOwnership     RoomRoleAction = "cancel-ownership"
	RoomMembershipGranted   RoomRoleAction = "membership-granted"
	RoomMembershipRevoked   RoomRoleAction = "membership-revoked"
	RoomOperatorshipGranted RoomRoleAction = "operatorship-granted"
	RoomOperatorshipRevoked RoomRoleAction = "operatorship-revoked"
	RoomCreationRejected    RoomRoleAction = "creation-rejected"
)

type RoomRoleRequest struct {
	Room, Username string
	Action         RoomRoleAction
}

func (m RoomRoleRequest) command() uint32 {
	switch m.Action {
	case RoomAddMember:
		return ServerAddRoomMember
	case RoomRemoveMember:
		return ServerRemoveRoomMember
	case RoomAddOperator:
		return ServerAddRoomOperator
	case RoomRemoveOperator:
		return ServerRemoveRoomOperator
	case RoomCancelMembership:
		return ServerCancelRoomMembership
	case RoomCancelOwnership:
		return ServerCancelRoomOwnership
	}
	return 0
}
func (m RoomRoleRequest) encode(e *Encoder) error {
	if m.command() == 0 {
		return fmt.Errorf("%w: unsupported room role request", ErrMalformed)
	}
	if err := encodeRoomName(e, m.Room); err != nil {
		return err
	}
	if m.Action == RoomCancelMembership || m.Action == RoomCancelOwnership {
		if m.Username != "" {
			return fmt.Errorf("%w: relinquishment cannot target another user", ErrMalformed)
		}
		return nil
	}
	return encodeUsername(e, m.Username)
}

type RoomInvitations struct{ Enabled bool }

func (RoomInvitations) socialMessage()            {}
func (RoomInvitations) command() uint32           { return ServerRoomInvitations }
func (m RoomInvitations) encode(e *Encoder) error { e.Bool(m.Enabled); return nil }
func DecodeRoomInvitations(payload []byte) (m RoomInvitations, err error) {
	d := NewDecoder(payload)
	if m.Enabled, err = d.Bool(); err != nil {
		return m, err
	}
	return m, d.Done()
}

type RoomRoleUpdate struct {
	Room, Username string
	Action         RoomRoleAction
}

func (RoomRoleUpdate) socialMessage() {}
func DecodeRoomRoleUpdate(payload []byte, code uint32) (m RoomRoleUpdate, err error) {
	switch code {
	case ServerAddRoomMember:
		m.Action = RoomAddMember
	case ServerRemoveRoomMember:
		m.Action = RoomRemoveMember
	case ServerAddRoomOperator:
		m.Action = RoomAddOperator
	case ServerRemoveRoomOperator:
		m.Action = RoomRemoveOperator
	case ServerRoomMembershipGranted:
		m.Action = RoomMembershipGranted
	case ServerRoomMembershipRevoked:
		m.Action = RoomMembershipRevoked
	case ServerRoomOperatorshipGranted:
		m.Action = RoomOperatorshipGranted
	case ServerRoomOperatorshipRevoked:
		m.Action = RoomOperatorshipRevoked
	case ServerCannotCreateRoom:
		m.Action = RoomCreationRejected
	default:
		return m, fmt.Errorf("%w: unknown room role update", ErrMalformed)
	}
	d := NewDecoder(payload)
	if m.Room, err = decodeRoomName(d); err != nil {
		return m, err
	}
	switch m.Action {
	case RoomAddMember, RoomRemoveMember, RoomAddOperator, RoomRemoveOperator:
		if m.Username, err = decodeUsername(d); err != nil {
			return m, err
		}
	}
	return m, d.Done()
}

type RoomRoleList struct {
	Room      string
	Operators bool
	Users     []string
}

func (RoomRoleList) socialMessage() {}
func DecodeRoomRoleList(payload []byte, operators bool) (m RoomRoleList, err error) {
	m.Operators = operators
	d := NewDecoder(payload)
	if m.Room, err = decodeRoomName(d); err != nil {
		return m, err
	}
	n, err := decodeRoomCount(d, MaxRoomUsers, 4)
	if err != nil {
		return m, err
	}
	m.Users = make([]string, n)
	for i := range m.Users {
		if m.Users[i], err = decodeUsername(d); err != nil {
			return m, err
		}
	}
	return m, d.Done()
}

type RoomWallEntry struct{ Username, Text string }
type RoomWallSnapshot struct {
	Room    string
	Entries []RoomWallEntry
}

func (RoomWallSnapshot) socialMessage() {}
func DecodeRoomWallSnapshot(payload []byte) (m RoomWallSnapshot, err error) {
	d := NewDecoder(payload)
	if m.Room, err = decodeRoomName(d); err != nil {
		return m, err
	}
	n, err := decodeRoomCount(d, MaxRoomUsers, 8)
	if err != nil {
		return m, err
	}
	m.Entries = make([]RoomWallEntry, n)
	for i := range m.Entries {
		if m.Entries[i].Username, err = decodeUsername(d); err != nil {
			return m, err
		}
		if m.Entries[i].Text, err = d.String(); err != nil {
			return m, err
		}
		if len(m.Entries[i].Text) > MaxChatBytes {
			return m, ErrTooLarge
		}
	}
	return m, d.Done()
}

type RoomWallUpdate struct {
	Room string
	RoomWallEntry
	Remove bool
}

func (RoomWallUpdate) socialMessage() {}
func DecodeRoomWallUpdate(payload []byte, remove bool) (m RoomWallUpdate, err error) {
	m.Remove = remove
	if remove {
		left, err := DecodeRoomUserLeft(payload)
		m.Room, m.Username = left.Room, left.Username
		return m, err
	}
	message, err := DecodeRoomMessage(payload, false)
	m.Room, m.Username, m.Text = message.Room, message.Username, message.Text
	return m, err
}

type RoomWallRequest struct{ Room, Text string }

func (RoomWallRequest) command() uint32 { return ServerSetRoomWall }
func (m RoomWallRequest) encode(e *Encoder) error {
	if m.Text != "" {
		return (RoomMessageRequest{Room: m.Room, Text: m.Text}).encode(e)
	}
	// An empty wall removes the old ticker; it is not an empty chat send.
	if err := encodeRoomName(e, m.Room); err != nil {
		return err
	}
	return e.String("")
}
func (c *Client) ChangeRoomRole(ctx context.Context, req RoomRoleRequest, before func() error) (bool, error) {
	return c.sendTracked(ctx, req, before)
}
func (c *Client) SetRoomInvitations(ctx context.Context, enabled bool, before func() error) (bool, error) {
	return c.sendTracked(ctx, RoomInvitations{Enabled: enabled}, before)
}
func (c *Client) SetRoomWall(ctx context.Context, room, text string, before func() error) (bool, error) {
	return c.sendTracked(ctx, RoomWallRequest{Room: room, Text: text}, before)
}
