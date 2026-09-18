package soulseek

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func testMessagePeer(t *testing.T) (*Client, *messagePeer, net.Conn) {
	t.Helper()
	c := NewClient(ClientConfig{Username: "local"})
	local, remote := net.Pipe()
	_ = remote.SetDeadline(time.Now().Add(3 * time.Second))
	p := c.installPeer("remote", local, true)
	t.Cleanup(func() { _ = remote.Close(); _ = c.Close() })
	return c, p, remote
}

func TestUnusedIncomingPeerPreservesActiveConnection(t *testing.T) {
	c, p, remote := testMessagePeer(t)
	lease := p.lease()
	defer lease.Close()
	p.mu.Lock()
	lease.request = QueueRequest{Filename: "music/song.flac"}
	p.mu.Unlock()
	unused, loser := net.Pipe()
	defer unused.Close()
	_ = loser.Close() // A losing direct/reverse route sends no application frame.
	c.serveMessagePeer(unused, PeerInitMessage{Username: "remote", Type: "P"})
	select {
	case <-p.done:
		t.Fatal("unused incoming connection closed the active peer")
	default:
	}
	sent := make(chan error, 1)
	go func() { sent <- writeMessage(remote, QueuePlace{Filename: "music/song.flac", Place: 2}) }()
	_ = lease.SetReadDeadline(time.Now().Add(time.Second))
	if command, _, err := ReadFrame(lease); err != nil || command != PeerPlaceInQueue {
		t.Fatalf("active request lost: command=%d error=%v", command, err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
}

func TestIncomingSearchPreservesActiveConnection(t *testing.T) {
	c, p, _ := testMessagePeer(t)
	lease := p.lease()
	defer lease.Close()
	searches := make(chan SearchResponse, 1)
	c.mu.Lock()
	c.pending[7] = searches
	c.mu.Unlock()
	incoming, remote := net.Pipe()
	defer incoming.Close()
	defer remote.Close()
	_ = remote.SetDeadline(time.Now().Add(time.Second))
	done := make(chan struct{})
	go func() {
		c.serveMessagePeer(incoming, PeerInitMessage{Username: "remote", Type: "P"})
		close(done)
	}()
	if err := writeMessage(remote, SearchResponse{Username: "remote", Token: 7}); err != nil {
		t.Fatal(err)
	}
	_ = remote.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("search connection did not finish")
	}
	select {
	case <-searches:
	default:
		t.Fatal("lost search response")
	}
	select {
	case <-p.done:
		t.Fatal("search response replaced the active peer")
	default:
	}
}

func TestPeerConnectionAfterReconnect(t *testing.T) {
	server, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	c := NewClient(ClientConfig{Username: "local", Address: server.Addr().String(), ListenAddr: "127.0.0.1:0"})
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for range 2 {
		if err := c.Connect(ctx); err != nil {
			t.Fatal(err)
		}
		conn, err := server.Accept()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		local, remote := net.Pipe()
		defer remote.Close()
		c.installPeer("remote", local, true)
		lease, err := c.acquirePeer(ctx, "remote")
		if err != nil {
			t.Fatalf("peer after connect: %v", err)
		}
		if err := c.uploadRoot.Err(); err != nil {
			t.Fatalf("connection lifetime is already canceled: %v", err)
		}
		_ = lease.Close()
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPeerConnectionReuse(t *testing.T) {
	c, p, remote := testMessagePeer(t)
	errs := make(chan error, 1)
	go func() {
		for range 2 {
			command, _, err := ReadFrame(remote)
			if err != nil {
				errs <- err
				return
			}
			if command != PeerGetSharedList {
				errs <- ErrMalformed
				return
			}
			if err := writeMessage(remote, SharedListResponse{Entries: []ShareEntry{{Name: "music\\song.flac", Size: 42}}}); err != nil {
				errs <- err
				return
			}
		}
		errs <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for range 2 {
		entries, err := c.BrowseUser(ctx, "remote", "")
		if err != nil || len(entries) == 0 {
			t.Fatalf("browse: %v, %v", entries, err)
		}
		c.mu.Lock()
		same := c.peers["remote"] == p
		c.mu.Unlock()
		if !same {
			t.Fatal("successful browse discarded reusable connection")
		}
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}

func TestPeerInterleavedSearchBrowseAndQueue(t *testing.T) {
	c, p, remote := testMessagePeer(t)
	searches := make(chan SearchResponse, 1)
	c.mu.Lock()
	c.pending[7] = searches
	c.mu.Unlock()
	browse, download := p.lease(), p.lease()
	defer browse.Close()
	defer download.Close()
	errs := make(chan error, 1)
	go func() {
		for range 2 {
			if _, _, err := ReadFrame(remote); err != nil {
				errs <- err
				return
			}
		}
		for _, m := range []Message{
			SearchResponse{Username: "remote", Token: 7},
			QueuePlace{Filename: "music/song.flac", Place: 3},
			SharedListResponse{Entries: []ShareEntry{{Name: "music\\song.flac", Size: 42}}},
		} {
			if err := writeMessage(remote, m); err != nil {
				errs <- err
				return
			}
		}
		errs <- nil
	}()
	if err := writeMessage(browse, SharedListRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := writeMessage(download, QueueRequest{Filename: "music/song.flac"}); err != nil {
		t.Fatal(err)
	}
	_ = browse.SetReadDeadline(time.Now().Add(time.Second))
	_ = download.SetReadDeadline(time.Now().Add(time.Second))
	if command, _, err := ReadFrame(browse); err != nil || command != PeerSharedList {
		t.Fatalf("browse got %d: %v", command, err)
	}
	if command, _, err := ReadFrame(download); err != nil || command != PeerPlaceInQueue {
		t.Fatalf("queue got %d: %v", command, err)
	}
	select {
	case <-searches:
	case <-time.After(time.Second):
		t.Fatal("lost search response")
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
		t.Fatal("search reply closed active operations")
	default:
	}
	_ = browse.Close()
	_ = download.Close()
	select {
	case <-p.done:
	case <-time.After(time.Second):
		t.Fatal("idle search socket retained")
	}
}

func TestPeerReplacementPreservesPendingOperation(t *testing.T) {
	c, p, remote := testMessagePeer(t)
	lease := p.lease()
	defer lease.Close()
	sent := make(chan error, 1)
	go func() { _, _, err := ReadFrame(remote); sent <- err }()
	if err := writeMessage(lease, QueueRequest{Filename: "music/song.flac"}); err != nil {
		t.Fatal(err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	incoming, other := net.Pipe()
	defer other.Close()
	_ = other.SetDeadline(time.Now().Add(time.Second))
	if replacement := c.installPeer("remote", incoming, true); replacement != p {
		t.Fatal("lost pending logical connection")
	}
	go func() {
		sent <- writeMessage(other, TransferRequest{Direction: 1, Token: 42, Filename: "music/song.flac", Size: 42})
	}()
	_ = lease.SetReadDeadline(time.Now().Add(time.Second))
	if cmd, _, err := ReadFrame(lease); cmd != PeerTransferRequest || err != nil {
		t.Fatalf("replacement response %d: %v", cmd, err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	p.finishConn(remote, io.EOF) // A stale transport must not evict the current one.
	select {
	case <-p.done:
		t.Fatal("stale reader closed replacement")
	default:
	}
}

func TestPeerIdleSearchAndCanceledBrowse(t *testing.T) {
	t.Run("search", func(t *testing.T) {
		_, p, remote := testMessagePeer(t)
		if err := writeMessage(remote, SearchResponse{Username: "remote", Token: 17}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-p.done:
		case <-time.After(time.Second):
			t.Fatal("idle search socket retained")
		}
	})
	t.Run("canceled browse", func(t *testing.T) {
		_, p, remote := testMessagePeer(t)
		lease := p.lease()
		sent := make(chan error, 1)
		go func() { _, _, err := ReadFrame(remote); sent <- err }()
		if err := writeMessage(lease, SharedListRequest{}); err != nil {
			t.Fatal(err)
		}
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
		_ = lease.Close()
		select {
		case <-p.done:
		case <-time.After(time.Second):
			t.Fatal("untagged canceled response could satisfy next browse")
		}
	})
}

func TestRacePeerConnections(t *testing.T) {
	t.Run("reverse wins while direct stalls and late socket closes", func(t *testing.T) {
		winner, other := net.Pipe()
		defer other.Close()
		late, lateOther := net.Pipe()
		defer lateOther.Close()
		started := make(chan struct{})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		got, err := racePeerConnections(ctx,
			func(ctx context.Context) (net.Conn, error) { close(started); <-ctx.Done(); return late, nil },
			func(context.Context) (net.Conn, error) { <-started; return winner, nil }, nil)
		if err != nil || got != winner {
			t.Fatalf("winner: %v %v", got, err)
		}
		defer got.Close()
		_ = lateOther.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := lateOther.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
			t.Fatalf("late connection leaked: %v", err)
		}
	})
	t.Run("failed branch does not cancel other", func(t *testing.T) {
		for _, directFails := range []bool{true, false} {
			winner, other := net.Pipe()
			failed := make(chan struct{})
			failure := func(context.Context) (net.Conn, error) { close(failed); return nil, io.EOF }
			success := func(ctx context.Context) (net.Conn, error) { <-failed; return winner, ctx.Err() }
			direct, reverse := failure, success
			if !directFails {
				direct, reverse = success, failure
			}
			got, err := racePeerConnections(context.Background(), direct, reverse, nil)
			if err != nil || got != winner {
				t.Fatalf("winner: %v %v", got, err)
			}
			_ = got.Close()
			_ = other.Close()
		}
	})
}

func TestIndirectCancellationRejectsLateSocket(t *testing.T) {
	c := NewClient(ClientConfig{Username: "local"})
	defer c.Close()
	server, other := net.Pipe()
	defer other.Close()
	c.conn = server
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := c.connectIndirect(ctx, "remote", "P"); result <- err }()
	command, payload, err := ReadFrame(other)
	if err != nil || command != ServerConnectToPeer {
		t.Fatalf("request %d: %v", command, err)
	}
	d := NewDecoder(payload)
	token, err := d.U32()
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	local, remote := net.Pipe()
	defer remote.Close()
	go c.servePeer(local)
	var e Encoder
	e.U32(token)
	_ = remote.SetDeadline(time.Now().Add(time.Second))
	if err := WriteInitFrame(remote, PeerPierceFirewall, e.Payload()); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("late socket leaked: %v", err)
	}
}

func TestPeerBidirectionalBrowse(t *testing.T) {
	a, b := NewClient(ClientConfig{}), NewClient(ClientConfig{})
	left, right := net.Pipe()
	a.installPeer("b", left, true)
	b.installPeer("a", right, true)
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	errs := make(chan error, 2)
	go func() { _, err := a.BrowseUser(ctx, "b", ""); errs <- err }()
	go func() { _, err := b.BrowseUser(ctx, "a", ""); errs <- err }()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestPeerLeaseCancellationAndDeadline(t *testing.T) {
	t.Run("write", func(t *testing.T) {
		_, p, _ := testMessagePeer(t)
		l := p.lease()
		result := make(chan error, 1)
		go func() { result <- writeMessage(l, QueueRequest{Filename: "music/song.flac"}) }()
		// Closing even before the writer is scheduled must unblock it promptly.
		_ = l.Close()
		select {
		case err := <-result:
			if err == nil {
				t.Fatal("write unexpectedly succeeded")
			}
		case <-time.After(time.Second):
			t.Fatal("canceled writer blocked")
		}
	})
	t.Run("read deadline", func(t *testing.T) {
		_, p, _ := testMessagePeer(t)
		l := p.lease()
		defer l.Close()
		result := make(chan error, 1)
		go func() { _, _, err := ReadFrame(l); result <- err }()
		_ = l.SetReadDeadline(time.Now())
		select {
		case err := <-result:
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("deadline did not wake reader")
		}
	})
}

func TestPeerBrowseProgressAndLimitSnapshot(t *testing.T) {
	c, _, remote := testMessagePeer(t)
	response, err := EncodeMessage(SharedListResponse{Entries: []ShareEntry{{Name: "music/song.flac", Size: 42}}})
	if err != nil {
		t.Fatal(err)
	}
	c.ConfigureBrowseLimits(BrowseLimits{MaxCompressedSize: len(response)})
	progress := make(chan struct{}, 1)
	errs := make(chan error, 1)
	go func() {
		if _, _, err := ReadFrame(remote); err != nil {
			errs <- err
			return
		}
		// Changes apply to the next operation, not this pending browse.
		c.ConfigureBrowseLimits(BrowseLimits{MaxCompressedSize: 8})
		cut := len(response) / 2
		if _, err := remote.Write(response[:cut]); err != nil {
			errs <- err
			return
		}
		select {
		case <-progress:
		case <-time.After(time.Second):
			errs <- errors.New("no incremental progress")
			return
		}
		_, err := remote.Write(response[cut:])
		errs <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = c.BrowseUserWithProgress(ctx, "remote", "", func(n, total uint64) {
		if n > 0 && n < total {
			select {
			case progress <- struct{}{}:
			default:
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	go func() {
		if _, _, err := ReadFrame(remote); err == nil {
			_, _ = remote.Write(response)
		}
	}()
	if _, err := c.BrowseUser(ctx, "remote", ""); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("next browse bypassed compressed limit: %v", err)
	}
}

func TestPeerReplacementDiscardsStaleBufferedResponse(t *testing.T) {
	c, p, remote := testMessagePeer(t)
	lease := p.lease()
	defer lease.Close()
	received, release := make(chan struct{}, 1), make(chan struct{})
	configurePeerRead(lease, func(n, total uint64) {
		if n == total {
			received <- struct{}{}
			<-release
		}
	}, MaxFrameSize)
	sent := make(chan error, 1)
	go func() {
		if _, _, err := ReadFrame(remote); err != nil {
			sent <- err
			return
		}
		sent <- writeMessage(remote, SharedListResponse{Entries: []ShareEntry{{Name: "old", Size: 1}}})
	}()
	if err := writeMessage(lease, SharedListRequest{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("old reader did not receive response")
	}
	incoming, other := net.Pipe()
	defer other.Close()
	_ = other.SetDeadline(time.Now().Add(time.Second))
	c.installPeer("remote", incoming, true)
	configurePeerRead(lease, nil, MaxFrameSize)
	close(release)
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	go func() { sent <- writeMessage(other, SharedListResponse{Entries: []ShareEntry{{Name: "new", Size: 2}}}) }()
	_ = lease.SetReadDeadline(time.Now().Add(time.Second))
	_, payload, err := ReadFrame(lease)
	if err != nil {
		t.Fatal(err)
	}
	response, err := DecodeSharedListResponse(payload)
	if err != nil || len(response.Entries) != 2 || response.Entries[1].Name != "new" || response.Entries[1].Size != 2 {
		t.Fatalf("stale response delivered: %+v %v", response, err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
}

func TestPeerQueuedWriterCancellation(t *testing.T) {
	_, p, remote := testMessagePeer(t)
	first, second := p.lease(), p.lease()
	defer first.Close()
	defer second.Close()
	blocked := make(chan error, 1)
	go func() { blocked <- writeMessage(first, QueueRequest{Filename: "first"}) }()
	// Consume only one byte; net.Pipe keeps the first writer blocked holding
	// the frame write slot until the rest is read or its lease is closed.
	if _, err := remote.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- writeMessage(second, QueueRequest{Filename: "second"}) }()
	_ = second.Close()
	select {
	case err := <-result:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled writer stuck behind another lease")
	}
	_ = first.Close()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("active write failed to stop")
	}
}

func TestPeerQueuedWriterDeadlineChanges(t *testing.T) {
	for _, extend := range []bool{false, true} {
		t.Run(map[bool]string{false: "shorten", true: "extend"}[extend], func(t *testing.T) {
			_, p, remote := testMessagePeer(t)
			l := p.lease()
			defer l.Close()
			p.writes <- struct{}{} // Hold the frame slot while this writer queues.
			held := true
			defer func() {
				if held {
					<-p.writes
				}
			}()
			original := time.Now().Add(100 * time.Millisecond)
			_ = l.SetWriteDeadline(original)
			result := make(chan error, 1)
			go func() { result <- writeMessage(l, QueueRequest{Filename: "queued"}) }()
			// The initial deadline notification is consumed only by the queued writer.
			until := time.Now().Add(time.Second)
			for len(l.writeDeadlineChanged) > 0 {
				if time.Now().After(until) {
					t.Fatal("writer did not queue")
				}
				time.Sleep(time.Millisecond)
			}
			if !extend {
				_ = l.SetWriteDeadline(time.Now())
				select {
				case err := <-result:
					var timeout net.Error
					if !errors.As(err, &timeout) || !timeout.Timeout() {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("shortened deadline ignored")
				}
				select {
				case <-p.done:
					t.Fatal("queued timeout closed shared socket")
				default:
				}
				return
			}
			_ = l.SetWriteDeadline(time.Now().Add(time.Second))
			<-time.After(time.Until(original.Add(10 * time.Millisecond)))
			select {
			case err := <-result:
				t.Fatalf("obsolete deadline fired: %v", err)
			default:
			}
			read := make(chan error, 1)
			go func() { _, _, err := ReadFrame(remote); read <- err }()
			<-p.writes
			held = false
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			if err := <-read; err != nil {
				t.Fatal(err)
			}
		})
	}
}
