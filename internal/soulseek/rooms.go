package soulseek

import (
	"context"
	"fmt"
	"strings"
)

const (
	ServerRoomMessage           uint32 = 13
	ServerJoinRoom              uint32 = 14
	ServerLeaveRoom             uint32 = 15
	ServerRoomUserJoined        uint32 = 16
	ServerRoomUserLeft          uint32 = 17
	ServerRoomList              uint32 = 64
	ServerPublicFeedSubscribe   uint32 = 150
	ServerPublicFeedUnsubscribe uint32 = 151
	ServerPublicFeedMessage     uint32 = 152
	MaxRoomNameBytes                   = 24
	// These are local allocation budgets, not advertised server limits.
	MaxRoomUsers   = 20_000
	MaxRoomEntries = 50_000
)

// ValidateRoomName follows Nicotine+'s JoinRoom contract without renaming rooms.
func ValidateRoomName(room string) error {
	if room == "" || len(room) > MaxRoomNameBytes || strings.TrimSpace(room) != room || strings.Contains(room, "  ") {
		return fmt.Errorf("%w: room names require 1–24 ASCII characters, without leading, trailing or repeated spaces", ErrMalformed)
	}
	for _, c := range []byte(room) {
		if c < 32 || c > 126 {
			return fmt.Errorf("%w: room names require printable ASCII", ErrMalformed)
		}
	}
	return nil
}

func encodeRoomName(e *Encoder, room string) error {
	if err := ValidateRoomName(room); err != nil {
		return err
	}
	return e.String(room)
}
func decodeRoomName(d *Decoder) (string, error) {
	room, err := d.String()
	if err != nil {
		return "", err
	}
	return room, ValidateRoomName(room)
}

type JoinRoomRequest struct {
	Room    string
	Private bool
}
type LeaveRoomRequest struct{ Room string }
type RoomMessageRequest struct{ Room, Text string }
type RoomListRequest struct{}
type PublicFeedRequest struct{ Subscribe bool }

func (JoinRoomRequest) command() uint32    { return ServerJoinRoom }
func (LeaveRoomRequest) command() uint32   { return ServerLeaveRoom }
func (RoomMessageRequest) command() uint32 { return ServerRoomMessage }
func (RoomListRequest) command() uint32    { return ServerRoomList }
func (m PublicFeedRequest) command() uint32 {
	if m.Subscribe {
		return ServerPublicFeedSubscribe
	}
	return ServerPublicFeedUnsubscribe
}
func (m JoinRoomRequest) encode(e *Encoder) error {
	if err := encodeRoomName(e, m.Room); err != nil {
		return err
	}
	if m.Private {
		e.U32(1)
	} else {
		e.U32(0)
	}
	return nil
}
func (m LeaveRoomRequest) encode(e *Encoder) error { return encodeRoomName(e, m.Room) }
func (m RoomMessageRequest) encode(e *Encoder) error {
	if err := encodeRoomName(e, m.Room); err != nil {
		return err
	}
	if err := ValidateChatText(m.Text); err != nil {
		return err
	}
	if strings.ContainsAny(m.Text, "\r\n") {
		return fmt.Errorf("%w: room messages must be one line", ErrMalformed)
	}
	return e.String(m.Text)
}
func (RoomListRequest) encode(*Encoder) error   { return nil }
func (PublicFeedRequest) encode(*Encoder) error { return nil }

type RoomUser struct {
	Username                                          string
	Status                                            UserStatus
	Stats                                             UserStats
	SlotsFull                                         uint32
	Country                                           string
	StatusKnown, StatsKnown, SlotsKnown, CountryKnown bool
}
type RoomJoined struct {
	Room      string
	Users     []RoomUser
	Private   bool
	Owner     string
	Operators []string
}
type RoomLeft struct{ Room string }
type RoomUserJoined struct {
	Room string
	User RoomUser
}
type RoomUserLeft struct{ Room, Username string }
type RoomMessage struct {
	Room, Username, Text string // Text preserves raw legacy bytes for daemon display processing.
	PublicFeed           bool
}
type RoomPopulation struct {
	Room       string
	Users      uint32
	UsersKnown bool
}
type RoomDirectory struct {
	Public, Owned, Member []RoomPopulation
	Operated              []string
}

func (RoomJoined) socialMessage()     {}
func (RoomLeft) socialMessage()       {}
func (RoomUserJoined) socialMessage() {}
func (RoomUserLeft) socialMessage()   {}
func (RoomMessage) socialMessage()    {}
func (RoomDirectory) socialMessage()  {}

// Every array has its own count. Bound allocation and reject overlong parallel
// arrays while retaining explicitly missing metadata as unknown, like Nicotine+.
func decodeRoomCount(d *Decoder, maximum, recordBytes int) (int, error) {
	n, err := d.U32()
	if err != nil {
		return 0, err
	}
	if uint64(n) > uint64(maximum) {
		return 0, fmt.Errorf("%w: room array count", ErrTooLarge)
	}
	if uint64(n)*uint64(recordBytes) > uint64(d.Remaining()) {
		return 0, ErrTruncated
	}
	return int(n), nil
}
func decodeRoomCountry(d *Decoder) (string, error) {
	country, err := d.String()
	if err != nil {
		return "", err
	}
	if len(country) > 2 {
		return "", fmt.Errorf("%w: country code", ErrMalformed)
	}
	return country, nil
}
func DecodeRoomJoined(payload []byte) (m RoomJoined, err error) {
	d := NewDecoder(payload)
	if m.Room, err = decodeRoomName(d); err != nil {
		return m, err
	}
	n, err := decodeRoomCount(d, MaxRoomUsers, 4)
	if err != nil {
		return m, err
	}
	m.Users = make([]RoomUser, n)
	seen := make(map[string]bool, n)
	for i := range m.Users {
		if m.Users[i].Username, err = decodeUsername(d); err != nil {
			return m, err
		}
		if seen[m.Users[i].Username] {
			return m, fmt.Errorf("%w: duplicate room user", ErrMalformed)
		}
		seen[m.Users[i].Username] = true
	}
	count, err := decodeRoomCount(d, n, 4)
	if err != nil {
		return m, err
	}
	for i := range count {
		if m.Users[i].Status, err = decodeUserStatus(d); err != nil {
			return m, err
		}
		m.Users[i].StatusKnown = true
	}
	count, err = decodeRoomCount(d, n, 20)
	if err != nil {
		return m, err
	}
	for i := range count {
		if m.Users[i].Stats, err = decodeUserStats(d); err != nil {
			return m, err
		}
		m.Users[i].StatsKnown = true
	}
	count, err = decodeRoomCount(d, n, 4)
	if err != nil {
		return m, err
	}
	for i := range count {
		if m.Users[i].SlotsFull, err = d.U32(); err != nil {
			return m, err
		}
		m.Users[i].SlotsKnown = true
	}
	count, err = decodeRoomCount(d, n, 4)
	if err != nil {
		return m, err
	}
	for i := range count {
		if m.Users[i].Country, err = decodeRoomCountry(d); err != nil {
			return m, err
		}
		m.Users[i].CountryKnown = true
	}
	if d.Remaining() > 0 {
		m.Private = true
		if m.Owner, err = d.String(); err != nil {
			return m, err
		}
		// An empty owner field does not establish ownership.
		if m.Owner != "" {
			if err = ValidateUsername(m.Owner); err != nil {
				return m, err
			}
		}
		count, err = decodeRoomCount(d, MaxRoomUsers, 4)
		if err != nil {
			return m, err
		}
		m.Operators = make([]string, count)
		for i := range m.Operators {
			if m.Operators[i], err = decodeUsername(d); err != nil {
				return m, err
			}
		}
	}
	return m, d.Done()
}
func DecodeRoomLeft(payload []byte) (m RoomLeft, err error) {
	d := NewDecoder(payload)
	if m.Room, err = decodeRoomName(d); err != nil {
		return m, err
	}
	return m, d.Done()
}
func DecodeRoomUserJoined(payload []byte) (m RoomUserJoined, err error) {
	d := NewDecoder(payload)
	if m.Room, err = decodeRoomName(d); err != nil {
		return m, err
	}
	if m.User.Username, err = decodeUsername(d); err != nil {
		return m, err
	}
	if m.User.Status, err = decodeUserStatus(d); err != nil {
		return m, err
	}
	if m.User.Stats, err = decodeUserStats(d); err != nil {
		return m, err
	}
	if m.User.SlotsFull, err = d.U32(); err != nil {
		return m, err
	}
	if m.User.Country, err = decodeRoomCountry(d); err != nil {
		return m, err
	}
	m.User.StatusKnown, m.User.StatsKnown, m.User.SlotsKnown, m.User.CountryKnown = true, true, true, true
	return m, d.Done()
}
func DecodeRoomUserLeft(payload []byte) (m RoomUserLeft, err error) {
	d := NewDecoder(payload)
	if m.Room, err = decodeRoomName(d); err != nil {
		return m, err
	}
	if m.Username, err = decodeUsername(d); err != nil {
		return m, err
	}
	return m, d.Done()
}
func DecodeRoomMessage(payload []byte, publicFeed bool) (m RoomMessage, err error) {
	m.PublicFeed = publicFeed
	if len(payload) > MaxRoomNameBytes+MaxUsernameBytes+MaxChatBytes+12 {
		return m, ErrTooLarge
	}
	d := NewDecoder(payload)
	if m.Room, err = decodeRoomName(d); err != nil {
		return m, err
	}
	if m.Username, err = decodeUsername(d); err != nil {
		return m, err
	}
	if m.Text, err = d.String(); err != nil {
		return m, err
	}
	if len(m.Text) > MaxChatBytes {
		return m, ErrTooLarge
	}
	return m, d.Done()
}
func decodeRoomPopulations(d *Decoder, maximum int) ([]RoomPopulation, error) {
	n, err := decodeRoomCount(d, maximum, 4)
	if err != nil {
		return nil, err
	}
	rooms := make([]RoomPopulation, n)
	for i := range rooms {
		if rooms[i].Room, err = decodeRoomName(d); err != nil {
			return nil, err
		}
	}
	count, err := decodeRoomCount(d, n, 4)
	if err != nil {
		return nil, err
	}
	for i := range count {
		if rooms[i].Users, err = d.U32(); err != nil {
			return nil, err
		}
		rooms[i].UsersKnown = true
	}
	return rooms, nil
}
func DecodeRoomDirectory(payload []byte) (m RoomDirectory, err error) {
	d := NewDecoder(payload)
	if m.Public, err = decodeRoomPopulations(d, MaxRoomEntries); err != nil {
		return m, err
	}
	if m.Owned, err = decodeRoomPopulations(d, MaxRoomEntries-len(m.Public)); err != nil {
		return m, err
	}
	if m.Member, err = decodeRoomPopulations(d, MaxRoomEntries-len(m.Public)-len(m.Owned)); err != nil {
		return m, err
	}
	count, err := decodeRoomCount(d, MaxRoomEntries-len(m.Public)-len(m.Owned)-len(m.Member), 4)
	if err != nil {
		return m, err
	}
	m.Operated = make([]string, count)
	for i := range m.Operated {
		if m.Operated[i], err = decodeRoomName(d); err != nil {
			return m, err
		}
	}
	return m, d.Done()
}

func (c *Client) RequestRoomDirectory(ctx context.Context) error {
	return c.sendContext(ctx, RoomListRequest{})
}
func (c *Client) JoinRoom(ctx context.Context, room string, private bool, beforeWrite func() error) (bool, error) {
	return c.sendTracked(ctx, JoinRoomRequest{Room: room, Private: private}, beforeWrite)
}
func (c *Client) LeaveRoom(ctx context.Context, room string, beforeWrite func() error) (bool, error) {
	return c.sendTracked(ctx, LeaveRoomRequest{Room: room}, beforeWrite)
}
func (c *Client) SendRoomMessage(ctx context.Context, room, text string, beforeWrite func() error) (bool, error) {
	return c.sendTracked(ctx, RoomMessageRequest{Room: room, Text: text}, beforeWrite)
}
func (c *Client) SetPublicFeed(ctx context.Context, subscribe bool, beforeWrite func() error) (bool, error) {
	return c.sendTracked(ctx, PublicFeedRequest{Subscribe: subscribe}, beforeWrite)
}
