package soulseek

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func startSoulfindSocial(t *testing.T, ctx context.Context, addr, name string, update func(context.Context, SocialMessage) error) (*Client, func()) {
	t.Helper()
	client := NewClient(ClientConfig{Address: addr, Username: name, Password: "local-only", ListenAddr: "127.0.0.1:0", SocialUpdate: update})
	connectSoulfind(t, client)
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("social reader failed to stop")
		}
	}
	t.Cleanup(stop)
	return client, stop
}

func TestSoulfindPublicRoomLifecycle(t *testing.T) {
	addr := soulfindAddress(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	suffix := fmt.Sprint(time.Now().UnixNano())
	room := "oto" + suffix
	aliceName, bobName := "rma"+suffix, "rmb"+suffix
	start := func(name string) (*Client, chan SocialMessage) {
		updates := make(chan SocialMessage, 64)
		client, _ := startSoulfindSocial(t, ctx, addr, name, func(ctx context.Context, msg SocialMessage) error {
			select {
			case updates <- msg:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		return client, updates
	}
	wait := func(updates chan SocialMessage, label string, matches func(SocialMessage) bool) SocialMessage {
		for {
			select {
			case msg := <-updates:
				if matches(msg) {
					return msg
				}
				if pm, ok := msg.(PrivateMessage); ok && pm.Username == "server" {
					t.Fatalf("%s: server notice %q", label, pm.Text)
				}
			case <-ctx.Done():
				t.Fatalf("%s: %v", label, ctx.Err())
				return nil
			}
		}
	}
	joined := func(msg SocialMessage) bool { x, ok := msg.(RoomJoined); return ok && x.Room == room }
	message := func(msg SocialMessage) bool {
		x, ok := msg.(RoomMessage)
		return ok && x.Room == room && x.Username == aliceName && x.Text == "hello 世界" && !x.PublicFeed
	}
	alice, a := start(aliceName)
	bob, b := start(bobName)
	_, err := alice.JoinRoom(ctx, room, false, nil)
	must(t, err)
	first := wait(a, "creator joins", joined).(RoomJoined)
	failIf(t, first.Private || len(first.Users) != 1 || first.Users[0].Username != aliceName, "unexpected creator roster", first)
	if _, err := bob.JoinRoom(ctx, room, false, nil); err != nil {
		t.Fatal(err)
	}
	second := wait(b, "second participant joins", joined).(RoomJoined)
	failIf(t, second.Private || len(second.Users) != 2, "unexpected second roster", second)
	wait(a, "participant arrival", func(msg SocialMessage) bool {
		x, ok := msg.(RoomUserJoined)
		return ok && x.Room == room && x.User.Username == bobName
	})
	if _, err := alice.SendRoomMessage(ctx, room, "hello 世界", nil); err != nil {
		t.Fatal(err)
	}
	wait(a, "authoritative own-message echo", message)
	wait(b, "other participant receives", message)
	must(t, bob.RequestRoomDirectory(ctx))
	wait(b, "explicit directory includes small room", func(msg SocialMessage) bool {
		x, ok := msg.(RoomDirectory)
		if !ok {
			return false
		}
		for _, entry := range x.Public {
			if entry.Room == room && entry.Users == 2 {
				return true
			}
		}
		return false
	})
	if _, err := bob.LeaveRoom(ctx, room, nil); err != nil {
		t.Fatal(err)
	}
	wait(b, "leave confirmation", func(msg SocialMessage) bool { x, ok := msg.(RoomLeft); return ok && x.Room == room })
	wait(a, "participant departure", func(msg SocialMessage) bool {
		x, ok := msg.(RoomUserLeft)
		return ok && x.Room == room && x.Username == bobName
	})
	// The subscriber is no longer a member: this must be a public-feed frame,
	// not the ordinary room echo. A same-stream stats reply fences subscription.
	statsReply := func(msg SocialMessage) bool { x, ok := msg.(UserStatistics); return ok && x.Username == aliceName }
	if _, err := bob.SetPublicFeed(ctx, true, nil); err != nil {
		t.Fatal(err)
	}
	must(t, bob.RequestUserStats(ctx, aliceName))
	wait(b, "feed subscription processing barrier", statsReply)
	if _, err := alice.SendRoomMessage(ctx, room, "feed 世界", nil); err != nil {
		t.Fatal(err)
	}
	wait(b, "public feed without membership", func(msg SocialMessage) bool {
		x, ok := msg.(RoomMessage)
		return ok && x.PublicFeed && x.Room == room && x.Username == aliceName && x.Text == "feed 世界"
	})
	if _, err := bob.SetPublicFeed(ctx, false, nil); err != nil {
		t.Fatal(err)
	}
	must(t, bob.RequestUserStats(ctx, aliceName))
	wait(b, "feed unsubscription processing barrier", statsReply)
	if _, err := bob.JoinRoom(ctx, room, false, nil); err != nil {
		t.Fatal(err)
	}
	if again := wait(b, "rejoin", joined).(RoomJoined); len(again.Users) != 2 {
		t.Fatal("rejoin roster", again)
	}
}
