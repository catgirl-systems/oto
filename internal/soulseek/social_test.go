package soulseek

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/testutil"
)

func communityFixture(t testing.TB, name string) testutil.WireFixture {
	t.Helper()
	for _, fixture := range testutil.SocialFixtures(t) {
		if fixture.Name == name {
			return fixture
		}
	}
	t.Fatalf("missing Community fixture %s", name)
	return testutil.WireFixture{}
}

func TestCommunityUserProtocol(t *testing.T) {
	stats := UserStats{AverageSpeed: 1000, UploadCount: 12, Files: 99, Directories: 4}
	responses := map[string]any{
		"watch-online":         WatchUserResponse{Username: "Alice", Exists: true, Status: UserStatusOnline, Stats: stats, Country: "FR"},
		"watch-missing":        WatchUserResponse{Username: "alice"},
		"status-away":          UserPresence{Username: "Alice", Status: UserStatusAway, Privileged: true},
		"user-stats":           UserStatistics{Username: "Alice", Stats: stats},
		"peer-address":         PeerAddress{Username: "Alice", IP: "127.0.0.1", Port: 2234},
		"privileged-users":     PrivilegedUsers{Users: []string{"Supporter", "Another"}},
		"supporter-connection": ConnectPeerInstruction{Username: "Supporter", Kind: "P", IP: "127.0.0.1", Port: 2323, Token: 41, Privileged: true},
		"supporter-expired":    UserPresence{Username: "Supporter", Status: UserStatusOnline},
	}
	for name, expected := range responses {
		t.Run(name, func(t *testing.T) {
			fixture := communityFixture(t, name)
			payload := fixture.Payload(t)
			got, err := DecodeServerMessage(fixture.Code, payload)
			if err != nil || !reflect.DeepEqual(got, expected) {
				t.Fatalf("decode: %#v %v", got, err)
			}
			for n := range len(payload) {
				// Country and obfuscation are optional complete suffixes.
				if name == "watch-online" && n == len(payload)-6 || name == "peer-address" && n == len(payload)-6 || name == "supporter-connection" && n == len(payload)-8 {
					continue
				}
				if _, err := DecodeServerMessage(fixture.Code, payload[:n]); err == nil {
					t.Fatalf("accepted truncation at %d", n)
				}
			}
			if _, err := DecodeServerMessage(fixture.Code, append(bytes.Clone(payload), 0)); err == nil {
				t.Fatal("accepted trailing bytes")
			}
		})
	}
	requests := map[string]Message{
		"WatchUser-request":      WatchUserRequest{Username: "Alice"},
		"UnwatchUser-request":    UnwatchUserRequest{Username: "Alice"},
		"GetUserStatus-request":  UserStatusRequest{Username: "Alice"},
		"GetUserStats-request":   UserStatsRequest{Username: "Alice"},
		"GetPeerAddress-request": PeerAddressRequest{Username: "Alice"},
	}
	for name, request := range requests {
		fixture := communityFixture(t, name)
		encoded, err := EncodeMessage(request)
		if err != nil {
			t.Fatal(err)
		}
		command, payload, err := ReadFrame(bytes.NewReader(encoded))
		if err != nil || command != fixture.Code || !bytes.Equal(payload, fixture.Payload(t)) {
			t.Fatalf("%s: %x %v", name, encoded, err)
		}
	}
	fixture := communityFixture(t, "watch-online")
	offline := fixture.Payload(t)
	offline[10] = 0
	m, err := DecodeWatchUser(offline[:len(offline)-6])
	if err != nil || m.Status != UserStatusOffline || m.Country != "" {
		t.Fatalf("legacy offline: %+v %v", m, err)
	}
	invalid := communityFixture(t, "status-away").Payload(t)
	invalid[9] = 3
	if _, err := DecodeUserPresence(invalid); err == nil {
		t.Fatal("accepted invalid status")
	}
	for _, username := range []string{"", "\nAlice", string([]byte{0xff}), string(bytes.Repeat([]byte{'x'}, MaxUsernameBytes+1))} {
		if _, err := EncodeMessage(WatchUserRequest{Username: username}); err == nil {
			t.Fatal("accepted invalid username")
		}
	}
	for _, username := range []string{"Alice", "alice", " Alice ", "猫"} {
		var e Encoder
		if err := encodeUsername(&e, username); err != nil {
			t.Fatal(err)
		}
		if got, err := decodeUsername(NewDecoder(e.Payload())); err != nil || got != username {
			t.Fatalf("identity rewritten: %q %v", got, err)
		}
	}
	// XXX: decoders preserve undecodable wire names; nicotine+ renders whatever
	// it receives and never drops the session over one odd name.
	for _, raw := range []string{"", "\nAlice", string([]byte{0xff})} {
		var e Encoder
		if err := e.String(raw); err != nil {
			t.Fatal(err)
		}
		if got, err := decodeUsername(NewDecoder(e.Payload())); err != nil || got != raw {
			t.Fatalf("wire name dropped: %q %v", got, err)
		}
	}
	for _, raw := range []string{string(bytes.Repeat([]byte{'x'}, MaxUsernameBytes+1))} {
		var e Encoder
		if err := e.String(raw); err != nil {
			t.Fatal(err)
		}
		if _, err := decodeUsername(NewDecoder(e.Payload())); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("oversize name accepted: %v", err)
		}
	}
	// Peer code 5 is a share list, never a server watch response.
	encoded, err := EncodeMessage(SharedListResponse{})
	if err != nil {
		t.Fatal(err)
	}
	cmd, payload, err := ReadFrame(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeMessage(cmd, payload)
	if _, ok := decoded.(SharedListResponse); !ok || err != nil {
		t.Fatalf("peer namespace: %T %v", decoded, err)
	}
}

func TestCommunityDispatchBurst(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var seen atomic.Int32
	const count = 1024
	stop := errors.New("script complete")
	var client *Client
	client = NewClientOnConn(ClientConfig{SocialUpdate: func(ctx context.Context, update SocialMessage) error {
		_ = client.PublicIP() // This acquires client.mu: callback must be unlocked.
		if _, ok := update.(UserStatistics); !ok {
			return fmt.Errorf("wrong update %T", update)
		}
		n := seen.Add(1)
		if n == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if n == count {
			return stop
		}
		return nil
	}}, left)
	defer client.Close()
	run := make(chan error, 1)
	go func() { run <- client.Run(ctx) }()
	fixture := communityFixture(t, "user-stats")
	payload := fixture.Payload(t)
	written := make(chan error, 1)
	go func() {
		for range count {
			if err := WriteFrame(right, fixture.Code, payload); err != nil {
				written <- err
				return
			}
		}
		written <- nil
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-written:
		t.Fatalf("reader did not apply backpressure: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-run:
		if !errors.Is(err, stop) {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if seen.Load() != count || len(client.events) != cap(client.events) {
		t.Fatalf("lost authoritative updates: %d, diagnostics %d", seen.Load(), len(client.events))
	}
}

func TestCommunityDispatchCancellation(t *testing.T) {
	for _, mode := range []string{"callback-cancel", "callback-close", "read-cancel", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			left, right := net.Pipe()
			defer right.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{})
			client := NewClientOnConn(ClientConfig{SocialUpdate: func(ctx context.Context, _ SocialMessage) error {
				close(entered)
				<-ctx.Done()
				return ctx.Err()
			}}, left)
			defer client.Close()
			run := make(chan error, 1)
			go func() { run <- client.Run(ctx) }()
			if mode != "read-cancel" {
				fixture := communityFixture(t, "status-away")
				payload := fixture.Payload(t)
				if mode == "malformed" {
					payload = payload[:len(payload)-1]
				}
				if err := WriteFrame(right, fixture.Code, payload); err != nil {
					t.Fatal(err)
				}
				if mode != "malformed" {
					select {
					case <-entered:
					case <-time.After(time.Second):
						t.Fatal("callback not entered")
					}
				}
			}
			if mode == "callback-close" {
				_ = client.Close()
			} else if mode != "malformed" {
				cancel()
			}
			select {
			case err := <-run:
				if mode == "malformed" {
					if !errors.Is(err, ErrTruncated) {
						t.Fatal(err)
					}
				} else if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("Run did not stop")
			}
		})
	}
}

type lookupReadyContext struct {
	context.Context
	once  sync.Once
	ready chan struct{}
}

func (c *lookupReadyContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.ready) })
	return c.Context.Done()
}
func TestCommunityAddressCancellationAndClose(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	_ = right.SetDeadline(time.Now().Add(3 * time.Second))
	client := NewClientOnConn(ClientConfig{}, left)
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := client.ResolveUserAddress(ctx, "Alice"); first <- err }()
	if cmd, _, err := ReadFrame(right); err != nil || cmd != ServerGetPeerAddress {
		t.Fatalf("request %d %v", cmd, err)
	}
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// A later consumer shares the already-written request, rather than confusing
	// its response with a new request after cancellation.
	second := make(chan error, 1)
	ready := &lookupReadyContext{Context: context.Background(), ready: make(chan struct{})}
	go func() {
		address, err := client.ResolveUserAddress(ready, "Alice")
		if err == nil && address.IP != "127.0.0.1" {
			err = fmt.Errorf("address: %+v", address)
		}
		second <- err
	}()
	select {
	case <-ready.ready:
	case <-time.After(time.Second):
		t.Fatal("lookup sent a duplicate request")
	}
	client.route(ServerGetPeerAddress, PeerAddress{Username: "Alice", IP: "127.0.0.1"})
	select {
	case err := <-second:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shared lookup did not finish")
	}
	pending := make(chan error, 1)
	go func() { _, err := client.ResolveUserAddress(context.Background(), "Bob"); pending <- err }()
	if cmd, _, err := ReadFrame(right); err != nil || cmd != ServerGetPeerAddress {
		t.Fatalf("pending %d %v", cmd, err)
	}
	_ = client.Close()
	select {
	case err := <-pending:
		if !errors.Is(err, ErrNotConnected) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("lookup leaked on close")
	}
}

func FuzzCommunityUserDecode(f *testing.F) {
	for _, name := range []string{"watch-online", "watch-missing", "status-away", "user-stats", "peer-address", "privileged-users", "supporter-connection", "supporter-expired", "privilege-balance", "privilege-balance-empty"} {
		fixture := communityFixture(f, name)
		f.Add(fixture.Code, fixture.Payload(f))
	}
	f.Fuzz(func(t *testing.T, code uint32, payload []byte) {
		switch code {
		case ServerWatchUser, ServerUserStatus, ServerUserStats, ServerGetPeerAddress, ServerPrivilegedUsers, ServerConnectToPeer, ServerCheckPrivileges:
			_, _ = DecodeServerMessage(code, payload)
		}
	})
}

func TestCommunityInterruptedFrameRetiresTransport(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	_ = right.SetDeadline(time.Now().Add(time.Second))
	client := NewClientOnConn(ClientConfig{}, left)
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- client.WatchUser(ctx, "Alice") }()
	prefix := make([]byte, 2)
	if _, err := io.ReadFull(right, prefix); err != nil {
		t.Fatal(err)
	}
	cancel() // The server has only half of the length prefix.
	if err := <-result; err == nil {
		t.Fatal("partial write reported success")
	}
	if n, err := right.Read(prefix); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("partial frame transport reused: %d %v", n, err)
	}
	if err := client.WatchUser(context.Background(), "Bob"); err == nil {
		t.Fatal("new frame sent after a partial frame")
	}
}

func TestCommunityCancelledRequestDoesNotWrite(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	_ = right.SetDeadline(time.Now().Add(time.Second))
	client := NewClientOnConn(ClientConfig{}, left)
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.WatchUser(ctx, "Alice"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- client.WatchUser(context.Background(), "Bob") }()
	cmd, payload, err := ReadFrame(right)
	if err != nil || cmd != ServerWatchUser {
		t.Fatalf("next request: %d %v", cmd, err)
	}
	username, err := NewDecoder(payload).String()
	if err != nil || username != "Bob" {
		t.Fatalf("cancelled request was transmitted: %s %v", username, err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}
