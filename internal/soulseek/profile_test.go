package soulseek

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestCommunityProfileReferenceAndBounds(t *testing.T) {
	f := communityFixture(t, "profile")
	m, err := DecodePeerProfile(f.Payload(t))
	failIf(t, err != nil || m.Description != "hello 世界" || m.UploadSlots != 3 || m.QueueLength != 7 || !m.SlotsAvailable || !m.UploadAllowedKnown || m.UploadAllowed != 0, "reference profile", err, m)
	frame, err := EncodeMessage(m)
	failIf(t, err != nil || binary.LittleEndian.Uint32(frame[4:]) != f.Code || !bytes.Equal(frame[8:], f.Payload(t)), "profile encoder differs from independent fixture", err)
	request, err := EncodeMessage(UserInfoRequest{})
	if err != nil || !bytes.Equal(request, []byte{4, 0, 0, 0, 15, 0, 0, 0}) {
		t.Fatal("nonempty profile request", err)
	}
	legacy := f.Payload(t)[:len(f.Payload(t))-4]
	for _, extra := range [][]byte{nil, {0, 0, 0}} {
		old, err := DecodePeerProfile(append(append([]byte{}, legacy...), extra...))
		if err != nil || old.UploadAllowedKnown {
			t.Fatal("legacy peer inferred unsolicited permission", err)
		}
	}
	for _, extra := range [][]byte{{0}, {0, 0}, {0, 1, 0}, {0, 0, 0, 0, 0}} {
		if _, err := DecodePeerProfile(append(append([]byte{}, legacy...), extra...)); err == nil {
			t.Fatal("bad extension accepted")
		}
	}
	if _, err := DecodePeerProfile(make([]byte, MaxProfileFrameBytes+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatal("frame bound", err)
	}
	for _, profile := range []PeerProfile{{Description: strings.Repeat("a", MaxProfileDescriptionBytes+1)}, {Picture: make([]byte, MaxProfilePictureBytes+1)}} {
		if _, err := EncodeMessage(profile); !errors.Is(err, ErrTooLarge) {
			t.Fatal("unbounded output", err)
		}
	}
	// Independent layout of an opaque picture field, including its upper bound.
	pic := bytes.Repeat([]byte{0x89}, MaxProfilePictureBytes)
	payload := []byte{0, 0, 0, 0, 1}
	payload = binary.LittleEndian.AppendUint32(payload, uint32(len(pic)))
	payload = append(payload, pic...)
	payload = append(payload, make([]byte, 13)...)
	withPic, err := DecodePeerProfile(payload)
	if err != nil || !bytes.Equal(withPic.Picture, pic) {
		t.Fatal("bounded picture decode", err)
	}
	oversized := append([]byte{}, payload...)
	binary.LittleEndian.PutUint32(oversized[5:9], MaxProfilePictureBytes+1)
	oversized = append(oversized, 0)
	if _, err := DecodePeerProfile(oversized); !errors.Is(err, ErrTooLarge) {
		t.Fatal("picture field bound", err)
	}
}

func TestCommunityProfileServingAndCancellation(t *testing.T) {
	client := NewClient(ClientConfig{Username: "local", Uploads: NewUploadManager(3)})
	defer client.Close()
	if err := client.SetSelfDescription("hello 世界"); err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	done := make(chan struct{})
	go func() { defer close(done); client.serveMessagePeer(b, PeerInitMessage{Username: "peer", Type: "P"}) }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	profile, err := requestPeerProfile(ctx, a)
	if err != nil || profile.Description != "hello 世界" || profile.UploadSlots != 3 || !profile.SlotsAvailable || profile.UploadAllowed != 0 {
		t.Fatal("served profile", err, profile)
	}
	if err := client.SetSelfDescription("\x1b[2J"); err == nil || client.SelfProfile().Description != profile.Description {
		t.Fatal("invalid description published")
	}
	_ = a.Close()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("profile connection leaked")
	}
	for _, stage := range []string{"write", "read"} {
		t.Run(stage, func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { _, err := requestPeerProfile(ctx, a); done <- err }()
			if stage == "read" {
				if _, _, err := ReadFrame(b); err != nil {
					t.Fatal(err)
				}
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation", err)
				}
			case <-time.After(time.Second):
				t.Fatal("profile I/O did not cancel")
			}
		})
	}
	// Reject a declared oversize frame before waiting for its body.
	a, b = net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() {
		_, _, _ = ReadFrame(b)
		header := binary.LittleEndian.AppendUint32(nil, MaxProfileFrameBytes+1)
		_, _ = b.Write(header)
	}()
	if _, err := requestPeerProfile(ctx, a); !errors.Is(err, ErrTooLarge) {
		t.Fatal("allocation guard", err)
	}
}

func FuzzCommunityProfileDecode(f *testing.F) {
	f.Add(communityFixture(f, "profile").Payload(f))
	f.Fuzz(func(t *testing.T, payload []byte) {
		m, err := DecodePeerProfile(payload)
		failIf(t, err == nil && (len(m.Picture) > MaxProfilePictureBytes || len(m.Description) > MaxProfileDescriptionBytes), "profile bounds")
	})
}
