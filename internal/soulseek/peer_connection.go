package soulseek

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// Each P socket has one reader. Leases route replies to their requesting
// operation; closing a lease does not interrupt unrelated transfers.
type messagePeer struct {
	net.Conn
	client      *Client
	username    string
	mu          sync.Mutex
	writes      chan struct{}
	leases      map[*peerLease]struct{}
	done        chan struct{}
	err         error
	idle        *time.Timer
	searchReply bool
	incoming    chan peerTask
	writing     *peerLease
	writeConn   net.Conn
}

type peerFrame struct {
	command uint32
	payload []byte
}
type peerTask struct {
	peerFrame
	lease *peerLease
}
type peerLease struct {
	*messagePeer
	frames                      chan peerFrame
	closed                      chan struct{}
	request                     Message // guarded by messagePeer.mu, as are deadlines/progress
	progress                    func(uint64, uint64)
	frameLimit                  int
	readDeadline, writeDeadline time.Time
	deadlineChanged             chan struct{}
	writeDeadlineChanged        chan struct{}
}

func configurePeerRead(peer net.Conn, progress func(uint64, uint64), limit int) {
	if l := leasedPeer(peer); l != nil {
		l.mu.Lock()
		l.progress, l.frameLimit = progress, limit
		l.mu.Unlock()
	}
}

func leasedPeer(r any) *peerLease {
	for {
		switch p := r.(type) {
		case *peerLease:
			return p
		case *diagnosticConn:
			r = p.Conn
		default:
			return nil
		}
	}
}

func (c *Client) acquirePeer(ctx context.Context, username string) (net.Conn, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c.mu.Lock()
		if c.closing {
			c.mu.Unlock()
			return nil, net.ErrClosed
		}
		if p := c.peers[username]; p != nil {
			lease := p.lease()
			c.mu.Unlock()
			if lease != nil {
				return lease, nil
			}
			continue
		}
		if pending := c.peerConnecting[username]; pending != nil {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-pending:
			}
			continue
		}
		pending := make(chan struct{})
		c.peerConnecting[username] = pending
		c.mu.Unlock()
		conn, err := c.connectUserType(ctx, username, "P")
		var lease *peerLease
		if err == nil {
			p := c.installPeer(username, conn, false)
			lease = p.lease()
			if lease == nil {
				err = net.ErrClosed
			}
		}
		c.mu.Lock()
		delete(c.peerConnecting, username)
		close(pending)
		c.mu.Unlock()
		return lease, err
	}
}

// Incoming connections supersede the socket without losing pending operations.
// Old readers cannot evict the replacement; already-sent requests are not replayed.
func (c *Client) installPeer(username string, conn net.Conn, incoming bool) *messagePeer {
	c.mu.Lock()
	if old := c.peers[username]; old != nil && !c.closing {
		old.mu.Lock()
		usable := old.err == nil
		previous := old.Conn
		if usable && incoming {
			old.Conn = conn
			old.searchReply = false
			if old.idle != nil {
				old.idle.Reset(time.Minute)
			}
		}
		old.mu.Unlock()
		if usable {
			if incoming {
				c.peerWG.Add(1)
			}
			c.mu.Unlock()
			if incoming {
				_ = previous.Close()
				go old.run(conn)
			} else {
				_ = conn.Close()
			}
			return old
		}
	}
	p := &messagePeer{Conn: conn, client: c, username: username, writes: make(chan struct{}, 1), leases: make(map[*peerLease]struct{}), done: make(chan struct{}), incoming: make(chan peerTask, 4)}
	closing := c.closing
	if !closing {
		c.peers[username] = p
		c.peerWG.Add(2)
	}
	c.mu.Unlock()
	if closing {
		p.finish(net.ErrClosed)
		return p
	}
	p.mu.Lock()
	p.idle = time.AfterFunc(time.Minute, p.closeIdle)
	p.mu.Unlock()
	go p.handleIncoming()
	go p.run(conn)
	return p
}

func (p *messagePeer) handleIncoming() {
	defer p.client.peerWG.Done()
	for {
		select {
		case <-p.done:
			return
		case task := <-p.incoming:
			p.client.handleMessagePeer(task.lease, PeerInitMessage{Username: p.username, Type: "P"}, task.command, task.payload)
			_ = task.lease.Close()
		}
	}
}

func (p *messagePeer) lease() *peerLease { return p.leaseFor(nil) }

func (p *messagePeer) leaseFor(source net.Conn) *peerLease {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil || (source != nil && p.Conn != source) {
		return nil
	}
	l := &peerLease{messagePeer: p, frames: make(chan peerFrame, 4), closed: make(chan struct{}), deadlineChanged: make(chan struct{}, 1), writeDeadlineChanged: make(chan struct{}, 1)}
	p.leases[l] = struct{}{}
	return l
}

func (p *messagePeer) closeIdle() { p.closeIdleConn(nil) }

func (p *messagePeer) closeIdleConn(source net.Conn) {
	// Pending callers count as activity even before they acquire their lease.
	p.client.mu.Lock()
	p.mu.Lock()
	if len(p.leases) != 0 || p.err != nil || p.client.peerConnecting[p.username] != nil || (source != nil && p.Conn != source) {
		p.mu.Unlock()
		p.client.mu.Unlock()
		return
	}
	p.err = net.ErrClosed
	p.mu.Unlock()
	p.client.mu.Unlock()
	p.finish(net.ErrClosed)
}

func (p *messagePeer) finish(err error) { p.finishConn(nil, err) }

func (p *messagePeer) finishConn(source net.Conn, err error) {
	p.mu.Lock()
	if source != nil && p.Conn != source {
		p.mu.Unlock()
		return
	}
	select {
	case <-p.done:
		p.mu.Unlock()
		return
	default:
	}
	p.err = err
	conn := p.Conn
	close(p.done)
	if p.idle != nil {
		p.idle.Stop()
	}
	p.mu.Unlock()
	_ = conn.Close()
	p.client.mu.Lock()
	if p.client.peers[p.username] == p {
		delete(p.client.peers, p.username)
	}
	p.client.mu.Unlock()
}

func (l *peerLease) Close() error {
	p := l.messagePeer
	p.mu.Lock()
	select {
	case <-l.closed:
		p.mu.Unlock()
		return nil
	default:
		close(l.closed)
	}
	delete(p.leases, l)
	if p.writing == l {
		_ = p.writeConn.SetWriteDeadline(time.Now())
	}
	_, untaggedPending := l.request.(SharedListRequest)
	if p.idle != nil {
		p.idle.Reset(time.Minute)
	}
	closeSearch := p.searchReply && len(p.leases) == 0
	source := p.Conn
	p.mu.Unlock()
	// Shared-list replies have no token. A canceled request must not satisfy a
	// later browse on this socket.
	if untaggedPending {
		p.finish(net.ErrClosed)
	} else if closeSearch {
		p.closeIdleConn(source)
	}
	return nil
}

func (l *peerLease) LocalAddr() net.Addr { l.mu.Lock(); defer l.mu.Unlock(); return l.Conn.LocalAddr() }
func (l *peerLease) RemoteAddr() net.Addr {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Conn.RemoteAddr()
}

func (l *peerLease) Read([]byte) (int, error) {
	return 0, errors.New("soulseek: use framed reads on a P connection")
}
func (l *peerLease) Write(b []byte) (int, error) {
	p := l.messagePeer
	fallback := time.Now().Add(20 * time.Second)
	for {
		p.mu.Lock()
		requested := l.writeDeadline
		p.mu.Unlock()
		deadline := requested
		if deadline.IsZero() {
			deadline = fallback
		}
		timer := time.NewTimer(time.Until(deadline))
		acquired, changed := false, false
		var err error
		select {
		case p.writes <- struct{}{}:
			acquired = true
		case <-l.closed:
			err = net.ErrClosed
		case <-p.done:
			err = net.ErrClosed
		case <-l.writeDeadlineChanged:
			changed = true
		case <-timer.C:
			p.mu.Lock()
			changed = !l.writeDeadline.Equal(requested)
			p.mu.Unlock()
			if !changed {
				err = os.ErrDeadlineExceeded
			}
		}
		timer.Stop()
		if err != nil {
			return 0, err
		}
		if changed {
			continue
		}
		if acquired {
			break
		}
	}
	defer func() { <-p.writes }()
	p.mu.Lock()
	select {
	case <-l.closed:
		p.mu.Unlock()
		return 0, net.ErrClosed
	default:
	}
	conn, deadline := p.Conn, l.writeDeadline
	if deadline.IsZero() {
		deadline = fallback
	}
	if !deadline.After(time.Now()) {
		p.mu.Unlock()
		return 0, os.ErrDeadlineExceeded
	}
	p.writing, p.writeConn = l, conn
	_ = conn.SetWriteDeadline(deadline)
	p.mu.Unlock()
	err := writeAll(conn, b)
	p.mu.Lock()
	_ = conn.SetWriteDeadline(time.Time{})
	p.writing, p.writeConn = nil, nil
	p.mu.Unlock()
	if err != nil {
		p.finishConn(conn, err)
		return 0, err
	}
	return len(b), nil
}
func (l *peerLease) SetDeadline(t time.Time) error {
	_ = l.SetReadDeadline(t)
	return l.SetWriteDeadline(t)
}
func (l *peerLease) SetReadDeadline(t time.Time) error {
	l.mu.Lock()
	l.readDeadline = t
	l.mu.Unlock()
	select {
	case l.deadlineChanged <- struct{}{}:
	default:
	}
	return nil
}
func (l *peerLease) SetWriteDeadline(t time.Time) error {
	l.mu.Lock()
	l.writeDeadline = t
	var err error
	if l.writing == l {
		err = l.writeConn.SetWriteDeadline(t)
	}
	l.mu.Unlock()
	select {
	case l.writeDeadlineChanged <- struct{}{}:
	default:
	}
	return err
}

func (l *peerLease) writeMessage(m Message) error {
	b, err := EncodeMessage(m)
	if err != nil {
		return err
	}
	l.mu.Lock()
	switch m.(type) {
	case SharedListRequest, FolderRequest, QueueRequest, TransferRequest:
		l.request = m
	}
	l.mu.Unlock()
	_, err = l.Write(b)
	return err
}

func (l *peerLease) readFrame(progress func(uint64, uint64), limit int) (uint32, []byte, error) {
	l.mu.Lock()
	l.progress = progress
	l.mu.Unlock()
	for {
		l.mu.Lock()
		deadline := l.readDeadline
		l.mu.Unlock()
		var timeout <-chan time.Time
		var timer *time.Timer
		if !deadline.IsZero() {
			timer = time.NewTimer(time.Until(deadline))
			timeout = timer.C
		}
		var f peerFrame
		var err error
		changed := false
		select {
		case f = <-l.frames:
		default:
			select {
			case f = <-l.frames:
			case <-l.closed:
				err = net.ErrClosed
			case <-timeout:
				err = os.ErrDeadlineExceeded
			case <-l.deadlineChanged:
				changed = true
			case <-l.done:
				// EOF may follow a complete response immediately.
				select {
				case f = <-l.frames:
				default:
					l.mu.Lock()
					err = l.err
					l.mu.Unlock()
				}
			}
		}
		if timer != nil {
			timer.Stop()
		}
		if changed {
			continue
		}
		if err != nil {
			return 0, nil, err
		}
		if len(f.payload)+4 > limit {
			return 0, nil, ErrTooLarge
		}
		if progress != nil {
			progress(uint64(len(f.payload)+4), uint64(len(f.payload)+4))
		}
		return f.command, f.payload, nil
	}
}

func (l *peerLease) matches(f peerFrame) bool {
	switch req := l.request.(type) {
	case SharedListRequest:
		return f.command == PeerSharedList
	case FolderRequest:
		if f.command != PeerFolderResponse {
			return false
		}
		zr, err := zlib.NewReader(bytes.NewReader(f.payload))
		if err != nil {
			return true
		}
		defer zr.Close()
		var token uint32
		return binary.Read(zr, binary.LittleEndian, &token) != nil || token == req.Token
	case TransferRequest:
		if f.command != PeerTransferResponse {
			return false
		}
		response, err := DecodeTransferResponse(f.payload)
		return err != nil || response.Token == req.Token
	case QueueRequest:
		var filename string
		switch f.command {
		case PeerPlaceInQueue, PeerUploadDenied, PeerUploadFailed:
			filename, _ = NewDecoder(f.payload).String()
		case PeerTransferRequest:
			r, err := DecodeTransferRequest(f.payload)
			if err != nil || r.Direction != 1 {
				return false
			}
			filename = r.Filename
		default:
			return false
		}
		return downloadKey(l.username, filename) == downloadKey(l.username, req.Filename)
	}
	return false
}

type bufferedPeer struct {
	net.Conn
	reader *bufio.Reader
}

func (p *bufferedPeer) Read(b []byte) (int, error) { return p.reader.Read(b) }

func (p *messagePeer) run(conn net.Conn) {
	defer p.client.peerWG.Done()
	stop := context.AfterFunc(p.client.uploadRoot, func() { p.finish(net.ErrClosed) })
	defer stop()
	buffer := bufio.NewReader(conn)
	var reader io.Reader = &bufferedPeer{Conn: conn, reader: buffer}
	if d, ok := conn.(*diagnosticConn); ok {
		reader = &diagnosticConn{Conn: reader.(net.Conn), logger: d.logger, id: d.id, opened: d.opened}
	}
	for {
		header, err := buffer.Peek(8)
		if err != nil {
			p.finishConn(conn, err)
			return
		}
		command := binary.LittleEndian.Uint32(header[4:])
		limit := MaxFrameSize
		matchedLimit := false
		p.mu.Lock()
		for l := range p.leases {
			switch l.request.(type) {
			case SharedListRequest, FolderRequest:
				if (command == PeerSharedList || command == PeerFolderResponse) && l.frameLimit > 0 {
					if !matchedLimit || l.frameLimit > limit {
						limit = l.frameLimit
					}
					matchedLimit = true
				}
			}
		}
		p.mu.Unlock()
		command, payload, err := readFrame(reader, func(n, total uint64) {
			p.mu.Lock()
			var callbacks []func(uint64, uint64)
			for l := range p.leases {
				_, browsing := l.request.(SharedListRequest)
				if browsing && l.progress != nil && command == PeerSharedList {
					callbacks = append(callbacks, l.progress)
				}
			}
			p.mu.Unlock()
			for _, cb := range callbacks {
				cb(n, total)
			}
		}, limit)
		if err != nil {
			p.finishConn(conn, err)
			return
		}
		f := peerFrame{command, payload}
		p.mu.Lock()
		if p.Conn != conn || p.err != nil {
			p.mu.Unlock()
			return
		}
		if p.idle != nil {
			p.idle.Reset(time.Minute)
		}
		delivered, overflow := false, false
		for l := range p.leases {
			if !l.matches(f) {
				continue
			}
			if _, queue := l.request.(QueueRequest); !queue {
				l.request = nil
			}
			select {
			case l.frames <- f:
				delivered = true
			default:
				overflow = true
			}
			break
		}
		p.mu.Unlock()
		if overflow {
			p.finishConn(conn, errors.New("soulseek: peer response queue full"))
			return
		}
		if delivered {
			continue
		}
		lease := p.leaseFor(conn)
		if lease == nil {
			return
		}
		if command == PeerSearch {
			p.client.handleMessagePeer(lease, PeerInitMessage{Username: p.username, Type: "P"}, command, payload)
			p.mu.Lock()
			if p.Conn == conn {
				p.searchReply = true
			}
			p.mu.Unlock()
			_ = lease.Close()
			continue
		}
		// Inbound replies may require writes. Keep the reader running so two
		// clients browsing each other cannot deadlock their TCP send buffers.
		select {
		case p.incoming <- peerTask{f, lease}:
		default:
			_ = lease.Close()
			p.finishConn(conn, errors.New("soulseek: incoming peer queue full"))
			return
		}
	}
}
