package soulseek

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSharePolicyAdmissionPublicationFence(t *testing.T) {
	var calls atomic.Int32
	c := permissionTestClient(t, func(string, netip.Addr) SharePermission {
		if calls.Add(1) == 1 {
			return SharePermission{Roots: map[string]ShareVisibility{"Public": ShareAllowed}}
		}
		return SharePermission{Banned: true}
	})
	defer c.Close()
	_, created, err := c.registerUploadWithAddress("Alice", "Public/song.mp3", false, netip.MustParseAddr("127.0.0.1"), false, "")
	if err == nil || created || calls.Load() != 2 || len(c.uploads) != 0 {
		t.Fatal("publication fence", created, err, calls.Load())
	}
	c.cfg.Uploads.mu.Lock()
	defer c.cfg.Uploads.mu.Unlock()
	if c.cfg.Uploads.outstandingFiles["Alice"] != 0 {
		t.Fatal("denied reservation retained accounting")
	}
}

func TestSharePolicyRevokesWaitingUpload(t *testing.T) {
	var allow atomic.Bool
	allow.Store(true)
	c := permissionTestClient(t, func(string, netip.Addr) SharePermission {
		if allow.Load() {
			return SharePermission{Roots: map[string]ShareVisibility{"Public": ShareAllowed}}
		}
		return SharePermission{Banned: true, Reason: "Unavailable"}
	})
	defer c.Close()
	c.cfg.UploadsReady = make(chan struct{})
	a, created, err := c.registerUploadWithAddress("Alice", "Public/song.mp3", false, netip.MustParseAddr("127.0.0.1"), true, "")
	if err != nil || !created {
		t.Fatal(err)
	}
	allow.Store(false)
	c.RevalidateSharePolicy()
	if a.ctx.Err() == nil {
		t.Fatal("waiting upload not revoked")
	}
	a.mu.Lock()
	notify, reason := a.notify, a.policyReason
	a.mu.Unlock()
	if !notify || reason != "Unavailable" {
		t.Fatal("queued peer notification missing", notify, reason)
	}
	select {
	case <-a.done:
	case <-time.After(3 * time.Second):
		t.Fatal("revoked worker did not finish")
	}
}

type shareWriteSignal struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func (s *shareWriteSignal) Write(p []byte) (int, error) {
	s.once.Do(func() { close(s.started) })
	return s.Conn.Write(p)
}

func TestSharePolicyCancelsObsoleteResponses(t *testing.T) {
	c := permissionTestClient(t, nil)
	defer c.Close()
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	peer := &shareWriteSignal{Conn: left, started: make(chan struct{})}
	ctx := c.shareResponseContext()
	done := make(chan error, 1)
	go func() { done <- writeShareMessage(ctx, peer, SharedListResponse{}) }()
	select {
	case <-peer.started:
	case <-time.After(time.Second):
		t.Fatal("response never started")
	}
	c.RevalidateSharePolicy()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("obsolete response completed")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked response retained")
	}
	if ctx.Err() == nil || c.shareResponseContext().Err() != nil {
		t.Fatal("response generation not renewed")
	}
	if err := writeShareMessage(ctx, peer, SharedListResponse{}); !errors.Is(err, context.Canceled) {
		t.Fatal("obsolete snapshot was reused", err)
	}
}

func TestShareAddressStallRetiresTokenlessConnection(t *testing.T) {
	c := NewClient(ClientConfig{})
	defer c.Close()
	left, right := net.Pipe()
	defer right.Close()
	c.conn = left
	c.addresses["Alice"] = &peerAddressLookup{done: make(chan struct{}), started: time.Now().Add(-2 * time.Minute)}
	if _, err := c.lookupPeerAddress(context.Background(), "Bob"); !errors.Is(err, ErrNotConnected) {
		t.Fatal("stalled correlation reused", err)
	}
	if _, err := right.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatal("old connection retained", err)
	}
}

func TestSharePolicyRevocationFencesBatchBeforeCancellation(t *testing.T) {
	var banned, early atomic.Bool
	c := permissionTestClient(t, func(string, netip.Addr) SharePermission {
		return SharePermission{Roots: map[string]ShareVisibility{"Public": ShareAllowed}, Banned: banned.Load(), Reason: "Policy changed"}
	})
	defer c.Close()
	c.cfg.UploadsReady = make(chan struct{})
	var attempts []*uploadAttempt
	for _, user := range []string{"Alice", "Bob", "Carol"} {
		a, _, err := c.registerUploadWithAddress(user, "Public/song.mp3", false, netip.MustParseAddr("127.0.0.1"), true, "")
		if err != nil {
			t.Fatal(err)
		}
		attempts = append(attempts, a)
	}
	for _, a := range attempts {
		cancel := a.cancel
		a.cancel = func() {
			c.cfg.Uploads.mu.Lock()
			for _, other := range attempts {
				if !other.job.cancelled {
					early.Store(true)
				}
			}
			c.cfg.Uploads.mu.Unlock()
			cancel()
		}
	}
	banned.Store(true)
	c.RevalidateSharePolicy()
	for _, a := range attempts {
		select {
		case <-a.done:
		case <-time.After(3 * time.Second):
			t.Fatal("revoked batch worker retained")
		}
		if a.state != "cancelled" || !a.notify || a.policyReason != "Policy changed" {
			t.Fatal("lost queued denial", a.state, a.notify, a.policyReason)
		}
	}
	if early.Load() {
		t.Fatal("cancelled worker before fencing the full scheduler batch")
	}
}
