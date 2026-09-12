package soulseek

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCommunityRoomProtocol(t *testing.T) {
	alice := RoomUser{Username: "Alice", Status: UserStatusOnline, Stats: UserStats{4096, 3, 0, 12, 2}, Country: "NL", StatusKnown: true, StatsKnown: true, SlotsKnown: true, CountryKnown: true}
	bob := RoomUser{Username: "Bob", Status: UserStatusAway, Stats: UserStats{1024, 2, 99, 10, 1}, SlotsFull: 1, Country: "DE", StatusKnown: true, StatsKnown: true, SlotsKnown: true, CountryKnown: true}
	for name, want := range map[string]any{
		"room-joined":            RoomJoined{Room: "oto test", Users: []RoomUser{alice, bob}},
		"room-private-joined":    RoomJoined{Room: "quiet", Users: []RoomUser{alice, bob}, Private: true, Owner: "Owner", Operators: []string{"Alice", "Op"}},
		"room-partial-joined":    RoomJoined{Room: "oto test", Users: []RoomUser{{Username: "Alice", Status: UserStatusOnline, StatusKnown: true}, {Username: "Bob"}}},
		"room-user-joined":       RoomUserJoined{Room: "oto test", User: bob},
		"room-user-left":         RoomUserLeft{Room: "oto test", Username: "Bob"},
		"room-left":              RoomLeft{Room: "oto test"},
		"room-echo":              RoomMessage{Room: "oto test", Username: "Alice", Text: "hello 世界"},
		"public-feed-message":    RoomMessage{Room: "oto test", Username: "Bob", Text: "caf\xe9", PublicFeed: true},
		"room-directory":         RoomDirectory{Public: []RoomPopulation{{"oto test", 2, true}, {"Music", 42, true}}, Owned: []RoomPopulation{{"quiet", 3, true}}, Member: []RoomPopulation{{"members", 5, true}}, Operated: []string{"quiet"}},
		"room-partial-directory": RoomDirectory{Public: []RoomPopulation{{"one", 7, true}, {"two", 0, false}}, Owned: []RoomPopulation{}, Member: []RoomPopulation{}, Operated: []string{}},
	} {
		t.Run(name, func(t *testing.T) {
			f := communityFixture(t, name)
			p := f.Payload(t)
			got, err := DecodeServerMessage(f.Code, p)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("reference fields: %#v %v", got, err)
			}
			if _, ok := got.(SocialMessage); !ok {
				t.Fatal("not authoritative")
			}
			for n := range len(p) {
				// Omitting the complete optional owner/operator suffix is a valid public join.
				if name == "room-private-joined" && n == len(p)-28 {
					continue
				}
				if _, err := DecodeServerMessage(f.Code, p[:n]); err == nil {
					t.Fatalf("accepted truncation at %d", n)
				}
			}
			if _, err := DecodeServerMessage(f.Code, append(bytes.Clone(p), 0)); err == nil {
				t.Fatal("trailing bytes")
			}
		})
	}
	for name, msg := range map[string]Message{
		"room-join-public":        JoinRoomRequest{Room: "oto test"},
		"room-join-private":       JoinRoomRequest{Room: "oto test", Private: true},
		"room-leave":              LeaveRoomRequest{Room: "oto test"},
		"room-send":               RoomMessageRequest{Room: "oto test", Text: "hello 世界"},
		"room-directory-request":  RoomListRequest{},
		"public-feed-subscribe":   PublicFeedRequest{Subscribe: true},
		"public-feed-unsubscribe": PublicFeedRequest{},
	} {
		encoded, err := EncodeMessage(msg)
		code, p, decodeErr := ReadFrame(bytes.NewReader(encoded))
		f := communityFixture(t, name)
		if err != nil || decodeErr != nil || code != f.Code || !bytes.Equal(p, f.Payload(t)) {
			t.Fatal("reference request differs", name, err, decodeErr)
		}
	}
	// Peer 16 is a profile response, never a room user arrival.
	f := communityFixture(t, "profile")
	if msg, err := DecodeMessage(f.Code, f.Payload(t)); err != nil {
		t.Fatal(err)
	} else if _, ok := msg.(SocialMessage); ok {
		t.Fatal("peer/server command collision")
	}
}

func TestCommunityRoomValidationAndBounds(t *testing.T) {
	for _, room := range []string{"", " leading", "trailing ", "two  spaces", "tab\tname", "line\nname", "control\x1b", "DEL\x7f", "café", strings.Repeat("x", 25)} {
		if err := ValidateRoomName(room); err == nil {
			t.Fatalf("invalid room accepted: %q", room)
		}
		for _, msg := range []Message{JoinRoomRequest{Room: room}, LeaveRoomRequest{Room: room}, RoomMessageRequest{Room: room, Text: "hello"}} {
			if _, err := EncodeMessage(msg); err == nil {
				t.Fatal("encoder bypassed validation")
			}
		}
	}
	for _, room := range []string{"a", "Music", "music", "! a + b ?", strings.Repeat("x", 24)} {
		if err := ValidateRoomName(room); err != nil {
			t.Fatal(err)
		}
	}
	for _, text := range []string{"", " \t", "\xff", "two\nlines", "two\rlines", strings.Repeat("x", MaxChatBytes+1)} {
		if _, err := EncodeMessage(RoomMessageRequest{Room: "test", Text: text}); err == nil {
			t.Fatal("invalid room text accepted")
		}
	}
	p := communityFixture(t, "room-joined").Payload(t)
	binary.LittleEndian.PutUint32(p[12:16], MaxRoomUsers+1)
	if _, err := DecodeRoomJoined(p); !errors.Is(err, ErrTooLarge) {
		t.Fatal("unbounded roster", err)
	}
	p = communityFixture(t, "room-joined").Payload(t)
	binary.LittleEndian.PutUint32(p[32:36], 3) // Three statuses for two usernames.
	if _, err := DecodeRoomJoined(p); err == nil {
		t.Fatal("overlong parallel array")
	}
	p = communityFixture(t, "room-directory").Payload(t)
	binary.LittleEndian.PutUint32(p[:4], MaxRoomEntries+1)
	if _, err := DecodeRoomDirectory(p); !errors.Is(err, ErrTooLarge) {
		t.Fatal("unbounded directory", err)
	}
	var e Encoder
	_ = e.String("test")
	e.U32(2)
	_ = e.String("Alice")
	_ = e.String("Alice")
	for range 4 {
		e.U32(0)
	}
	if _, err := DecodeRoomJoined(e.Payload()); err == nil {
		t.Fatal("duplicate roster identity")
	}
	e = Encoder{}
	_ = e.String("test")
	_ = e.String("Alice")
	_ = e.String(strings.Repeat("x", MaxChatBytes+1))
	if _, err := DecodeRoomMessage(e.Payload(), false); !errors.Is(err, ErrTooLarge) {
		t.Fatal("unbounded message", err)
	}
	if _, err := DecodeRoomMessage(make([]byte, MaxChatBytes+MaxUsernameBytes+MaxRoomNameBytes+13), true); !errors.Is(err, ErrTooLarge) {
		t.Fatal("unbounded feed", err)
	}
}

func TestCommunityRoomAuthoritativeDispatchAndPrivacy(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stop := errors.New("storage failed")
	var client *Client
	client = NewClientOnConn(ClientConfig{SocialUpdate: func(ctx context.Context, msg SocialMessage) error {
		client.mu.Lock()
		client.mu.Unlock() // The callback must not hold client locks.
		if got, ok := msg.(RoomMessage); !ok || got.Text != "hello 世界" {
			return errors.New("wrong callback")
		}
		return stop
	}}, left)
	defer client.Close()
	for i := 0; i < cap(client.events); i++ {
		client.events <- Event{}
	}
	run := make(chan error, 1)
	go func() { run <- client.Run(ctx) }()
	f := communityFixture(t, "room-echo")
	_ = right.SetWriteDeadline(time.Now().Add(time.Second))
	if err := WriteFrame(right, f.Code, f.Payload(t)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-run:
		if !errors.Is(err, stop) {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("dropped authoritative room message")
	}
	for len(client.events) > 0 {
		if ev := <-client.events; ev.Message != nil {
			t.Fatal("body entered diagnostics")
		}
	}
	client.route(ServerRoomMessage, RoomMessage{Text: "private room body"})
	if ev := <-client.events; ev.Message != nil {
		t.Fatal("body entered non-full diagnostics")
	}
}

func TestCommunityRoomClientWrites(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c := NewClientOnConn(ClientConfig{}, left)
	defer c.Close()
	for _, name := range []string{"room-join-public", "room-leave", "room-send", "room-directory-request", "public-feed-subscribe", "public-feed-unsubscribe"} {
		expected := communityFixture(t, name)
		read := make(chan error, 1)
		go func() {
			code, p, err := ReadFrame(right)
			if err == nil && (code != expected.Code || !bytes.Equal(p, expected.Payload(t))) {
				err = errors.New("wrong frame")
			}
			read <- err
		}()
		var err error
		var attempted bool
		switch name {
		case "room-join-public":
			attempted, err = c.JoinRoom(ctx, "oto test", false, nil)
		case "room-leave":
			attempted, err = c.LeaveRoom(ctx, "oto test", nil)
		case "room-send":
			attempted, err = c.SendRoomMessage(ctx, "oto test", "hello 世界", nil)
		case "room-directory-request":
			err = c.RequestRoomDirectory(ctx)
			attempted = err == nil
		case "public-feed-subscribe":
			attempted, err = c.SetPublicFeed(ctx, true, nil)
		case "public-feed-unsubscribe":
			attempted, err = c.SetPublicFeed(ctx, false, nil)
		}
		if err != nil || !attempted {
			t.Fatal(name, err)
		}
		select {
		case err := <-read:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	denied := errors.New("no longer joined")
	if attempted, err := c.SendRoomMessage(ctx, "oto test", "late", func() error { return denied }); attempted || !errors.Is(err, denied) {
		t.Fatal("writer reservation guard", err)
	}
	cancel()
	if attempted, err := c.JoinRoom(ctx, "oto test", false, nil); attempted || !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled join wrote", err)
	}
}

func FuzzCommunityRoomDecode(f *testing.F) {
	for _, name := range []string{"room-joined", "room-partial-joined", "room-private-joined", "room-directory", "room-echo", "public-feed-message", "room-user-joined", "room-user-left", "room-left"} {
		fixture := communityFixture(f, name)
		f.Add(fixture.Code, fixture.Payload(f))
	}
	f.Fuzz(func(t *testing.T, code uint32, payload []byte) {
		switch code {
		case ServerJoinRoom, ServerLeaveRoom, ServerRoomMessage, ServerRoomUserJoined, ServerRoomUserLeft, ServerRoomList, ServerPublicFeedMessage:
			_, _ = DecodeServerMessage(code, payload)
		}
	})
}
