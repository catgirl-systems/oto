package soulseek

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ServerWatchUser   uint32 = 5
	ServerUnwatchUser uint32 = 6
	ServerUserStatus  uint32 = 7
	ServerUserStats   uint32 = 36
	MaxUsernameBytes         = 1024
)

// SocialMessage is a decoded server update, delivered synchronously without
// client locks. The handler must honor cancellation and must not wait for a
// network response. Returning an error terminates Run; updates are never dropped.
// Peer messages cannot enter this path (server and peer command numbers overlap).
type SocialMessage interface{ socialMessage() }

func (WatchUserResponse) socialMessage() {}
func (UserPresence) socialMessage()      {}
func (UserStatistics) socialMessage()    {}
func (PeerAddress) socialMessage()       {}

// ValidateUsername validates without folding, trimming or rewriting identity.
func ValidateUsername(username string) error {
	if strings.TrimSpace(username) == "" || len(username) > MaxUsernameBytes || !utf8.ValidString(username) || strings.IndexFunc(username, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: invalid username", ErrMalformed)
	}
	return nil
}

type WatchUserRequest struct{ Username string }
type UnwatchUserRequest struct{ Username string }
type UserStatusRequest struct{ Username string }
type UserStatsRequest struct{ Username string }

func (WatchUserRequest) command() uint32             { return ServerWatchUser }
func (UnwatchUserRequest) command() uint32           { return ServerUnwatchUser }
func (UserStatusRequest) command() uint32            { return ServerUserStatus }
func (UserStatsRequest) command() uint32             { return ServerUserStats }
func (m WatchUserRequest) encode(e *Encoder) error   { return encodeUsername(e, m.Username) }
func (m UnwatchUserRequest) encode(e *Encoder) error { return encodeUsername(e, m.Username) }
func (m UserStatusRequest) encode(e *Encoder) error  { return encodeUsername(e, m.Username) }
func (m UserStatsRequest) encode(e *Encoder) error   { return encodeUsername(e, m.Username) }

func encodeUsername(e *Encoder, username string) error {
	if err := ValidateUsername(username); err != nil {
		return err
	}
	return e.String(username)
}

func decodeUsername(d *Decoder) (string, error) {
	username, err := d.String()
	if err != nil {
		return "", err
	}
	return username, ValidateUsername(username)
}

type UserStats struct {
	AverageSpeed uint32
	UploadCount  uint32
	Unknown      uint32 // Reserved wire field, not a count or capability.
	Files        uint32
	Directories  uint32
}

type WatchUserResponse struct {
	Username string
	Exists   bool
	Status   UserStatus
	Stats    UserStats
	Country  string
}

type UserPresence struct {
	Username   string
	Status     UserStatus
	Privileged bool
}

type UserStatistics struct {
	Username string
	Stats    UserStats
}

func decodeUserStatus(d *Decoder) (UserStatus, error) {
	n, err := d.U32()
	if err != nil {
		return 0, err
	}
	if n > uint32(UserStatusOnline) {
		return 0, fmt.Errorf("%w: invalid user status", ErrMalformed)
	}
	return UserStatus(n), nil
}

func decodeUserStats(d *Decoder) (stats UserStats, err error) {
	for _, field := range []*uint32{&stats.AverageSpeed, &stats.UploadCount, &stats.Unknown, &stats.Files, &stats.Directories} {
		if *field, err = d.U32(); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

func DecodeWatchUser(payload []byte) (m WatchUserResponse, err error) {
	d := NewDecoder(payload)
	if m.Username, err = decodeUsername(d); err != nil {
		return m, err
	}
	if m.Exists, err = d.Bool(); err != nil {
		return m, err
	}
	if !m.Exists {
		return m, d.Done()
	}
	if m.Status, err = decodeUserStatus(d); err != nil {
		return m, err
	}
	if m.Stats, err = decodeUserStats(d); err != nil {
		return m, err
	}
	// Offline responses from older servers omit the country entirely.
	if d.Remaining() > 0 {
		if m.Country, err = d.String(); err != nil {
			return m, err
		}
		if len(m.Country) > 2 {
			return m, fmt.Errorf("%w: country code", ErrMalformed)
		}
	}
	return m, d.Done()
}

func DecodeUserPresence(payload []byte) (m UserPresence, err error) {
	d := NewDecoder(payload)
	if m.Username, err = decodeUsername(d); err != nil {
		return m, err
	}
	if m.Status, err = decodeUserStatus(d); err != nil {
		return m, err
	}
	if m.Privileged, err = d.Bool(); err != nil {
		return m, err
	}
	return m, d.Done()
}

func DecodeUserStatistics(payload []byte) (m UserStatistics, err error) {
	d := NewDecoder(payload)
	if m.Username, err = decodeUsername(d); err != nil {
		return m, err
	}
	if m.Stats, err = decodeUserStats(d); err != nil {
		return m, err
	}
	return m, d.Done()
}

// DecodeServerMessage keeps server-only messages out of the peer command space.
func DecodeServerMessage(command uint32, payload []byte) (any, error) {
	switch command {
	case ServerWatchUser:
		return DecodeWatchUser(payload)
	case ServerUserStatus:
		return DecodeUserPresence(payload)
	case ServerUserStats:
		return DecodeUserStatistics(payload)
	case ServerPrivateMessage:
		return DecodePrivateMessage(payload)
	case ServerRoomMessage, ServerPublicFeedMessage:
		return DecodeRoomMessage(payload, command == ServerPublicFeedMessage)
	case ServerJoinRoom:
		return DecodeRoomJoined(payload)
	case ServerLeaveRoom:
		return DecodeRoomLeft(payload)
	case ServerRoomUserJoined:
		return DecodeRoomUserJoined(payload)
	case ServerRoomUserLeft:
		return DecodeRoomUserLeft(payload)
	case ServerRoomList:
		return DecodeRoomDirectory(payload)
	case ServerRoomMembers, ServerRoomOperators:
		return DecodeRoomRoleList(payload, command == ServerRoomOperators)
	case ServerAddRoomMember, ServerRemoveRoomMember, ServerAddRoomOperator, ServerRemoveRoomOperator,
		ServerRoomMembershipGranted, ServerRoomMembershipRevoked, ServerRoomOperatorshipGranted, ServerRoomOperatorshipRevoked, ServerCannotCreateRoom:
		return DecodeRoomRoleUpdate(payload, command)
	case ServerRoomInvitations:
		return DecodeRoomInvitations(payload)
	case ServerRoomWallSnapshot:
		return DecodeRoomWallSnapshot(payload)
	case ServerRoomWallAdded, ServerRoomWallRemoved:
		return DecodeRoomWallUpdate(payload, command == ServerRoomWallRemoved)
	default:
		return DecodeMessage(command, payload)
	}
}

// WatchUser and UnwatchUser only write requests. Subscription ownership and
// reconnect restoration belong to the daemon, not individual frontends.
func (c *Client) WatchUser(ctx context.Context, username string) error {
	return c.sendContext(ctx, WatchUserRequest{Username: username})
}

func (c *Client) UnwatchUser(ctx context.Context, username string) error {
	return c.sendContext(ctx, UnwatchUserRequest{Username: username})
}

func (c *Client) RequestUserStatus(ctx context.Context, username string) error {
	return c.sendContext(ctx, UserStatusRequest{Username: username})
}

func (c *Client) RequestUserStats(ctx context.Context, username string) error {
	return c.sendContext(ctx, UserStatsRequest{Username: username})
}

// ResolveUserAddress shares an outstanding lookup with transfers. The wire has
// no request token, so cancellation doesn't discard an in-flight correlation.
func (c *Client) ResolveUserAddress(ctx context.Context, username string) (PeerAddress, error) {
	if err := ValidateUsername(username); err != nil {
		return PeerAddress{}, err
	}
	return c.lookupPeerAddress(ctx, username)
}
