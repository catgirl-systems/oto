package soulseek

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestUploadQueuePositionRequestsFollowCurrentPriority(t *testing.T) {
	c := permissionTestClient(t, nil)
	defer c.Close()
	c.cfg.UploadsReady = make(chan struct{})
	blocker := c.cfg.Uploads.Enqueue("active", TransferRequest{})
	defer c.cfg.Uploads.Done(blocker)
	for _, user := range []string{"Alice", "Bob"} {
		if _, _, err := c.registerUploadWithAddress(user, "Public/song.mp3", false, netip.MustParseAddr("127.0.0.1"), true, ""); err != nil {
			t.Fatal(err)
		}
	}
	c.SetUploadUserPolicies(map[string]UploadUserPolicy{"Bob": {Preferred: true}})
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	_ = right.SetDeadline(time.Now().Add(3 * time.Second))
	done := make(chan struct{})
	go func() { defer close(done); c.serveMessagePeer(left, PeerInitMessage{Username: "Alice", Type: "P"}) }()
	fixture := communityFixture(t, "queue-position-request")
	check := func(want uint32) {
		t.Helper()
		must(t, WriteFrame(right, fixture.Code, fixture.Payload(t)))
		code, payload, err := ReadFrame(right)
		must(t, err)
		d := NewDecoder(payload)
		name := d.String()
		position := d.U32()
		failIf(t, code != PeerPlaceInQueue || d.Done() != nil || name != "Public\\song.mp3" || position != want, code, name, position, want, d.Done())
	}
	check(2)
	c.SetUploadUserPolicies(map[string]UploadUserPolicy{"Alice": {Preferred: true}})
	check(1)
	_ = right.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("queue-position peer retained")
	}
}

func TestCommunityPrivilegeCallbackPrecedesPeerRouting(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	defer listener.Close()
	fixture := communityFixture(t, "supporter-connection")
	payload := fixture.Payload(t)
	// Only replace the frozen reference fixture's port with this test's owned listener.
	binary.LittleEndian.PutUint32(payload[22:26], uint32(listener.Addr().(*net.TCPAddr).Port))
	left, right := net.Pipe()
	defer right.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stop := errors.New("authority checked")
	var c *Client
	c = NewClientOnConn(ClientConfig{SocialUpdate: func(_ context.Context, message SocialMessage) error {
		if m, ok := message.(ConnectPeerInstruction); !ok || !m.Privileged || m.Username != "Supporter" {
			return errors.New("missing authoritative privilege")
		}
		_ = c.PublicIP() // Callback remains outside client locks.
		select {
		case <-c.Events():
			return errors.New("peer routed before privilege publication")
		default:
		}
		return stop
	}}, left)
	defer c.Close()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	must(t, WriteFrame(right, fixture.Code, payload))
	select {
	case err := <-done:
		failIf(t, !errors.Is(err, stop), err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestUploadRecoveryCoordinatorRenewsAfterClose(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	defer listener.Close()
	ready := make(chan struct{})
	m := NewUploadManager(1)
	c := NewClient(ClientConfig{Address: listener.Addr().String(), Uploads: m, UploadsReady: ready})
	if err := c.Close(); err != nil {
		t.Fatal(err)
	} // Recovery was never released.
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	must(t, c.Connect(ctx))
	m.SetUserPolicies(map[string]UploadUserPolicy{"buddy": {Preferred: true}})
	normal := m.EnqueueRestored("normal", TransferRequest{})
	buddy := m.EnqueueRestored("buddy", TransferRequest{})
	failIf(t, uploadReady(normal) || uploadReady(buddy), "reconnected recovery reserved slots early")
	close(ready)
	select {
	case <-buddy.Ready:
	case <-ctx.Done():
		t.Fatal("reconnected recovery retained cancelled coordinator")
	}
	m.Done(buddy)
	m.Done(normal)
}
