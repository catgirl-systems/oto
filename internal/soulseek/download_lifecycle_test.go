package soulseek

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

type blockingDownloadWriter struct {
	started chan struct{}
	release chan struct{}
}

func (w *blockingDownloadWriter) WriteAt(p []byte, _ int64) (int, error) {
	close(w.started)
	<-w.release
	return len(p), nil
}

func TestDownloadCancellationWaitsForWriter(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client := NewClient(ClientConfig{})
	defer client.Close()
	address := listener.Addr().(*net.TCPAddr)
	lookup := &peerAddressLookup{done: make(chan struct{}), address: PeerAddress{Username: "peer", IP: "127.0.0.1", Port: uint32(address.Port)}}
	close(lookup.done)
	client.addresses["peer"] = lookup
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	writer := &blockingDownloadWriter{started: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		select {
		case <-writer.release:
		default:
			close(writer.release)
		}
	}()
	result := make(chan error, 1)
	go func() { result <- client.Download(ctx, "peer", "song", 4, 0, writer, nil) }()
	_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(time.Second))
	peer, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	_ = peer.SetDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := ReadInitFrame(peer); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadFrame(peer); err != nil {
		t.Fatal(err)
	}
	if err := writeMessage(peer, TransferRequest{Direction: 1, Token: 7, Filename: "song", Size: 4}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadFrame(peer); err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	_ = right.SetDeadline(time.Now().Add(2 * time.Second))
	go client.serveFile(left)
	var token [4]byte
	binary.LittleEndian.PutUint32(token[:], 7)
	if _, err := right.Write(token[:]); err != nil {
		t.Fatal(err)
	}
	var offset [8]byte
	if _, err := io.ReadFull(right, offset[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := right.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-writer.started:
	case <-ctx.Done():
		t.Fatal("file writer did not start")
	}
	cancel()
	select {
	case err := <-result:
		t.Fatalf("Download returned before its writer stopped: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(writer.release)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Download failed to release stopped writer")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.requested) != 0 || len(client.downloads) != 0 {
		t.Fatal("cancelled download left pending requests")
	}
}

func TestShortDownloadPreservesReadError(t *testing.T) {
	var target bytes.Buffer
	err := copyAtMost(context.Background(), &target, bytes.NewBufferString("ab"), 4, 0, nil)
	if !errors.Is(err, ErrMalformed) || !errors.Is(err, io.ErrUnexpectedEOF) || target.String() != "ab" {
		t.Fatalf("short download must retain partial data and retryable error: %q %v", target.String(), err)
	}
}
