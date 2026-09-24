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
	e.String(room)
	return nil
}
func decodeRoomName(d *Decoder) string {
	room := d.String()
	if err := ValidateRoomName(room); err != nil {
		d.fail(err)
		return ""
	}
	return room
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
	e.String(m.Text)
	return nil
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
func decodeRoomCount(d *Decoder, maximum, recordBytes int) int {
	n := d.U32()
	if uint64(n) > uint64(maximum) {
		d.fail(fmt.Errorf("%w: room array count", ErrTooLarge))
		return 0
	}
	if uint64(n)*uint64(recordBytes) > uint64(d.Remaining()) {
		d.fail(ErrTruncated)
		return 0
	}
	return int(n)
}
func decodeRoomCountry(d *Decoder) string {
	country := d.String()
	if len(country) > 2 {
		d.fail(fmt.Errorf("%w: country code", ErrMalformed))
	}
	return country
}
func DecodeRoomJoined(payload []byte) (RoomJoined, error) {
	d := NewDecoder(payload)
	var m RoomJoined
	m.Room = decodeRoomName(d)
	n := decodeRoomCount(d, MaxRoomUsers, 4)
	m.Users = make([]RoomUser, n)
	seen := make(map[string]bool, n)
	for i := range m.Users {
		m.Users[i].Username = decodeUsername(d)
		if d.Err() != nil {
			return m, d.Err()
		}
		if seen[m.Users[i].Username] {
			return m, fmt.Errorf("%w: duplicate room user", ErrMalformed)
		}
		seen[m.Users[i].Username] = true
	}
	for i := range decodeRoomCount(d, n, 4) {
		m.Users[i].Status, m.Users[i].StatusKnown = decodeUserStatus(d), true
	}
	for i := range decodeRoomCount(d, n, 20) {
		m.Users[i].Stats, m.Users[i].StatsKnown = decodeUserStats(d), true
	}
	for i := range decodeRoomCount(d, n, 4) {
		m.Users[i].SlotsFull, m.Users[i].SlotsKnown = d.U32(), true
	}
	for i := range decodeRoomCount(d, n, 4) {
		m.Users[i].Country, m.Users[i].CountryKnown = decodeRoomCountry(d), true
	}
	if d.Remaining() > 0 {
		m.Private = true
		m.Owner = d.String()
		// An empty owner field does not establish ownership.
		if m.Owner != "" {
			if err := ValidateUsername(m.Owner); err != nil {
				return m, err
			}
		}
		m.Operators = make([]string, decodeRoomCount(d, MaxRoomUsers, 4))
		for i := range m.Operators {
			m.Operators[i] = decodeUsername(d)
		}
	}
	return m, d.Done()
}
func DecodeRoomLeft(payload []byte) (RoomLeft, error) {
	d := NewDecoder(payload)
	m := RoomLeft{Room: decodeRoomName(d)}
	return m, d.Done()
}
func DecodeRoomUserJoined(payload []byte) (RoomUserJoined, error) {
	d := NewDecoder(payload)
	m := RoomUserJoined{Room: decodeRoomName(d)}
	m.User.Username = decodeUsername(d)
	m.User.Status = decodeUserStatus(d)
	m.User.Stats = decodeUserStats(d)
	m.User.SlotsFull = d.U32()
	m.User.Country = decodeRoomCountry(d)
	m.User.StatusKnown, m.User.StatsKnown, m.User.SlotsKnown, m.User.CountryKnown = true, true, true, true
	return m, d.Done()
}
func DecodeRoomUserLeft(payload []byte) (RoomUserLeft, error) {
	d := NewDecoder(payload)
	m := RoomUserLeft{Room: decodeRoomName(d), Username: decodeUsername(d)}
	return m, d.Done()
}
func DecodeRoomMessage(payload []byte, publicFeed bool) (RoomMessage, error) {
	m := RoomMessage{PublicFeed: publicFeed}
	if len(payload) > MaxRoomNameBytes+MaxUsernameBytes+MaxChatBytes+12 {
		return m, ErrTooLarge
	}
	d := NewDecoder(payload)
	m.Room = decodeRoomName(d)
	m.Username = decodeUsername(d)
	m.Text = d.String()
	if len(m.Text) > MaxChatBytes {
		return m, ErrTooLarge
	}
	return m, d.Done()
}
func decodeRoomPopulations(d *Decoder, maximum int) []RoomPopulation {
	n := decodeRoomCount(d, maximum, 4)
	rooms := make([]RoomPopulation, n)
	for i := range rooms {
		rooms[i].Room = decodeRoomName(d)
	}
	for i := range decodeRoomCount(d, n, 4) {
		rooms[i].Users, rooms[i].UsersKnown = d.U32(), true
	}
	return rooms
}
func DecodeRoomDirectory(payload []byte) (RoomDirectory, error) {
	d := NewDecoder(payload)
	var m RoomDirectory
	m.Public = decodeRoomPopulations(d, MaxRoomEntries)
	m.Owned = decodeRoomPopulations(d, MaxRoomEntries-len(m.Public))
	m.Member = decodeRoomPopulations(d, MaxRoomEntries-len(m.Public)-len(m.Owned))
	m.Operated = make([]string, decodeRoomCount(d, MaxRoomEntries-len(m.Public)-len(m.Owned)-len(m.Member), 4))
	for i := range m.Operated {
		m.Operated[i] = decodeRoomName(d)
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
