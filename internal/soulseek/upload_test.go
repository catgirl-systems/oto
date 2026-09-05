package soulseek

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A loopback peer, not a public Soulseek account. Each connection has one owner.
func uploadPeer(t *testing.T, mode string, offset uint64) (PeerAddress, <-chan uint32, <-chan []byte) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	messages := make(chan uint32, 32)
	data := make(chan []byte, 8)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var conns []net.Conn
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			p, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, p)
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer p.Close()
				_ = p.SetDeadline(time.Now().Add(5 * time.Second))
				_, payload, err := ReadInitFrame(p)
				if err != nil {
					return
				}
				init, err := parsePeerInit(payload)
				if err != nil {
					return
				}
				if init.Type == "P" {
					cmd, payload, err := ReadFrame(p)
					if err != nil {
						return
					}
					messages <- cmd
					if cmd != PeerTransferRequest {
						return
					}
					req, err := DecodeTransferRequest(payload)
					if err != nil {
						return
					}
					if mode == "negotiating" {
						<-ctx.Done()
						return
					}
					if mode == "reject" {
						_ = writeMessage(p, TransferResponse{Token: req.Token, Reason: "Cancelled"})
						return
					}
					_ = writeMessage(p, TransferResponse{Token: req.Token, Accepted: true})
				} else if init.Type == "F" {
					var token [4]byte
					if _, err := io.ReadFull(p, token[:]); err != nil {
						return
					}
					messages <- 0 // F setup reached
					if mode == "offset" {
						<-ctx.Done()
						return
					}
					var b [8]byte
					binary.LittleEndian.PutUint64(b[:], offset)
					if _, err := p.Write(b[:]); err != nil {
						return
					}
					if mode == "blocked" {
						<-ctx.Done()
						return
					}
					content, _ := io.ReadAll(p)
					data <- content
				}
			}()
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
		mu.Lock()
		for _, p := range conns {
			_ = p.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return PeerAddress{Username: "peer", IP: "127.0.0.1", Port: uint32(ln.Addr().(*net.TCPAddr).Port)}, messages, data
}

func uploadClient(t *testing.T, address PeerAddress, contents []byte) (*Client, chan TransferEvent, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "song"), contents, 0600); err != nil {
		t.Fatal(err)
	}
	shares := NewShareIndex()
	if err := shares.AddRoot("Music", root); err != nil {
		t.Fatal(err)
	}
	if err := shares.ScanContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := make(chan TransferEvent, 2048)
	c := NewClient(ClientConfig{Share: shares, Uploads: NewUploadManager(1), UploadUpdate: func(e TransferEvent) { events <- e }})
	if address.IP != "" {
		lookup := &peerAddressLookup{done: make(chan struct{}), address: address}
		close(lookup.done)
		c.addresses["peer"] = lookup
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, events, filepath.Join(root, "song")
}

func uploadEvent(t *testing.T, events <-chan TransferEvent, state string) TransferEvent {
	t.Helper()
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	for {
		select {
		case e := <-events:
			if e.State == state {
				return e
			}
		case <-timer.C:
			t.Fatalf("no upload %s event", state)
			return TransferEvent{}
		}
	}
}
func uploadMessage(t *testing.T, messages <-chan uint32, want uint32) {
	t.Helper()
	select {
	case got := <-messages:
		if got != want {
			t.Fatalf("message %d, want %d", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("missing peer message")
	}
}

func TestUploadResumeAndRetry(t *testing.T) {
	contents := bytes.Repeat([]byte("shared file\n"), 100)
	addr, _, received := uploadPeer(t, "normal", 17)
	c, events, path := uploadClient(t, addr, contents)
	for i := 0; i < 2; i++ {
		started, err := c.QueueUpload("peer", `Music\song`)
		if err != nil || !started {
			t.Fatalf("queue %v %v", started, err)
		}
		queued := uploadEvent(t, events, "queued")
		completed := uploadEvent(t, events, "completed")
		if queued.Attempt != completed.Attempt || completed.Done != uint64(len(contents)) {
			t.Fatalf("events %+v %+v", queued, completed)
		}
		// Join cleanup before requeue, including completed attempts.
		c.StopUploads([]UploadTarget{{Username: "peer", Filename: `Music\song`, Attempt: queued.Attempt}}, false)
		select {
		case got := <-received:
			if !bytes.Equal(got, contents[17:]) {
				t.Fatalf("resume bytes %d", len(got))
			}
		case <-time.After(time.Second):
			t.Fatal("no file bytes")
		}
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, contents) {
		t.Fatal("upload modified shared file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := c.QueueUpload("peer", `Music\song`); err == nil {
		t.Fatal("missing shared file accepted")
	}
}

func TestUploadStopStages(t *testing.T) {
	for _, mode := range []string{"queued", "negotiating", "offset", "limited", "blocked"} {
		t.Run(mode, func(t *testing.T) {
			contents := bytes.Repeat([]byte("x"), 4<<20)
			addr, messages, _ := uploadPeer(t, mode, 0)
			c, events, _ := uploadClient(t, addr, contents)
			var blocker *UploadJob
			if mode == "queued" {
				blocker = c.cfg.Uploads.Enqueue("blocker", TransferRequest{})
			}
			if mode == "limited" {
				c.ConfigureUploads(UploadPolicy{BytesPerSecond: 1024})
			}
			_, err := c.QueueUpload("peer", `Music\song`)
			if err != nil {
				t.Fatal(err)
			}
			queued := uploadEvent(t, events, "queued")
			if started, err := c.QueueUpload("peer", "Music/song"); err != nil || started {
				t.Fatalf("duplicate %v %v", started, err)
			}
			if mode != "queued" {
				uploadMessage(t, messages, PeerTransferRequest)
			}
			if mode == "offset" || mode == "limited" || mode == "blocked" {
				uploadMessage(t, messages, 0)
			}
			if mode == "limited" || mode == "blocked" {
				uploadEvent(t, events, "running")
				time.Sleep(30 * time.Millisecond)
			}
			start := time.Now()
			c.StopUploads([]UploadTarget{{Username: "peer", Filename: `Music\song`, Attempt: queued.Attempt}}, mode == "queued")
			if time.Since(start) > time.Second {
				t.Fatal("stop did not unblock worker")
			}
			cancelled := uploadEvent(t, events, "cancelled")
			if mode == "limited" && cancelled.Done == 0 {
				t.Fatal("cancel lost bytes written inside a paced chunk")
			}
			if cancelled.Error != "" {
				t.Fatalf("intentional cancellation error %q", cancelled.Error)
			}
			if mode == "queued" {
				uploadMessage(t, messages, PeerUploadDenied)
				c.cfg.Uploads.Done(blocker)
			}
			c.mu.Lock()
			remaining := len(c.uploads)
			c.mu.Unlock()
			c.cfg.Uploads.mu.Lock()
			active, queuedJobs := c.cfg.Uploads.active, len(c.cfg.Uploads.q)
			c.cfg.Uploads.mu.Unlock()
			if remaining != 0 || active != 0 || queuedJobs != 0 {
				t.Fatalf("leaked upload/slot: %d/%d/%d", remaining, active, queuedJobs)
			}
		})
	}
}

func TestUploadBatchAndSchedulerCancellation(t *testing.T) {
	c, events, _ := uploadClient(t, PeerAddress{}, []byte("shared"))
	blocker := c.cfg.Uploads.Enqueue("blocker", TransferRequest{})
	var targets []UploadTarget
	for _, user := range []string{"a", "b", "c"} {
		if _, err := c.QueueUpload(user, `Music\song`); err != nil {
			t.Fatal(err)
		}
		e := uploadEvent(t, events, "queued")
		targets = append(targets, UploadTarget{Username: user, Filename: e.Filename, Attempt: e.Attempt})
	}
	c.StopUploads(targets, false)
	for range targets {
		uploadEvent(t, events, "cancelled")
	}
	c.cfg.Uploads.Done(blocker)
	c.cfg.Uploads.Done(blocker)
	if c.cfg.Uploads.active != 0 || len(c.cfg.Uploads.q) != 0 {
		t.Fatal("batch promoted/leaked cancelled jobs")
	}
	for _, policy := range []string{UploadScheduleFIFO, UploadScheduleRoundRobin, UploadScheduleRandom, UploadScheduleSmallestFirst} {
		m := NewUploadManager(1)
		m.Configure(UploadPolicy{Scheduling: policy})
		first := m.Enqueue("a", TransferRequest{})
		cancelled := m.Enqueue("b", TransferRequest{})
		next := m.Enqueue("b", TransferRequest{})
		m.CancelJobs([]*UploadJob{cancelled})
		m.Done(first)
		if uploadReady(cancelled) || !uploadReady(next) {
			t.Fatalf("%s promoted cancelled job", policy)
		}
		m.Done(cancelled)
		m.Done(next)
		m.Done(next)
		if m.active != 0 {
			t.Fatalf("%s leaked slot", policy)
		}
	}
}

func TestUploadIncomingQueuesAndSeparateDenials(t *testing.T) {
	c, events, _ := uploadClient(t, PeerAddress{}, []byte("shared"))
	blocker := c.cfg.Uploads.Enqueue("blocker", TransferRequest{})
	defer c.cfg.Uploads.Done(blocker)
	left, right := net.Pipe()
	defer right.Close()
	defer left.Close()
	_ = right.SetDeadline(time.Now().Add(3 * time.Second))
	go c.serveMessagePeer(left, PeerInitMessage{Username: "peer", Type: "P"})
	for _, req := range []Message{QueueRequest{Filename: `Music\song`}, QueueRequest{Filename: "Music/song"}, TransferRequest{Direction: 0, Token: 99, Filename: `Music\song`}} {
		if err := writeMessage(right, req); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ReadFrame(right); err != nil {
			t.Fatal(err)
		}
	}
	e := uploadEvent(t, events, "queued")
	c.mu.Lock()
	n := len(c.uploads)
	c.mu.Unlock()
	if n != 1 {
		t.Fatalf("duplicate attempts %d", n)
	}
	c.StopUploads([]UploadTarget{{Username: "peer", Filename: e.Filename, Attempt: e.Attempt}}, false)
	uploadEvent(t, events, "cancelled")
	// The same P connection can deliver failures for a pending download.
	pending := &pendingDownload{done: make(chan error, 1)}
	c.mu.Lock()
	c.requested[downloadKey("peer", `Other\song`)] = pending
	c.mu.Unlock()
	if err := writeMessage(right, QueueDenied{Filename: "Other/song", Reason: "Cancelled"}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-pending.done:
		if err.Error() != "Cancelled" {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("separate P denial lost")
	}
	if err := writeMessage(right, QueueFailedMessage{Filename: `Other\song`}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-pending.done:
		if err != ErrUploadFailed {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("separate P failure lost")
	}
	frame, _ := EncodeMessage(QueueDenied{Filename: `Other\song`, Reason: "Cancelled"})
	if _, err := DecodeQueueDenied(append(frame[8:], 0)); err == nil {
		t.Fatal("trailing denial data accepted")
	}
	frame, _ = EncodeMessage(QueueFailedMessage{Filename: `Other\song`})
	if _, err := DecodeQueueFailed(append(frame[8:], 0)); err == nil {
		t.Fatal("trailing failure data accepted")
	}
}

func TestUploadStopDuringBlockedAddressLookup(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprint(shutdown), func(t *testing.T) {
			c, events, _ := uploadClient(t, PeerAddress{}, []byte("shared"))
			left, right := net.Pipe()
			defer right.Close()
			c.mu.Lock()
			c.conn = left
			c.mu.Unlock()
			if _, err := c.QueueUpload("peer", `Music\song`); err != nil {
				t.Fatal(err)
			}
			queued := uploadEvent(t, events, "queued")
			uploadEvent(t, events, "running")
			done := make(chan struct{})
			go func() {
				if shutdown {
					_ = c.Close()
				} else {
					c.StopUploads([]UploadTarget{{Username: "peer", Filename: queued.Filename, Attempt: queued.Attempt}}, false)
				}
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("server write ignored cancellation")
			}
			state := "cancelled"
			if shutdown {
				state = "failed"
			}
			uploadEvent(t, events, state)
		})
	}
}

func TestUploadPeerRequeueJoinsRetiringAttempt(t *testing.T) {
	address, _, _ := uploadPeer(t, "reject", 0)
	c, events, _ := uploadClient(t, address, []byte("shared"))
	gate := make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(gate) })
	c.cfg.UploadUpdate = func(e TransferEvent) {
		events <- e
		if e.State == "failed" && e.Attempt == 1 {
			<-gate
		}
	}
	if _, err := c.QueueUpload("peer", `Music\song`); err != nil {
		t.Fatal(err)
	}
	uploadEvent(t, events, "failed")
	result := make(chan bool, 1)
	go func() {
		_, started, err := c.registerUpload("peer", `Music\song`, true)
		result <- started && err == nil
	}()
	select {
	case <-result:
		t.Fatal("peer requeue was lost while the old worker retired")
	case <-time.After(20 * time.Millisecond):
	}
	release.Do(func() { close(gate) })
	select {
	case started := <-result:
		if !started {
			t.Fatal("no replacement attempt")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("replacement attempt blocked")
	}
}
