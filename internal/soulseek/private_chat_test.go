package soulseek

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestCommunityPrivateProtocol(t *testing.T) {
	for _, name := range []string{"pm-online", "pm-offline"} {
		fixture := communityFixture(t, name)
		payload := fixture.Payload(t)
		decoded, err := DecodeServerMessage(fixture.Code, payload)
		want := PrivateMessage{ID: 42, Timestamp: 1700000000, Username: "Alice", Text: "hello 世界", New: name == "pm-online"}
		failIfFmt(t, err != nil || decoded != want, "%s: %+v, %v", name, decoded, err)
		for n := range len(payload) {
			if _, err := DecodePrivateMessage(payload[:n]); err == nil {
				t.Fatalf("accepted truncated PM at %d", n)
			}
		}
		if _, err := DecodePrivateMessage(append(bytes.Clone(payload), 0)); err == nil {
			t.Fatal("accepted trailing PM data")
		}
	}
	for name, message := range map[string]Message{
		"pm-send": PrivateMessageRequest{Username: "Alice", Text: "hello 世界"},
		"pm-ack":  PrivateMessageAck{ID: 42},
	} {
		got, err := EncodeMessage(message)
		fixture := communityFixture(t, name)
		code, payload, decodeErr := ReadFrame(bytes.NewReader(got))
		failIfFmt(t, err != nil || decodeErr != nil || code != fixture.Code || !bytes.Equal(payload, fixture.Payload(t)), "%s differs from independent reference: %x, %v, %v", name, got, err, decodeErr)
	}
	for _, text := range []string{"", " \n\t", "\xff", strings.Repeat("é", MaxChatBytes/2+1)} {
		if _, err := EncodeMessage(PrivateMessageRequest{Username: "Alice", Text: text}); err == nil {
			t.Fatal("accepted invalid outgoing chat text")
		}
	}
	var legacy Encoder
	legacy.U32(1)
	legacy.U32(2)
	_ = legacy.String("Alice")
	_ = legacy.String("caf\xe9")
	legacy.Bool(false)
	if m, err := DecodePrivateMessage(legacy.Payload()); err != nil || m.Text != "caf\xe9" {
		t.Fatalf("lost legacy wire bytes: %+v %v", m, err)
	}
	for _, text := range []string{strings.Repeat("x", MaxChatBytes+1), strings.Repeat("x", MaxChatBytes+MaxUsernameBytes+18)} {
		legacy = Encoder{}
		legacy.U32(1)
		legacy.U32(2)
		_ = legacy.String("Alice")
		_ = legacy.String(text)
		legacy.Bool(true)
		if _, err := DecodePrivateMessage(legacy.Payload()); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("unbounded incoming chat: %v", err)
		}
	}
}

func TestCommunityPrivateAcknowledgementBarrier(t *testing.T) {
	for _, mode := range []string{"commit", "store-fails", "cancel", "no-handler"} {
		t.Run(mode, func(t *testing.T) {
			left, right := net.Pipe()
			defer right.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			entered, commit := make(chan struct{}), make(chan struct{})
			storeErr := errors.New("storage unavailable")
			cfg := ClientConfig{SocialUpdate: func(ctx context.Context, message SocialMessage) error {
				if _, ok := message.(PrivateMessage); !ok {
					return errors.New("wrong message type")
				}
				close(entered)
				select {
				case <-commit:
				case <-ctx.Done():
					return ctx.Err()
				}
				if mode == "store-fails" {
					return storeErr
				}
				return nil
			}}
			if mode == "no-handler" {
				cfg.SocialUpdate = nil
			}
			client := NewClientOnConn(cfg, left)
			defer client.Close()
			run := make(chan error, 1)
			go func() { run <- client.Run(ctx) }()
			fixture := communityFixture(t, "pm-offline")
			_ = right.SetWriteDeadline(time.Now().Add(time.Second))
			must(t, WriteFrame(right, fixture.Code, fixture.Payload(t)))
			if mode != "no-handler" {
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			_ = right.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
			if _, _, err := ReadFrame(right); err == nil {
				t.Fatal("ACK preceded durable acceptance")
			}
			_ = right.SetReadDeadline(time.Now().Add(time.Second))
			close(commit)
			if mode == "commit" {
				code, payload, err := ReadFrame(right)
				failIfFmt(t, err != nil || code != ServerPrivateAck || !bytes.Equal(payload, communityFixture(t, "pm-ack").Payload(t)), "missing ACK after commit: %d %x %v", code, payload, err)
			}
			if mode != "store-fails" {
				cancel()
			}
			select {
			case err := <-run:
				failIf(t, mode == "store-fails" && !errors.Is(err, storeErr), err)
			case <-ctx.Done():
				failIf(t, ctx.Err() == context.DeadlineExceeded, "PM callback/ACK did not stop")
				select {
				case <-run:
				case <-time.After(time.Second):
					t.Fatal("PM callback/ACK did not cancel")
				}
			}
			for len(client.events) > 0 {
				if event := <-client.events; event.Message != nil {
					t.Fatal("private content entered diagnostics")
				}
			}
		})
	}
}

type interruptedPrivateConn struct{ net.Conn }

func (c interruptedPrivateConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p[:min(len(p), 7)])
	if err == nil {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func TestCommunityPrivateWriteStages(t *testing.T) {
	for _, mode := range []string{"cancel-before", "prepare-fails", "cancel-after-prepare", "partial", "complete"} {
		t.Run(mode, func(t *testing.T) {
			left, right := net.Pipe()
			defer right.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var conn net.Conn = left
			if mode == "partial" {
				conn = interruptedPrivateConn{left}
			}
			client := NewClientOnConn(ClientConfig{}, conn)
			defer client.Close()
			var prepared bool
			prepareErr := errors.New("commit failed")
			prepare := func() error {
				prepared = true
				if mode == "prepare-fails" {
					return prepareErr
				}
				if mode == "cancel-after-prepare" {
					cancel()
				}
				return nil
			}
			if mode == "cancel-before" {
				cancel()
			}
			read := make(chan error, 1)
			if mode == "partial" || mode == "complete" {
				go func() {
					_, _, err := ReadFrame(right)
					read <- err
				}()
			}
			attempted, err := client.SendPrivateMessage(ctx, "Alice", "hello 世界", prepare)
			failIfFmt(t, attempted != (mode == "partial" || mode == "complete") || prepared != (mode != "cancel-before"), "wrong write stage: attempted %t, prepared %t, err %v", attempted, prepared, err)
			switch mode {
			case "complete":
				failIf(t, err != nil || <-read != nil, "complete write failed")
			case "partial":
				failIf(t, !errors.Is(err, io.ErrUnexpectedEOF) || <-read == nil, "partial frame did not retire transport")
			case "prepare-fails":
				failIf(t, !errors.Is(err, prepareErr), err)
			default:
				failIf(t, !errors.Is(err, context.Canceled), err)
			}
		})
	}
}

func FuzzCommunityPrivateDecode(f *testing.F) {
	for _, name := range []string{"pm-online", "pm-offline"} {
		f.Add(communityFixture(f, name).Payload(f))
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		_, _ = DecodePrivateMessage(payload)
	})
}
