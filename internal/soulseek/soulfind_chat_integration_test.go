package soulseek

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// The existing pinned Soulfind CI job supplies OTO_SOULFIND_ADDR. Scripted
// fixture tests remain mandatory in the ordinary suite independently of this.
func TestSoulfindPrivateChatOnlineOffline(t *testing.T) {
	addr := soulfindAddress(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	suffix := fmt.Sprint(time.Now().UnixNano())
	aliceName, bobName := "pma"+suffix, "pmb"+suffix
	messages := make(chan PrivateMessage, 8)
	statuses := make(chan UserPresence, 16)
	start := func(name string) (*Client, func()) {
		client := NewClient(ClientConfig{Address: addr, Username: name, Password: "local-only", ListenAddr: "127.0.0.1:0", SocialUpdate: func(ctx context.Context, update SocialMessage) error {
			switch x := update.(type) {
			case PrivateMessage:
				select {
				case messages <- x:
				case <-ctx.Done():
					return ctx.Err()
				}
			case UserPresence:
				select {
				case statuses <- x:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}})
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
	_, stopAlice := start(aliceName)
	bob, _ := start(bobName)
	send := func(text string) {
		if attempted, err := bob.SendPrivateMessage(ctx, aliceName, text, nil); !attempted || err != nil {
			t.Fatalf("send to local server: %v, attempted=%t", err, attempted)
		}
	}
	receive := func(text string, online bool) {
		for {
			select {
			case message := <-messages:
				if message.Text != text {
					continue
				} // An unacknowledged prior wire attempt may replay on reconnect.
				if message.Username != bobName || message.New != online || message.Timestamp == 0 {
					t.Fatalf("unexpected private message metadata: %+v", message)
				}
				return
			case <-ctx.Done():
				t.Fatalf("did not receive %s message: %v", text, ctx.Err())
			}
		}
	}
	send("online 世界")
	receive("online 世界", true)
	stopAlice()
	requestStatus := func(username string) UserPresence {
		if err := bob.RequestUserStatus(ctx, username); err != nil {
			t.Fatal(err)
		}
		for {
			select {
			case status := <-statuses:
				if status.Username == username {
					return status
				}
			case <-ctx.Done():
				t.Fatal("no status response from local server")
				return UserPresence{}
			}
		}
	}
	for requestStatus(aliceName).Status != UserStatusOffline {
		select {
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("recipient did not go offline")
		}
	}
	send("offline 世界")
	// Same-stream round trip: the server has handled the PM before login resumes.
	requestStatus(bobName)
	start(aliceName)
	receive("offline 世界", false)
}
