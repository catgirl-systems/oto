package soulseek

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type ClientConfig struct {
	Address, Username, Password, ListenAddr string
	Share                                   *ShareIndex
	Uploads                                 *UploadManager
}

type UserStatus uint32

const (
	UserStatusAway   UserStatus = 1
	UserStatusOnline UserStatus = 2
)

type Event struct {
	Command uint32
	Message any
	Err     error
}

type TransferEvent struct {
	Direction, Username, Filename, State string
	Done, Total                          uint64
	Error                                string
}

type pendingDownload struct {
	username, filename string
	size, offset       uint64
	writer             io.WriterAt
	progress           ProgressFunc
	done               chan error
	ctx                context.Context
}

type peerAddressLookup struct {
	done    chan struct{}
	address PeerAddress
	err     error
}

// Client owns one server connection; reconnecting creates a fresh lifecycle.
type Client struct {
	cfg         ClientConfig
	mu          sync.Mutex
	writeMu     sync.Mutex
	browseSlot  chan struct{}
	conn        net.Conn
	listener    net.Listener
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	events      chan Event
	pending     map[uint32]chan SearchResponse
	addresses   map[string]*peerAddressLookup
	pierce      map[uint32]chan net.Conn
	requested   map[string]*pendingDownload
	downloads   map[uint32]*pendingDownload
	distributed *DistributedNode
	token       uint32
}

func NewClient(cfg ClientConfig) *Client {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:0"
	}
	if cfg.Share == nil {
		cfg.Share = NewShareIndex()
	}
	if cfg.Uploads == nil {
		cfg.Uploads = NewUploadManager(1)
	}
	return &Client{cfg: cfg, events: make(chan Event, 32), pending: make(map[uint32]chan SearchResponse), addresses: make(map[string]*peerAddressLookup), pierce: make(map[uint32]chan net.Conn), requested: make(map[string]*pendingDownload), downloads: make(map[uint32]*pendingDownload), distributed: NewDistributedNode(), browseSlot: make(chan struct{}, 1)}
}

// NewClientOnConn is useful for deterministic net.Pipe tests.
func NewClientOnConn(cfg ClientConfig, c net.Conn) *Client { x := NewClient(cfg); x.conn = c; return x }
func (c *Client) Events() <-chan Event                     { return c.events }
func (c *Client) shareIndex() *ShareIndex                  { c.mu.Lock(); defer c.mu.Unlock(); return c.cfg.Share }
func sharedCounts(index *ShareIndex) (counts SharedCounts) {
	for _, file := range index.Files() {
		if file.Directory {
			counts.Folders++
		} else {
			counts.Files++
		}
	}
	return counts
}
func (c *Client) baseContext() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}
func (c *Client) SetShareIndex(index *ShareIndex) {
	if index == nil {
		index = NewShareIndex()
	}
	c.mu.Lock()
	c.cfg.Share = index
	c.mu.Unlock()
	_ = c.send(sharedCounts(index))
}

func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	if c.conn != nil {
		c.mu.Unlock()
		return errors.New("soulseek: already connected")
	}
	c.mu.Unlock()
	d := net.Dialer{}
	conn, e := d.DialContext(ctx, "tcp", c.cfg.Address)
	if e != nil {
		return e
	}
	c.mu.Lock()
	c.conn = conn
	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.done = make(chan struct{})
	c.pending = make(map[uint32]chan SearchResponse)
	c.token = 0
	c.mu.Unlock()
	if e := c.startListener(); e != nil {
		_ = conn.Close()
		c.mu.Lock()
		c.conn = nil
		c.cancel = nil
		c.mu.Unlock()
		return e
	}
	return nil
}
func (c *Client) startListener() error {
	if c.cfg.ListenAddr == "" {
		return nil
	}
	ln, e := net.Listen("tcp", c.cfg.ListenAddr)
	if e != nil {
		return e
	}
	c.mu.Lock()
	c.listener = ln
	c.mu.Unlock()
	go c.acceptLoop(ln)
	return nil
}

// SetListenPort replaces the incoming listener and advertises it without
// interrupting the Soulseek session.
func (c *Client) SetListenPort(port uint16) error {
	c.mu.Lock()
	host, _, err := net.SplitHostPort(c.cfg.ListenAddr)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	address := net.JoinHostPort(host, fmt.Sprint(port))
	if c.listener != nil && c.listener.Addr().(*net.TCPAddr).Port == int(port) {
		c.cfg.ListenAddr = address
		c.mu.Unlock()
		return nil
	}
	if c.conn == nil {
		c.cfg.ListenAddr = address
		c.mu.Unlock()
		return nil
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	oldListener, oldAddress := c.listener, c.cfg.ListenAddr
	c.listener, c.cfg.ListenAddr = listener, address
	c.mu.Unlock()
	go c.acceptLoop(listener)
	if err := c.send(ListenPort{Port: uint32(port)}); err != nil {
		c.mu.Lock()
		if c.listener == listener {
			c.listener, c.cfg.ListenAddr = oldListener, oldAddress
		}
		c.mu.Unlock()
		_ = listener.Close()
		return err
	}
	if oldListener != nil {
		_ = oldListener.Close()
	}
	return nil
}
func (c *Client) Login(ctx context.Context) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errors.New("soulseek: not connected")
	}
	hash := fmt.Sprintf("%x", md5.Sum([]byte(c.cfg.Username+c.cfg.Password)))
	m := LoginRequest{Username: c.cfg.Username, Password: c.cfg.Password, Version: ProtocolVersion, MinorVersion: ProtocolMinor, Hash: hash}
	b, e := EncodeMessage(m)
	if e != nil {
		return e
	}
	if e = writeAll(conn, b); e != nil {
		return e
	}
	type result struct {
		cmd uint32
		p   []byte
		e   error
	}
	ch := make(chan result, 1)
	go func() { cmd, p, e := ReadFrame(conn); ch <- result{cmd, p, e} }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case x := <-ch:
		if x.e != nil {
			return x.e
		}
		if x.cmd != ServerLogin {
			return fmt.Errorf("%w: expected login response", ErrMalformed)
		}
		r, e := DecodeLoginResponse(x.p)
		if e != nil {
			return e
		}
		if !r.Success {
			return errors.New(r.Message)
		}
		c.mu.Lock()
		ln := c.listener
		c.mu.Unlock()
		if ln != nil {
			_ = c.send(ListenPort{Port: uint32(ln.Addr().(*net.TCPAddr).Port)})
		}
		_ = c.SetStatus(UserStatusOnline)
		index := c.shareIndex()
		_ = c.send(sharedCounts(index))
		_ = c.send(AcceptChildren{Value: true})
		_ = c.send(HaveNoParent{Value: true})
		return nil
	}
}

func (c *Client) SetStatus(status UserStatus) error {
	if status != UserStatusAway && status != UserStatusOnline {
		return errors.New("soulseek: invalid user status")
	}
	return c.send(Status{Status: uint32(status)})
}
func writeAll(w net.Conn, b []byte) error {
	for len(b) > 0 {
		n, e := w.Write(b)
		if e != nil {
			return e
		}
		if n == 0 {
			return ioErrNoProgress
		}
		b = b[n:]
	}
	return nil
}

var ioErrNoProgress = errors.New("soulseek: no progress writing")

// Run routes server frames until cancellation or connection close.
func (c *Client) Run(ctx context.Context) error {
	c.mu.Lock()
	conn := c.conn
	if c.done == nil {
		c.done = make(chan struct{})
	}
	c.mu.Unlock()
	if conn == nil {
		return errors.New("soulseek: not connected")
	}
	defer close(c.done)
	for {
		cmd, p, e := ReadFrame(conn)
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.emit(Event{Err: e})
			return e
		}
		m, e := DecodeMessage(cmd, p)
		if e != nil {
			c.emit(Event{Command: cmd, Err: e})
			continue
		}
		c.route(cmd, m)
	}
}
func (c *Client) route(cmd uint32, m any) {
	switch message := m.(type) {
	case SearchResponse:
		c.mu.Lock()
		ch := c.pending[message.Token]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- message:
			default:
			}
		}
	case PeerAddress:
		c.mu.Lock()
		lookup := c.addresses[message.Username]
		if lookup != nil {
			delete(c.addresses, message.Username)
			lookup.address = message
			close(lookup.done)
		}
		c.mu.Unlock()
	case ConnectPeerInstruction:
		go c.answerConnectPeer(message)
	case IncomingSearch:
		go c.respondSearch(message)
	case EmbeddedDistributed:
		if message.Command == DistributedSearchCommand {
			c.handleDistributedSearch(message.Payload)
		}
	case PossibleParents:
		go c.connectDistributedParent(message)
	case RawMessage:
		if message.Command == ServerResetDistributed {
			c.distributed.SetParent("")
			_ = c.send(HaveNoParent{Value: true})
		}
	}
	c.emit(Event{Command: cmd, Message: m})
}
func (c *Client) emit(e Event) {
	select {
	case c.events <- e:
	default:
	}
}
func (c *Client) nextToken() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token++
	if c.token == 0 {
		c.token = 1
	}
	return c.token
}
func (c *Client) send(m Message) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errors.New("soulseek: not connected")
	}
	b, e := EncodeMessage(m)
	if e != nil {
		return e
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeAll(conn, b)
}

// Search collects token-matched responses for five seconds.
func (c *Client) Search(ctx context.Context, rawQuery string) ([]SearchResult, error) {
	query, err := parseSearchQuery(rawQuery)
	if err != nil {
		return nil, err
	}
	token := c.nextToken()
	responses := make(chan SearchResponse, 64)
	c.mu.Lock()
	c.pending[token] = responses
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, token); c.mu.Unlock() }()
	if err := c.send(SearchRequest{Token: token, Query: query.wire}); err != nil {
		return nil, err
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	var results []SearchResult
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case response := <-responses:
			for _, result := range response.Results {
				if query.matches(result) {
					results = append(results, result)
				}
			}
		case <-timer.C:
			return results, nil
		}
	}
}
func (c *Client) Browse(ctx context.Context, peer net.Conn, path string) ([]ShareEntry, error) {
	if path == "" {
		select {
		case c.browseSlot <- struct{}{}:
			defer func() { <-c.browseSlot }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if err := writeMessage(peer, SharedListRequest{}); err != nil {
			return nil, err
		}
		command, payload, err := readFrameContext(ctx, peer)
		if err != nil {
			return nil, err
		}
		if command != PeerSharedList {
			return nil, fmt.Errorf("%w: expected shared list", ErrMalformed)
		}
		response, err := DecodeSharedListResponse(payload)
		return response.Entries, err
	}
	cleanPath, err := NormalizePath(path)
	if err != nil {
		return nil, err
	}
	path = strings.ReplaceAll(cleanPath, "/", "\\")
	token := c.nextToken()
	if err := writeMessage(peer, FolderRequest{Token: token, Path: path}); err != nil {
		return nil, err
	}
	command, payload, err := readFrameContext(ctx, peer)
	if err != nil {
		return nil, err
	}
	if command != PeerFolderResponse {
		return nil, fmt.Errorf("%w: expected folder response", ErrMalformed)
	}
	response, err := DecodeFolderResponse(payload)
	if err != nil {
		return nil, err
	}
	responsePath, pathErr := NormalizePath(response.Path)
	if response.Token != token || pathErr != nil || !strings.EqualFold(cleanPath, responsePath) {
		return nil, fmt.Errorf("%w: folder response", ErrMalformed)
	}
	return response.Entries, nil
}
func writeMessage(w net.Conn, m Message) error {
	b, e := EncodeMessage(m)
	if e != nil {
		return e
	}
	return writeAll(w, b)
}
func readFrameContext(ctx context.Context, c net.Conn) (uint32, []byte, error) {
	type rr struct {
		cmd uint32
		p   []byte
		e   error
	}
	ch := make(chan rr, 1)
	go func() { a, b, e := ReadFrame(c); ch <- rr{a, b, e} }()
	select {
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	case x := <-ch:
		return x.cmd, x.p, x.e
	}
}

func (c *Client) connectAddress(ctx context.Context, addr, kind string) (net.Conn, error) {
	peer, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if err = encodePeerHandshake(peer, PeerInitMessage{Username: c.cfg.Username, Type: kind, Token: 0}); err != nil {
		peer.Close()
		return nil, err
	}
	return peer, nil
}

func (c *Client) connectUser(ctx context.Context, username string) (net.Conn, error) {
	return c.connectUserType(ctx, username, "P")
}

func (c *Client) connectUserType(ctx context.Context, username, kind string) (net.Conn, error) {
	if username == "" {
		return nil, errors.New("soulseek: empty username")
	}
	address, err := c.lookupPeerAddress(ctx, username)
	if err != nil {
		return nil, err
	}
	var directErr error
	if address.IP != "0.0.0.0" && address.Port != 0 {
		if peer, err := c.connectAddress(ctx, net.JoinHostPort(address.IP, fmt.Sprint(address.Port)), kind); err == nil {
			return peer, nil
		} else {
			directErr = err
		}
	}
	peer, err := c.connectIndirect(ctx, username, kind)
	if err != nil && directErr != nil {
		return nil, fmt.Errorf("direct: %v; indirect: %w", directErr, err)
	}
	return peer, err
}

func (c *Client) lookupPeerAddress(ctx context.Context, username string) (PeerAddress, error) {
	c.mu.Lock()
	lookup := c.addresses[username]
	owner := lookup == nil
	if owner {
		lookup = &peerAddressLookup{done: make(chan struct{})}
		c.addresses[username] = lookup
	}
	c.mu.Unlock()

	if owner {
		if err := c.send(PeerAddressRequest{Username: username}); err != nil {
			c.mu.Lock()
			if c.addresses[username] == lookup {
				delete(c.addresses, username)
				lookup.err = err
				close(lookup.done)
			}
			c.mu.Unlock()
		}
	}
	select {
	case <-ctx.Done():
		return PeerAddress{}, ctx.Err()
	case <-lookup.done:
		return lookup.address, lookup.err
	}
}

func (c *Client) connectIndirect(ctx context.Context, username, kind string) (net.Conn, error) {
	token := randomToken()
	connection := make(chan net.Conn, 1)
	c.mu.Lock()
	c.pierce[token] = connection
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pierce, token); c.mu.Unlock() }()
	if err := c.send(ConnectPeer{Token: token, Username: username, Kind: kind}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case peer := <-connection:
		return peer, nil
	}
}

func (c *Client) answerConnectPeer(instruction ConnectPeerInstruction) {
	ctx, cancel := context.WithTimeout(c.baseContext(), 10*time.Second)
	defer cancel()
	peer, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(instruction.IP, fmt.Sprint(instruction.Port)))
	if err != nil {
		return
	}
	var payload Encoder
	payload.U32(instruction.Token)
	if err := WriteInitFrame(peer, PeerPierceFirewall, payload.Payload()); err != nil {
		_ = peer.Close()
		return
	}
	defer peer.Close()
	switch instruction.Kind {
	case "P":
		c.serveMessagePeer(peer, PeerInitMessage{Username: instruction.Username, Type: "P"})
	case "F":
		c.serveFile(peer)
	case "D":
		c.serveDistributed(peer, instruction.Username)
	}
}

func (c *Client) BrowseUser(ctx context.Context, username, path string) ([]ShareEntry, error) {
	peer, err := c.connectUser(ctx, username)
	if err != nil {
		return nil, err
	}
	defer peer.Close()
	return c.Browse(ctx, peer, path)
}

func downloadKey(username, filename string) string { return username + "\x00" + filename }

// Download queues one remote file and receives its F connection into dst.
func (c *Client) Download(ctx context.Context, username, filename string, size, offset uint64, dst io.WriterAt, progress ProgressFunc) error {
	if dst == nil || offset > size {
		return ErrMalformed
	}
	pending := &pendingDownload{username: username, filename: filename, size: size, offset: offset, writer: dst, progress: progress, done: make(chan error, 1), ctx: ctx}
	key := downloadKey(username, filename)
	c.mu.Lock()
	if _, exists := c.requested[key]; exists {
		c.mu.Unlock()
		return errors.New("soulseek: download already queued")
	}
	c.requested[key] = pending
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.requested, key); c.mu.Unlock() }()

	peer, err := c.connectUser(ctx, username)
	if err != nil {
		return err
	}
	defer peer.Close()
	if err := writeMessage(peer, QueueRequest{Filename: filename}); err != nil {
		return err
	}
	type frame struct {
		command uint32
		payload []byte
		err     error
	}
	frames := make(chan frame, 1)
	go func() {
		for {
			command, payload, err := ReadFrame(peer)
			frames <- frame{command, payload, err}
			if err != nil {
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-pending.done:
			return err
		case message := <-frames:
			if message.err != nil {
				return message.err
			}
			switch message.command {
			case PeerPlaceInQueue:
				d := NewDecoder(message.payload)
				_, err := d.String()
				place, placeErr := d.U32()
				if err == nil && placeErr == nil && pending.progress != nil {
					pending.progress(Progress{Done: pending.offset, Total: pending.size, State: "queued", Queue: place})
				}
				continue
			case PeerUploadDenied:
				d := NewDecoder(message.payload)
				_, _ = d.String()
				reason, _ := d.String()
				if reason == "" {
					reason = "upload denied"
				}
				return errors.New(reason)
			case PeerUploadFailed:
				return errors.New("remote upload failed")
			case PeerTransferRequest:
				request, err := DecodeTransferRequest(message.payload)
				if err != nil {
					return err
				}
				if request.Direction != 1 || request.Filename != filename {
					continue
				}
				if err := c.acceptDownload(peer, pending, request); err != nil {
					return err
				}
			}
		}
	}
}

func (c *Client) acceptDownload(peer net.Conn, pending *pendingDownload, request TransferRequest) error {
	if pending.size != 0 && request.Size != pending.size {
		return errors.New("soulseek: remote file size changed")
	}
	pending.size = request.Size
	c.mu.Lock()
	c.downloads[request.Token] = pending
	c.mu.Unlock()
	return writeMessage(peer, TransferResponse{Token: request.Token, Accepted: true})
}

func (c *Client) serveFile(peer net.Conn) {
	var tokenBytes [4]byte
	if _, err := io.ReadFull(peer, tokenBytes[:]); err != nil {
		return
	}
	token := binary.LittleEndian.Uint32(tokenBytes[:])
	c.mu.Lock()
	pending := c.downloads[token]
	delete(c.downloads, token)
	c.mu.Unlock()
	if pending == nil {
		return
	}
	var offsetBytes [8]byte
	binary.LittleEndian.PutUint64(offsetBytes[:], pending.offset)
	if _, err := peer.Write(offsetBytes[:]); err != nil {
		pending.done <- err
		return
	}
	err := CopyAtMost(pending.ctx, pending.writer, peer, pending.size, pending.offset, pending.progress)
	pending.done <- err
}

func (c *Client) respondSearch(search IncomingSearch) {
	index := c.shareIndex()
	if index == nil {
		return
	}
	results := searchToResults(index.Search(search.Query))
	if len(results) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(c.baseContext(), 10*time.Second)
	defer cancel()
	peer, err := c.connectUser(ctx, search.Username)
	if err != nil {
		return
	}
	defer peer.Close()
	_ = writeMessage(peer, SearchResponse{Username: c.cfg.Username, Token: search.Token, Results: results, SlotFree: true})
}

func (c *Client) handleDistributedSearch(payload []byte) {
	search, err := DecodeDistributedSearch(payload)
	if err != nil {
		return
	}
	go c.respondSearch(IncomingSearch{Username: search.Username, Token: search.Token, Query: search.Query})
	c.distributed.Dispatch(DistributedMessage{Command: DistributedSearchCommand, Payload: append([]byte(nil), payload...)})
}

func (c *Client) connectDistributedParent(message PossibleParents) {
	if c.distributed.Parent() != "" {
		return
	}
	for _, candidate := range message.Parents {
		ctx, cancel := context.WithTimeout(c.baseContext(), 10*time.Second)
		peer, err := c.connectAddress(ctx, net.JoinHostPort(candidate.IP, fmt.Sprint(candidate.Port)), "D")
		cancel()
		if err == nil {
			go c.runDistributedParent(candidate.Username, peer)
			return
		}
	}
}

func (c *Client) runDistributedParent(username string, peer net.Conn) {
	defer peer.Close()
	level := int32(0)
	root := username
	for {
		message, err := ReadDistributed(peer)
		if err != nil {
			break
		}
		switch message.Command {
		case DistributedBranchLevelCommand:
			if value, err := DecodeDistributedBranchLevel(message.Payload); err == nil {
				level = int32(value)
			}
		case DistributedBranchRootCommand:
			if value, err := DecodeDistributedBranchRoot(message.Payload); err == nil {
				root = string(value)
			}
		case DistributedSearchCommand:
			if _, err := DecodeDistributedSearch(message.Payload); err != nil {
				continue
			}
			if c.distributed.Parent() == "" {
				c.distributed.SetParent(username)
				_ = c.send(HaveNoParent{Value: false})
				_ = c.send(BranchLevel{Level: uint32(level + 1)})
				_ = c.send(BranchRoot{Username: root})
			}
			c.handleDistributedSearch(message.Payload)
		}
	}
	if c.distributed.Parent() == username {
		c.distributed.SetParent("")
		_ = c.send(HaveNoParent{Value: true})
	}
}

func (c *Client) serveDistributed(peer net.Conn, username string) {
	messages, err := c.distributed.AddChild(username)
	if err != nil {
		return
	}
	defer c.distributed.RemoveChild(username)
	level := DistributedBranchLevel(0)
	root := c.cfg.Username
	if parent := c.distributed.Parent(); parent != "" {
		level, root = 1, parent
	}
	_ = WriteDistributed(peer, DistributedMessage{Command: DistributedBranchLevelCommand, Payload: level.MarshalBinary()})
	rootPayload, _ := DistributedBranchRoot(root).MarshalBinary()
	_ = WriteDistributed(peer, DistributedMessage{Command: DistributedBranchRootCommand, Payload: rootPayload})
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		for message := range messages {
			if WriteDistributed(peer, message) != nil {
				return
			}
		}
	}()
	for {
		message, err := ReadDistributed(peer)
		if err != nil {
			return
		}
		if message.Command == DistributedSearchCommand {
			c.handleDistributedSearch(message.Payload)
		}
	}
}
func randomToken() uint32 {
	var b [4]byte
	if _, e := rand.Read(b[:]); e != nil {
		return uint32(time.Now().UnixNano())
	}
	return binary.LittleEndian.Uint32(b[:])
}
func (c *Client) acceptLoop(ln net.Listener) {
	for {
		p, e := ln.Accept()
		if e != nil {
			return
		}
		go c.servePeer(p)
	}
}
func (c *Client) servePeer(p net.Conn) {
	initCmd, b, e := ReadInitFrame(p)
	if e != nil {
		return
	}
	if initCmd == PeerPierceFirewall {
		d := NewDecoder(b)
		token, err := d.U32()
		if err != nil || d.Done() != nil {
			_ = p.Close()
			return
		}
		c.mu.Lock()
		connection := c.pierce[token]
		c.mu.Unlock()
		if connection == nil {
			_ = p.Close()
			return
		}
		select {
		case connection <- p:
			return
		default:
			_ = p.Close()
			return
		}
	}
	defer p.Close()
	if initCmd == byte(PeerInit) {
		peerInfo, err := parsePeerInit(b)
		if err != nil {
			return
		}
		if peerInfo.Type == "F" {
			c.serveFile(p)
			return
		}
		if peerInfo.Type == "D" {
			c.serveDistributed(p, peerInfo.Username)
			return
		}
		if peerInfo.Type != "P" {
			return
		}
		c.serveMessagePeer(p, peerInfo)
	}
}

func (c *Client) serveMessagePeer(peer net.Conn, peerInfo PeerInitMessage) {
	for {
		command, payload, err := ReadFrame(peer)
		if err != nil {
			return
		}
		switch command {
		case PeerGetSharedList:
			if len(payload) == 0 {
				_ = writeMessage(peer, SharedListResponse{Entries: c.shareEntries()})
			}
		case PeerFolderContents:
			d := NewDecoder(payload)
			token, err := d.U32()
			if err != nil {
				continue
			}
			path, err := d.String()
			if err != nil || d.Done() != nil {
				continue
			}
			entries, _ := c.shareIndex().Subtree(path)
			_ = writeMessage(peer, FolderResponse{Token: token, Path: path, Entries: entries})
		case PeerSearch:
			response, err := DecodeSearchResponse(payload)
			if err == nil {
				c.route(command, response)
			}
		case PeerTransferRequest:
			request, err := DecodeTransferRequest(payload)
			if err != nil {
				continue
			}
			if request.Direction == 0 {
				localPath, job := c.enqueueUpload(peerInfo.Username, request.Filename)
				if job == nil {
					_ = writeMessage(peer, TransferResponse{Token: request.Token, Accepted: false, Reason: "File not shared"})
					continue
				}
				_ = writeMessage(peer, TransferResponse{Token: request.Token, Accepted: false, Reason: "Queued"})
				if err := c.upload(peer, peerInfo.Username, localPath, job); err != nil {
					_ = writeMessage(peer, QueueFailedMessage{Filename: request.Filename})
				}
				continue
			}
			if request.Direction != 1 {
				continue
			}
			c.mu.Lock()
			pending := c.requested[downloadKey(peerInfo.Username, request.Filename)]
			c.mu.Unlock()
			if pending == nil {
				_ = writeMessage(peer, TransferResponse{Token: request.Token, Accepted: false, Reason: "Cancelled"})
				continue
			}
			_ = c.acceptDownload(peer, pending, request)
		case PeerQueueUpload:
			filename, err := parseStringPayload(payload)
			if err != nil {
				continue
			}
			localPath, job := c.enqueueUpload(peerInfo.Username, filename)
			if job == nil {
				_ = writeMessage(peer, QueueDenied{Filename: filename, Reason: "File not shared"})
				continue
			}
			_ = writeMessage(peer, QueuePlace{Filename: filename, Place: 1})
			if err := c.upload(peer, peerInfo.Username, localPath, job); err != nil {
				_ = writeMessage(peer, QueueFailedMessage{Filename: filename})
			}
		}
	}
}

func (c *Client) enqueueUpload(username, filename string) (string, *UploadJob) {
	localPath, err := c.shareIndex().Resolve(filename)
	if err != nil {
		return "", nil
	}
	stat, err := os.Stat(localPath)
	if err != nil || !stat.Mode().IsRegular() {
		return "", nil
	}
	job := c.cfg.Uploads.Enqueue(username, TransferRequest{Direction: 1, Token: randomToken(), Filename: filename, Size: uint64(stat.Size())})
	return localPath, job
}

func (c *Client) upload(messagePeer net.Conn, username, localPath string, job *UploadJob) (result error) {
	ctx, cancel := context.WithTimeout(c.baseContext(), 30*time.Minute)
	defer cancel()
	if err := c.cfg.Uploads.Wait(ctx, job); err != nil {
		return err
	}
	defer c.cfg.Uploads.Done(username)
	request := job.Request
	c.emit(Event{Command: PeerTransferRequest, Message: TransferEvent{Direction: "upload", Username: username, Filename: request.Filename, State: "running", Total: request.Size}})
	defer func() {
		state, message, done := "completed", "", request.Size
		if result != nil {
			state, message, done = "failed", result.Error(), 0
		}
		c.emit(Event{Command: PeerTransferRequest, Message: TransferEvent{Direction: "upload", Username: username, Filename: request.Filename, State: state, Done: done, Total: request.Size, Error: message}})
	}()
	if err := writeMessage(messagePeer, request); err != nil {
		return err
	}
	command, payload, err := readFrameContext(ctx, messagePeer)
	if err != nil {
		return err
	}
	if command != PeerTransferResponse {
		return fmt.Errorf("%w: expected transfer response", ErrMalformed)
	}
	response, err := DecodeTransferResponse(payload)
	if err != nil {
		return err
	}
	if response.Token != request.Token || !response.Accepted {
		if response.Reason == "" {
			response.Reason = "upload denied"
		}
		return errors.New(response.Reason)
	}
	filePeer, err := c.connectUserType(ctx, username, "F")
	if err != nil {
		return err
	}
	defer filePeer.Close()
	var token [4]byte
	binary.LittleEndian.PutUint32(token[:], request.Token)
	if _, err := filePeer.Write(token[:]); err != nil {
		return err
	}
	var offsetBytes [8]byte
	if _, err := io.ReadFull(filePeer, offsetBytes[:]); err != nil {
		return err
	}
	offset := binary.LittleEndian.Uint64(offsetBytes[:])
	return SendFile(ctx, filepath.Dir(localPath), filepath.Base(localPath), filePeer, request.Size, offset, func(progress Progress) {
		c.emit(Event{Command: PeerTransferRequest, Message: TransferEvent{Direction: "upload", Username: username, Filename: request.Filename, State: "running", Done: progress.Done, Total: progress.Total}})
	})
}
func (c *Client) shareEntries() []ShareEntry {
	files := c.shareIndex().Files()
	out := make([]ShareEntry, 0, len(files))
	for _, file := range files {
		out = append(out, ShareEntry{Name: file.Root + "\\" + strings.ReplaceAll(file.Path, "/", "\\"), Size: file.Size, Directory: file.Directory})
	}
	return out
}
func searchToResults(files []ShareFile) []SearchResult {
	out := make([]SearchResult, 0, len(files))
	for _, file := range files {
		out = append(out, SearchResult{Path: file.Root + "\\" + strings.ReplaceAll(file.Path, "/", "\\"), Size: file.Size, IsDirectory: file.Directory, Extension: strings.TrimPrefix(strings.ToLower(filepath.Ext(file.Path)), "."), Public: true})
	}
	return out
}

// Close stops listener and connection; it is safe to call repeatedly.
func (c *Client) Close() error {
	c.mu.Lock()
	ln, conn, cancel := c.listener, c.conn, c.cancel
	c.listener = nil
	c.conn = nil
	c.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if ln != nil {
		_ = ln.Close()
	}
	if conn != nil {
		return conn.Close()
	}
	return nil
}
