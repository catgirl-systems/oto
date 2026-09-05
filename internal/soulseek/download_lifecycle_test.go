package soulseek

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"testing/synctest"
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
	_ = peer.Close() // Losing the control connection must not own the file writer.
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

func TestDownloadControlConnectionLifetime(t *testing.T) {
	for _, action := range []string{"complete", "resume", "late_file", "short_file", "rejected", "before_accept", "separate_rejection", "separate_failure", "separate_size_change"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second))
			client := NewClient(ClientConfig{})
			defer client.Close()
			lookup := &peerAddressLookup{done: make(chan struct{}), address: PeerAddress{IP: "127.0.0.1", Port: uint32(listener.Addr().(*net.TCPAddr).Port)}}
			close(lookup.done)
			client.addresses["peer"] = lookup
			file, err := os.CreateTemp(t.TempDir(), "partial")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			var prefix []byte
			if action == "resume" {
				prefix = []byte("pre")
				if _, err := file.WriteAt(prefix, 0); err != nil {
					t.Fatal(err)
				}
			}
			chunk := bytes.Repeat([]byte("a"), 32<<10)
			contents := append(append(append([]byte(nil), prefix...), chunk...), []byte("tail")...)
			progress := make(chan Progress, 2)
			result := make(chan error, 1)
			go func() {
				result <- client.Download(ctx, "peer", "song", uint64(len(contents)), uint64(len(prefix)), file, func(p Progress) { progress <- p })
			}()
			peer, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
			_ = peer.SetDeadline(time.Now().Add(5 * time.Second))
			if _, _, err := ReadInitFrame(peer); err != nil {
				t.Fatal(err)
			}
			if command, _, err := ReadFrame(peer); err != nil || command != PeerQueueUpload {
				t.Fatalf("queue request: %d %v", command, err)
			}
			if action == "before_accept" {
				_ = peer.Close()
				if err := <-result; !errors.Is(err, io.EOF) {
					t.Fatalf("unaccepted download lost transport error: %v", err)
				}
				return
			}
			control := peer
			separate := action == "separate_rejection" || action == "separate_failure" || action == "separate_size_change"
			if separate {
				incoming, remote := net.Pipe()
				defer incoming.Close()
				defer remote.Close()
				_ = remote.SetDeadline(time.Now().Add(5 * time.Second))
				go client.serveMessagePeer(incoming, PeerInitMessage{Username: "peer", Type: "P"})
				control = remote
			}
			request := TransferRequest{Direction: 1, Token: 7, Filename: "song", Size: uint64(len(contents))}
			if action == "separate_size_change" {
				request.Size++
			}
			if err := writeMessage(control, request); err != nil {
				t.Fatal(err)
			}
			if action == "separate_size_change" {
				if err := <-result; err == nil || err.Error() != "soulseek: remote file size changed" {
					t.Fatalf("lost incoming setup failure: %v", err)
				}
				return
			}
			if command, _, err := ReadFrame(control); err != nil || command != PeerTransferResponse {
				t.Fatalf("transfer response: %d %v", command, err)
			}
			if action == "late_file" {
				_ = peer.Close()
				select {
				case err := <-result:
					t.Fatalf("control close cancelled accepted setup: %v", err)
				case <-time.After(20 * time.Millisecond):
				}
			}
			left, right := net.Pipe()
			defer left.Close()
			defer right.Close()
			_ = right.SetDeadline(time.Now().Add(5 * time.Second))
			go client.serveFile(left)
			if err := binary.Write(right, binary.LittleEndian, uint32(7)); err != nil {
				t.Fatal(err)
			}
			var offset uint64
			if err := binary.Read(right, binary.LittleEndian, &offset); err != nil || offset != uint64(len(prefix)) {
				t.Fatalf("resume offset: %d %v", offset, err)
			}
			if _, err := right.Write(chunk); err != nil {
				t.Fatal(err)
			}
			select {
			case <-progress:
			case <-ctx.Done():
				t.Fatal("file transfer never started")
			}
			if separate {
				_ = peer.Close()
				select {
				case err := <-result:
					t.Fatalf("original control close cancelled incoming transfer: %v", err)
				case <-time.After(20 * time.Millisecond):
				}
			}
			if action == "separate_failure" {
				if err := writeMessage(control, QueueFailedMessage{Filename: "song"}); err != nil {
					t.Fatal(err)
				}
				if err := <-result; !errors.Is(err, ErrUploadFailed) {
					t.Fatalf("lost incoming upload failure: %v", err)
				}
				return
			}
			if action == "rejected" || action == "separate_rejection" {
				if err := writeMessage(control, QueueDenied{Filename: "song", Reason: "File not shared"}); err != nil {
					t.Fatal(err)
				}
				var rejected *DownloadRejectedError
				if err := <-result; !errors.As(err, &rejected) || rejected.Reason != "File not shared" {
					t.Fatalf("lost explicit rejection: %v", err)
				}
				return
			}
			_ = peer.Close()
			select {
			case err := <-result:
				t.Fatalf("control close cancelled active file: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			tail := []byte("tail")
			if action == "short_file" {
				tail = tail[:2]
				contents = contents[:len(contents)-2]
			}
			if _, err := right.Write(tail); err != nil {
				t.Fatal(err)
			}
			_ = right.Close()
			err = <-result
			if action == "short_file" {
				if !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("short file lost retryable error: %v", err)
				}
			} else if err != nil {
				t.Fatalf("download failed: %v", err)
			}
			if got, err := os.ReadFile(file.Name()); err != nil || !bytes.Equal(got, contents) {
				t.Fatalf("downloaded data mismatch: got %d bytes, want %d: %v", len(got), len(contents), err)
			}
		})
	}
}

func TestDownloadFileSetupDeadline(t *testing.T) {
	for _, phase := range []string{"missing_file", "active_file", "stalled_offset"} {
		synctest.Test(t, func(t *testing.T) {
			client := NewClient(ClientConfig{})
			defer client.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			file, err := os.CreateTemp(t.TempDir(), "partial")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			pending := &pendingDownload{size: 4, writer: file, done: make(chan error, 1), ctx: ctx}
			peer, remote := net.Pipe()
			defer peer.Close()
			defer remote.Close()
			go func() { _, _, _ = ReadFrame(remote) }()
			start := time.Now()
			if err := client.acceptDownload(peer, pending, TransferRequest{Token: 7, Size: 4}); err != nil {
				t.Fatal(err)
			}
			collision := &pendingDownload{size: 4, done: make(chan error, 1), ctx: ctx}
			if err := client.acceptDownload(peer, collision, TransferRequest{Token: 7, Size: 4}); err == nil {
				t.Fatal("accepted a duplicate file connection token")
			}
			if phase == "missing_file" {
				if err := <-pending.done; !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("missing setup timeout: %v", err)
				}
				if elapsed := time.Since(start); elapsed != 45*time.Second {
					t.Fatalf("setup timeout took %v", elapsed)
				}
				client.mu.Lock()
				defer client.mu.Unlock()
				if len(client.downloads) != 0 {
					t.Fatal("expired file token remains registered")
				}
				return
			}
			left, right := net.Pipe()
			defer left.Close()
			defer right.Close()
			go client.serveFile(left)
			if err := binary.Write(right, binary.LittleEndian, uint32(7)); err != nil {
				t.Fatal(err)
			}
			if phase == "stalled_offset" {
				if err := <-pending.done; !errors.Is(err, os.ErrDeadlineExceeded) {
					t.Fatalf("offset handshake did not time out: %v", err)
				}
				return
			}
			var offset uint64
			if err := binary.Read(right, binary.LittleEndian, &offset); err != nil {
				t.Fatal(err)
			}
			time.Sleep(2 * time.Minute) // Virtual time: setup deadlines must not cap a live transfer.
			select {
			case err := <-pending.done:
				t.Fatalf("setup timeout affected active file: %v", err)
			default:
			}
			if _, err := right.Write([]byte("data")); err != nil {
				t.Fatal(err)
			}
			if err := <-pending.done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
