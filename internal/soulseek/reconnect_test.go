package soulseek

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestClientReconnectRenewsLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	log, err := os.Create(filepath.Join(t.TempDir(), "reconnect-debug.log"))
	must(t, err)
	defer log.Close()
	c, uploads, _ := uploadClient(t, PeerAddress{}, []byte("hello"), slog.New(slog.NewTextHandler(log, &slog.HandlerOptions{Level: slog.LevelDebug})))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	defer ln.Close()
	_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second))
	c.cfg.Address = ln.Addr().String()
	seen := make(chan SocialMessage, 8)
	c.cfg.SocialUpdate = func(_ context.Context, message SocialMessage) error { seen <- message; return nil }
	must(t, c.Close())
	for range 3 {
		oldRoot := c.uploadRoot
		must(t, c.Connect(ctx))
		failIf(t, c.uploadRoot == oldRoot || c.uploadRoot.Err() != nil || c.closing, "reconnect retained retired lifecycle")
		server, err := ln.Accept()
		must(t, err)
		t.Cleanup(func() { _ = server.Close() })
		_ = server.SetDeadline(time.Now().Add(5 * time.Second))
		runDone := make(chan struct{})
		go func() { defer close(runDone); _ = c.Run(ctx) }()
		t.Cleanup(func() { _ = c.Close(); <-runDone })
		fixture := communityFixture(t, "status-online")
		must(t, WriteFrame(server, fixture.Code, fixture.Payload(t)))
		select {
		case <-seen:
		case <-runDone:
			t.Fatal("reconnected Run stopped before receiving status")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		left, right := net.Pipe()
		peerDone := make(chan struct{})
		go func() {
			defer close(peerDone)
			defer left.Close()
			c.serveMessagePeer(left, PeerInitMessage{Username: "Alice", Type: "P"})
		}()
		_, profileErr := requestPeerProfile(ctx, right)
		_ = right.Close()
		<-peerDone
		failIfFmt(t, profileErr != nil, "reconnected profile serving: %v", profileErr)
		if _, created, err := c.registerUpload("Alice", "Music\\song", false); err != nil || !created {
			t.Fatalf("reconnected upload admission: created=%v error=%v", created, err)
		}
		if cmd, _, err := ReadFrame(server); err != nil || cmd != ServerGetPeerAddress {
			t.Fatalf("reconnected address lookup: command=%d error=%v", cmd, err)
		}
		must(t, c.Close())
		<-runDone
		_ = server.Close()
		uploadEvent(t, uploads, "failed") // Close still releases blocked uploads.
	}
}

func TestClientCloseCancelsDial(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := NewClient(ClientConfig{Address: "127.0.0.1:1"})
	entered := make(chan struct{})
	c.dialer.ControlContext = func(ctx context.Context, _, _ string, _ syscall.RawConn) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	connected := make(chan error, 1)
	go func() { connected <- c.Connect(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	closed := make(chan struct{})
	go func() { defer close(closed); _ = c.Close() }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel pending dial")
	}
	if err := <-connected; !errors.Is(err, context.Canceled) {
		t.Fatalf("dial error: %v", err)
	}
}

// Pause Close after its cancellation snapshot, before lifecycleMu acquisition.
// A second Close must not clear the first one's shutdown intent either.
func TestClientCloseRejectsRacingDialRegistration(t *testing.T) {
	for _, overlap := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "overlapping"}[overlap], func(t *testing.T) {
			c := NewClient(ClientConfig{Address: "127.0.0.1:1"})
			entered, resume, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
			c.connectCancel = func() {
				c.mu.Lock()
				c.connectCancel = nil // Retired dial; a new registration would otherwise be possible.
				c.mu.Unlock()
				close(entered)
				<-resume
			}
			dialed := make(chan struct{}, 1)
			c.dialer.ControlContext = func(context.Context, string, string, syscall.RawConn) error {
				dialed <- struct{}{}
				return errors.New("unexpected dial during shutdown")
			}
			go func() { defer close(closed); _ = c.Close() }()
			t.Cleanup(func() { close(resume); <-closed })
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("Close did not reach cancellation barrier")
			}
			if overlap {
				_ = c.Close()
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := c.Connect(ctx); err == nil || err.Error() != "soulseek: client is closing" {
				t.Fatalf("Connect during shutdown: %v", err)
			}
			select {
			case <-dialed:
				t.Fatal("dial registered after shutdown's cancellation snapshot")
			default:
			}
		})
	}
}
