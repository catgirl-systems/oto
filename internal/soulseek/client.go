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
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/catgirl-systems/oto/internal/country"
	"golang.org/x/sys/unix"
	"golang.org/x/text/cases"
)

type ClientConfig struct {
	Address, Username, Password, ListenAddr, NetworkInterface string
	Share                                                     *ShareIndex
	Uploads                                                   *UploadManager
	UploadUpdate                                              func(TransferEvent)
	IncomingSearch                                            *IncomingSearchPolicy
}

type IncomingSearchPolicy struct {
	Respond        bool
	MinimumLength  int
	MaximumResults int
}

func defaultIncomingSearchPolicy() IncomingSearchPolicy {
	return IncomingSearchPolicy{Respond: true, MaximumResults: 500}
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

const downloadSetupTimeout = 45 * time.Second

type pendingDownload struct {
	fileMu             sync.Mutex // Download waits for the file writer before returning.
	accepted           bool
	startTimer         *time.Timer // Protected by Client.mu; only bounds the wait for F.
	username, filename string
	size, offset       uint64
	writer             io.WriterAt
	progress           ProgressFunc
	done               chan error
	ctx                context.Context
}

func (p *pendingDownload) finish(err error) {
	// File completion, setup timeout and remote rejection can race; never block cleanup.
	select {
	case p.done <- err:
	default:
	}
}

type peerAddressLookup struct {
	done    chan struct{}
	address PeerAddress
	err     error
}

// Client owns one server connection; reconnecting creates a fresh lifecycle.
type Client struct {
	cfg                   ClientConfig
	mu                    sync.Mutex
	writeMu               chan struct{}
	uploads               map[string]*uploadAttempt
	uploadSeq             uint64
	uploadRoot            context.Context
	uploadCancel          context.CancelFunc
	uploadWG              sync.WaitGroup
	closing               bool
	browseSlot            chan struct{}
	searchSlots           chan struct{}
	conn                  net.Conn
	listener              net.Listener
	dialer                net.Dialer
	listenConfig          net.ListenConfig
	advertisedPort        uint16
	publicIP              string
	loggedIn              bool
	ctx                   context.Context
	cancel                context.CancelFunc
	done                  chan struct{}
	events                chan Event
	pending               map[uint32]chan SearchResponse
	passwordChange        chan string
	addresses             map[string]*peerAddressLookup
	pierce                map[uint32]chan net.Conn
	requested             map[string]*pendingDownload
	downloads             map[uint32]*pendingDownload
	distributed           *DistributedNode
	incomingSearch        IncomingSearchPolicy
	excludedSearchPhrases []string
	token                 uint32
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
	policy := defaultIncomingSearchPolicy()
	if cfg.IncomingSearch != nil {
		policy = *cfg.IncomingSearch
	}
	control := bindToDevice(cfg.NetworkInterface)
	uploadRoot, uploadCancel := context.WithCancel(context.Background())
	return &Client{cfg: cfg, writeMu: make(chan struct{}, 1), dialer: net.Dialer{Control: control}, listenConfig: net.ListenConfig{Control: control}, events: make(chan Event, 32), pending: make(map[uint32]chan SearchResponse), addresses: make(map[string]*peerAddressLookup), pierce: make(map[uint32]chan net.Conn), requested: make(map[string]*pendingDownload), downloads: make(map[uint32]*pendingDownload), uploads: make(map[string]*uploadAttempt), uploadRoot: uploadRoot, uploadCancel: uploadCancel, distributed: NewDistributedNode(), incomingSearch: policy, browseSlot: make(chan struct{}, 1), searchSlots: make(chan struct{}, 2)}
}

func bindToDevice(name string) func(string, string, syscall.RawConn) error {
	if name == "" {
		return nil
	}
	return func(_, _ string, raw syscall.RawConn) error {
		var bindErr error
		if err := raw.Control(func(fd uintptr) { bindErr = unix.BindToDevice(int(fd), name) }); err != nil {
			return fmt.Errorf("bind network interface %q: %w", name, err)
		}
		if bindErr != nil {
			return fmt.Errorf("bind network interface %q: %w", name, bindErr)
		}
		return nil
	}
}

// NewClientOnConn is useful for deterministic net.Pipe tests.
func NewClientOnConn(cfg ClientConfig, c net.Conn) *Client { x := NewClient(cfg); x.conn = c; return x }
func (c *Client) Events() <-chan Event                     { return c.events }
func (c *Client) PublicIP() string                         { c.mu.Lock(); defer c.mu.Unlock(); return c.publicIP }
func (c *Client) PublicPort() uint16 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.advertisedPort != 0 {
		return c.advertisedPort
	}
	if c.listener == nil {
		return 0
	}
	return uint16(c.listener.Addr().(*net.TCPAddr).Port)
}
func (c *Client) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return c.dialer.DialContext(ctx, network, address)
}
func (c *Client) shareIndex() *ShareIndex { c.mu.Lock(); defer c.mu.Unlock(); return c.cfg.Share }
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

func (c *Client) ConfigureUploads(policy UploadPolicy) {
	c.cfg.Uploads.Configure(policy)
}

func (c *Client) ConfigureIncomingSearch(policy IncomingSearchPolicy) {
	c.mu.Lock()
	c.incomingSearch = policy
	c.mu.Unlock()
}

func (c *Client) IncomingSearchPolicy() IncomingSearchPolicy {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.incomingSearch
}

func (c *Client) UploadPolicy() UploadPolicy {
	return c.cfg.Uploads.Policy()
}

func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	if c.conn != nil {
		c.mu.Unlock()
		return errors.New("soulseek: already connected")
	}
	c.mu.Unlock()
	conn, e := c.dialer.DialContext(ctx, "tcp", c.cfg.Address)
	if e != nil {
		return e
	}
	c.mu.Lock()
	c.conn = conn
	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.done = make(chan struct{})
	c.pending = make(map[uint32]chan SearchResponse)
	c.token = 0
	c.excludedSearchPhrases = nil
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
	ln, e := c.listenConfig.Listen(context.Background(), "tcp", c.cfg.ListenAddr)
	if e != nil {
		return e
	}
	c.mu.Lock()
	c.listener = ln
	c.mu.Unlock()
	go c.acceptLoop(ln)
	return nil
}

// ListenPort returns the actual TCP listener port.
func (c *Client) ListenPort() uint16 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.listener == nil {
		return 0
	}
	return uint16(c.listener.Addr().(*net.TCPAddr).Port)
}

// SetAdvertisedPort changes the externally reachable port without rebinding.
func (c *Client) SetAdvertisedPort(port uint16) error {
	if port == 0 {
		return errors.New("soulseek: invalid advertised port")
	}
	c.mu.Lock()
	c.advertisedPort = port
	loggedIn := c.loggedIn
	c.mu.Unlock()
	if loggedIn {
		return c.send(ListenPort{Port: uint32(port)})
	}
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
		changed, loggedIn := c.advertisedPort != port, c.loggedIn
		c.cfg.ListenAddr, c.advertisedPort = address, port
		c.mu.Unlock()
		if changed && loggedIn {
			return c.send(ListenPort{Port: uint32(port)})
		}
		return nil
	}
	if c.conn == nil {
		c.cfg.ListenAddr, c.advertisedPort = address, port
		c.mu.Unlock()
		return nil
	}
	listener, err := c.listenConfig.Listen(context.Background(), "tcp", address)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	oldListener, oldAddress, oldAdvertisedPort := c.listener, c.cfg.ListenAddr, c.advertisedPort
	c.listener, c.cfg.ListenAddr, c.advertisedPort = listener, address, port
	c.mu.Unlock()
	go c.acceptLoop(listener)
	if err := c.send(ListenPort{Port: uint32(port)}); err != nil {
		c.mu.Lock()
		if c.listener == listener {
			c.listener, c.cfg.ListenAddr, c.advertisedPort = oldListener, oldAddress, oldAdvertisedPort
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
		return ErrNotConnected
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
		ln, port := c.listener, c.advertisedPort
		c.loggedIn = true
		c.publicIP = net.IPv4(byte(r.IP>>24), byte(r.IP>>16), byte(r.IP>>8), byte(r.IP)).String()
		c.mu.Unlock()
		if port == 0 && ln != nil {
			port = uint16(ln.Addr().(*net.TCPAddr).Port)
		}
		if port != 0 {
			_ = c.send(ListenPort{Port: uint32(port)})
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

func (c *Client) ChangePassword(ctx context.Context, password string) error {
	if strings.TrimSpace(password) == "" {
		return errors.New("soulseek: password cannot be empty")
	}
	response := make(chan string, 1)
	c.mu.Lock()
	if c.conn == nil || !c.loggedIn {
		c.mu.Unlock()
		return errors.New("soulseek: not logged in")
	}
	if c.passwordChange != nil {
		c.mu.Unlock()
		return errors.New("soulseek: password change already in progress")
	}
	c.passwordChange = response
	done := c.done
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.passwordChange == response {
			c.passwordChange = nil
		}
		c.mu.Unlock()
	}()
	if err := c.send(ChangePassword{Password: password}); err != nil {
		return err
	}
	for {
		select {
		case got := <-response:
			if got != password {
				continue
			}
			c.mu.Lock()
			c.cfg.Password = password
			c.mu.Unlock()
			return nil
		case <-done:
			return errors.New("soulseek: connection closed before password changed")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
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
		return ErrNotConnected
	}
	defer close(c.done)
	defer func() {
		c.mu.Lock()
		c.excludedSearchPhrases = nil
		c.mu.Unlock()
	}()
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
	case ChangePassword:
		c.mu.Lock()
		ch := c.passwordChange
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- message.Password:
			default:
				select {
				case <-ch:
				default:
				}
				select {
				case ch <- message.Password:
				default:
				}
			}
		}
		return
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
		c.respondSearch(message)
	case ExcludedSearchPhrases:
		fold := cases.Fold()
		c.mu.Lock()
		c.excludedSearchPhrases = make([]string, len(message.Phrases))
		for i, phrase := range message.Phrases {
			c.excludedSearchPhrases[i] = fold.String(phrase)
		}
		c.mu.Unlock()
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
func (c *Client) send(m Message) error { return c.sendContext(c.baseContext(), m) }

func (c *Client) sendContext(ctx context.Context, m Message) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return ErrNotConnected
	}
	b, e := EncodeMessage(m)
	if e != nil {
		return e
	}
	select {
	case c.writeMu <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = conn.SetWriteDeadline(time.Now()); close(done) })
	defer func() {
		if !stop() {
			<-done
		}
		_ = conn.SetWriteDeadline(time.Time{})
		<-c.writeMu
	}()
	return writeAll(conn, b)
}

// Search collects token-matched responses for five seconds.
func (c *Client) Search(ctx context.Context, rawQuery string) ([]SearchResult, error) {
	return c.collectSearch(ctx, rawQuery, false)
}

// WishlistSearch uses the server's rate-limited automatic-search command.
func (c *Client) WishlistSearch(ctx context.Context, rawQuery string) ([]SearchResult, error) {
	return c.collectSearch(ctx, rawQuery, true)
}

func (c *Client) collectSearch(ctx context.Context, rawQuery string, wishlist bool) ([]SearchResult, error) {
	query, err := parseSearchQuery(rawQuery)
	if err != nil {
		return nil, err
	}
	token := c.nextToken()
	responses := make(chan SearchResponse, 64)
	c.mu.Lock()
	done := c.done
	c.pending[token] = responses
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, token); c.mu.Unlock() }()
	var request Message = SearchRequest{Token: token, Query: query.wire}
	if wishlist {
		request = WishlistSearchRequest{Token: token, Query: query.wire}
	}
	if err := c.send(request); err != nil {
		return nil, err
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	var results []SearchResult
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-done:
			return nil, errors.New("soulseek: connection closed during search")
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
	return c.browse(ctx, peer, path, nil)
}

// BrowseWithProgress reports compressed frame bytes for complete share lists.
func (c *Client) BrowseWithProgress(ctx context.Context, peer net.Conn, path string, progress func(received, total uint64)) ([]ShareEntry, error) {
	return c.browse(ctx, peer, path, progress)
}

func (c *Client) browse(ctx context.Context, peer net.Conn, path string, progress func(received, total uint64)) ([]ShareEntry, error) {
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
		command, payload, err := readFrameContextProgress(ctx, peer, progress)
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
	return readFrameContextProgress(ctx, c, nil)
}

func readFrameContextProgress(ctx context.Context, c net.Conn, progress func(received, total uint64)) (uint32, []byte, error) {
	type rr struct {
		cmd uint32
		p   []byte
		e   error
	}
	ch := make(chan rr, 1)
	go func() { a, b, e := ReadFrameWithProgress(c, progress); ch <- rr{a, b, e} }()
	select {
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	case x := <-ch:
		return x.cmd, x.p, x.e
	}
}

func (c *Client) connectAddress(ctx context.Context, addr, kind string) (net.Conn, error) {
	peer, err := c.dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = peer.Close() })
	defer stop()
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
		if err := c.sendContext(ctx, PeerAddressRequest{Username: username}); err != nil {
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
	if err := c.sendContext(ctx, ConnectPeer{Token: token, Username: username, Kind: kind}); err != nil {
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
	peer, err := c.dialer.DialContext(ctx, "tcp", net.JoinHostPort(instruction.IP, fmt.Sprint(instruction.Port)))
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
	return c.BrowseUserWithProgress(ctx, username, path, nil)
}

// BrowseUserWithProgress reports compressed frame bytes for complete share lists.
func (c *Client) BrowseUserWithProgress(ctx context.Context, username, path string, progress func(received, total uint64)) ([]ShareEntry, error) {
	peer, err := c.connectUser(ctx, username)
	if err != nil {
		return nil, err
	}
	defer peer.Close()
	return c.BrowseWithProgress(ctx, peer, path, progress)
}

func downloadKey(username, filename string) string {
	clean, err := NormalizePath(filename)
	if err != nil {
		clean = filename
	}
	return username + "\x00" + clean
}

// Download queues one remote file and receives its F connection into dst.
func (c *Client) Download(ctx context.Context, username, filename string, size, offset uint64, dst io.WriterAt, progress ProgressFunc) error {
	if dst == nil || offset > size {
		return ErrMalformed
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pending := &pendingDownload{username: username, filename: filename, size: size, offset: offset, writer: dst, progress: progress, done: make(chan error, 1), ctx: ctx}
	key := downloadKey(username, filename)
	c.mu.Lock()
	if _, exists := c.requested[key]; exists {
		c.mu.Unlock()
		return errors.New("soulseek: download already queued")
	}
	c.requested[key] = pending
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		delete(c.requested, key)
		if pending.startTimer != nil {
			pending.startTimer.Stop()
		}
		for token, download := range c.downloads {
			if download == pending {
				delete(c.downloads, token)
			}
		}
		c.mu.Unlock()
		pending.fileMu.Lock()
		pending.fileMu.Unlock()
	}()

	setupCtx, stopSetup := context.WithTimeout(ctx, downloadSetupTimeout)
	peer, err := c.connectUser(setupCtx, username)
	stopSetup()
	if err != nil {
		return err
	}
	defer peer.Close()
	stopPeer := context.AfterFunc(ctx, func() { _ = peer.Close() })
	defer stopPeer()
	if err := writeMessage(peer, QueueRequest{Filename: filename}); err != nil {
		return err
	}
	type frame struct {
		command uint32
		payload []byte
		err     error
	}
	frames := make(chan frame, 1)
	go func(frames chan<- frame) {
		for {
			command, payload, err := ReadFrame(peer)
			select {
			case frames <- frame{command, payload, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}(frames)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-pending.done:
			return err
		case message := <-frames:
			if message.err != nil {
				c.mu.Lock()
				accepted := pending.accepted
				c.mu.Unlock()
				var netErr net.Error
				transportError := errors.Is(message.err, io.EOF) || errors.Is(message.err, io.ErrUnexpectedEOF) ||
					errors.Is(message.err, net.ErrClosed) || errors.As(message.err, &netErr)
				if accepted && transportError {
					// P may close while F is still sending. The file result (or setup timeout) owns completion.
					_ = peer.Close()
					frames = nil
					continue
				}
				return fmt.Errorf("soulseek: download control connection: %w", message.err)
			}
			switch message.command {
			case PeerPlaceInQueue:
				d := NewDecoder(message.payload)
				_, err := d.String()
				place, placeErr := d.U32()
				if err == nil && placeErr == nil && pending.progress != nil {
					pending.progress(Progress{Done: offset, Total: size, State: "queued", Queue: place})
				}
				continue
			case PeerUploadDenied:
				denied, decodeErr := DecodeQueueDenied(message.payload)
				if decodeErr != nil || downloadKey(username, denied.Filename) != downloadKey(username, filename) {
					continue
				}
				reason := denied.Reason
				if reason == "" {
					reason = "upload denied"
				}
				return &DownloadRejectedError{Reason: reason}
			case PeerUploadFailed:
				failed, decodeErr := DecodeQueueFailed(message.payload)
				if decodeErr == nil && downloadKey(username, failed.Filename) == downloadKey(username, filename) {
					return ErrUploadFailed
				}
			case PeerTransferRequest:
				request, err := DecodeTransferRequest(message.payload)
				if err != nil {
					return err
				}
				if request.Direction != 1 || downloadKey(username, request.Filename) != downloadKey(username, filename) {
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
	c.mu.Lock()
	if err := pending.ctx.Err(); err != nil {
		c.mu.Unlock()
		return err
	}
	if pending.accepted {
		c.mu.Unlock()
		return nil
	}
	if pending.size != 0 && request.Size != pending.size {
		c.mu.Unlock()
		return errors.New("soulseek: remote file size changed")
	}
	if c.downloads[request.Token] != nil {
		c.mu.Unlock()
		return errors.New("soulseek: file connection token already in use")
	}
	pending.size, pending.accepted = request.Size, true
	c.downloads[request.Token] = pending
	pending.startTimer = time.AfterFunc(downloadSetupTimeout, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.downloads[request.Token] == pending {
			delete(c.downloads, request.Token)
			pending.finish(fmt.Errorf("soulseek: waiting for file connection: %w", context.DeadlineExceeded))
		}
	})
	c.mu.Unlock()
	if err := peer.SetWriteDeadline(time.Now().Add(downloadSetupTimeout)); err != nil {
		return err
	}
	defer peer.SetWriteDeadline(time.Time{})
	return writeMessage(peer, TransferResponse{Token: request.Token, Accepted: true})
}

func (c *Client) serveFile(peer net.Conn) {
	if err := peer.SetDeadline(time.Now().Add(downloadSetupTimeout)); err != nil {
		return
	}
	var tokenBytes [4]byte
	if _, err := io.ReadFull(peer, tokenBytes[:]); err != nil {
		return
	}
	token := binary.LittleEndian.Uint32(tokenBytes[:])
	c.mu.Lock()
	pending := c.downloads[token]
	delete(c.downloads, token)
	if pending != nil && pending.startTimer != nil {
		pending.startTimer.Stop()
	}
	c.mu.Unlock()
	if pending == nil {
		return
	}
	pending.fileMu.Lock()
	defer pending.fileMu.Unlock()
	if pending.ctx.Err() != nil {
		pending.finish(pending.ctx.Err())
		return
	}
	stop := context.AfterFunc(pending.ctx, func() { _ = peer.Close() })
	defer stop()
	var offsetBytes [8]byte
	binary.LittleEndian.PutUint64(offsetBytes[:], pending.offset)
	if _, err := peer.Write(offsetBytes[:]); err != nil {
		pending.finish(err)
		return
	}
	if err := peer.SetDeadline(time.Time{}); err != nil {
		pending.finish(err)
		return
	}
	err := CopyAtMost(pending.ctx, pending.writer, peer, pending.size, pending.offset, pending.progress)
	pending.finish(err)
}

func (c *Client) incomingSearchResults(query string) []SearchResult {
	c.mu.Lock()
	index, policy := c.cfg.Share, c.incomingSearch
	excluded := append([]string(nil), c.excludedSearchPhrases...)
	c.mu.Unlock()
	if index == nil || !policy.Respond || utf8.RuneCountInString(query) < policy.MinimumLength {
		return nil
	}
	return searchToResults(index.Search(query, policy.MaximumResults), excluded)
}

// respondSearch admits work before spawning: excess searches are best-effort and dropped.
func (c *Client) respondSearch(search IncomingSearch) {
	select {
	case c.searchSlots <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-c.searchSlots }()
		results := c.incomingSearchResults(search.Query)
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
		stop := context.AfterFunc(ctx, func() { _ = peer.Close() })
		defer stop()
		_ = writeMessage(peer, SearchResponse{Username: c.cfg.Username, Token: search.Token, Results: results, SlotFree: true})
	}()
}

func (c *Client) handleDistributedSearch(payload []byte) {
	search, err := DecodeDistributedSearch(payload)
	if err != nil {
		return
	}
	c.respondSearch(IncomingSearch{Username: search.Username, Token: search.Token, Query: search.Query})
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

func countryCodeForAddress(addr net.Addr) string {
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		return ""
	}
	return country.Lookup(tcpAddr.AddrPort().Addr())
}

func (c *Client) serveMessagePeer(peer net.Conn, peerInfo PeerInitMessage) {
	stop := context.AfterFunc(c.uploadRoot, func() { _ = peer.Close() })
	defer stop()
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
				countryCode := countryCodeForAddress(peer.RemoteAddr())
				for index := range response.Results {
					response.Results[index].CountryCode = countryCode
				}
				c.route(command, response)
			}
		case PeerUploadDenied:
			message, err := DecodeQueueDenied(payload)
			if err == nil {
				reason := message.Reason
				if reason == "" {
					reason = "upload denied"
				}
				c.failPendingDownload(peerInfo.Username, message.Filename, &DownloadRejectedError{Reason: reason})
			}
		case PeerUploadFailed:
			message, err := DecodeQueueFailed(payload)
			if err == nil {
				c.failPendingDownload(peerInfo.Username, message.Filename, ErrUploadFailed)
			}
		case PeerTransferRequest:
			request, err := DecodeTransferRequest(payload)
			if err != nil {
				continue
			}
			if request.Direction == 0 {
				_, _, err := c.registerUpload(peerInfo.Username, request.Filename)
				if err != nil {
					_ = writeMessage(peer, TransferResponse{Token: request.Token, Accepted: false, Reason: "File not shared"})
					continue
				}
				_ = writeMessage(peer, TransferResponse{Token: request.Token, Accepted: false, Reason: "Queued"})
				continue
			}
			if request.Direction != 1 {
				continue
			}
			clean, cleanErr := NormalizePath(request.Filename)
			if cleanErr != nil {
				_ = writeMessage(peer, TransferResponse{Token: request.Token, Accepted: false, Reason: "Cancelled"})
				continue
			}
			c.mu.Lock()
			pending := c.requested[downloadKey(peerInfo.Username, clean)]
			c.mu.Unlock()
			if pending == nil {
				_ = writeMessage(peer, TransferResponse{Token: request.Token, Accepted: false, Reason: "Cancelled"})
				continue
			}
			if err := c.acceptDownload(peer, pending, request); err != nil {
				pending.finish(err)
			}
		case PeerQueueUpload:
			filename, err := parseStringPayload(payload)
			if err != nil {
				continue
			}
			a, _, err := c.registerUpload(peerInfo.Username, filename)
			if err != nil {
				_ = writeMessage(peer, QueueDenied{Filename: filename, Reason: "File not shared"})
				continue
			}
			_ = writeMessage(peer, QueuePlace{Filename: a.target.Filename, Place: 1})
		}
	}
}

func (c *Client) shareEntries() []ShareEntry {
	files := c.shareIndex().Files()
	out := make([]ShareEntry, 0, len(files))
	for _, file := range files {
		out = append(out, ShareEntry{Name: file.Root + "\\" + strings.ReplaceAll(file.Path, "/", "\\"), Size: file.Size, Directory: file.Directory})
	}
	return out
}
func searchToResults(files []ShareFile, excludedPhrases []string) []SearchResult {
	out := make([]SearchResult, 0, len(files))
	fold := cases.Fold()
	for _, file := range files {
		path := file.Root + "\\" + strings.ReplaceAll(file.Path, "/", "\\")
		foldedPath, excluded := fold.String(path), false
		for _, phrase := range excludedPhrases {
			if strings.Contains(foldedPath, phrase) {
				excluded = true
				break
			}
		}
		if !excluded {
			out = append(out, SearchResult{Path: path, Size: file.Size, IsDirectory: file.Directory, Extension: strings.TrimPrefix(strings.ToLower(filepath.Ext(file.Path)), "."), Public: true})
		}
	}
	return out
}

// Close stops listener and connection, then waits for every upload attempt.
func (c *Client) Close() error {
	c.mu.Lock()
	ln, conn, cancel, uploadCancel := c.listener, c.conn, c.cancel, c.uploadCancel
	c.listener = nil
	c.conn = nil
	c.cancel = nil
	c.uploadCancel = nil
	c.closing = true
	c.loggedIn, c.advertisedPort, c.publicIP = false, 0, ""
	c.excludedSearchPhrases = nil
	c.mu.Unlock()
	if uploadCancel != nil {
		uploadCancel()
	}
	if cancel != nil {
		cancel()
	}
	if ln != nil {
		_ = ln.Close()
	}
	var closeErr error
	if conn != nil {
		closeErr = conn.Close()
	}
	c.uploadWG.Wait()
	return closeErr
}
