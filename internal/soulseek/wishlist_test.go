package soulseek

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
)

func TestWishlistProtocol(t *testing.T) {
	framed, err := EncodeMessage(WishlistSearchRequest{Token: 7, Query: "rare album"})
	if err != nil {
		t.Fatal(err)
	}
	command, payload, err := ReadFrame(bytes.NewReader(framed))
	if err != nil || command != ServerWishlistSearch {
		t.Fatalf("wishlist command: %d %v", command, err)
	}
	d := NewDecoder(payload)
	token, err := d.U32()
	if err != nil {
		t.Fatal(err)
	}
	query, err := d.String()
	if err != nil || d.Done() != nil || token != 7 || query != "rare album" {
		t.Fatalf("wishlist payload: token=%d query=%q err=%v", token, query, err)
	}

	var encoded Encoder
	encoded.U32(900)
	message, err := DecodeMessage(ServerWishlistInterval, encoded.Payload())
	interval, ok := message.(WishlistInterval)
	if err != nil || !ok || interval.Seconds != 900 {
		t.Fatalf("wishlist interval: %#v %v", message, err)
	}

	client := NewClient(ClientConfig{})
	client.route(ServerWishlistInterval, interval)
	event := <-client.Events()
	if event.Command != ServerWishlistInterval || event.Message != interval {
		t.Fatalf("wishlist route: %#v", event)
	}
}

func TestWishlistSearchUsesAutomaticCommand(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	client := NewClientOnConn(ClientConfig{}, clientConn)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := client.WishlistSearch(ctx, "rare album"); done <- err }()
	command, _, err := ReadFrame(serverConn)
	if err != nil || command != ServerWishlistSearch {
		t.Fatalf("automatic wishlist command: %d %v", command, err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wishlist search: %v", err)
	}
}
