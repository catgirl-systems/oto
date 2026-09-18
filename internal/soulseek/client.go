package soulseek

import (
	"bufio"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/catgirl-systems/oto/internal/country"
	"github.com/catgirl-systems/oto/internal/diagnostics"
	"golang.org/x/sys/unix"
	"golang.org/x/text/cases"
)

type ClientConfig struct {
	DownloadLimitBytesPerSecond                               int64
	Address, Username, Password, ListenAddr, NetworkInterface string
	Share                                                     *ShareIndex
	Uploads                                                   *UploadManager
	UploadUpdate                                              func(TransferEvent)
	UploadStreamStart                                         func(TransferEvent)
	// SharePolicy is consulted for each peer-facing share operation.
	SharePolicy func(username string, address netip.Addr) SharePermission
	// UploadAccepted runs after admission is reserved and before execution.
	// Returning an error rolls the reservation back.
	UploadAccepted func(TransferEvent) error
	UploadRejected func(TransferEvent)
	// DownloadOffered runs without client locks. Nil rejects unsolicited files.
	// It must bound and persist admission before asynchronously receiving the offer.
	DownloadOffered func(*DownloadOffer) error
	// SocialUpdate is authoritative; Events is only a lossy diagnostic stream.
	// A successful PrivateMessage callback must have committed content or a
	// deliberate-discard receipt: Run sends its acknowledgement only afterward.
	SocialUpdate func(context.Context, SocialMessage) error
	// UploadsReady gates new admissions and streaming until recovery is complete.
	UploadsReady   <-chan struct{}
	IncomingSearch *IncomingSearchPolicy
	BrowseLimits   BrowseLimits
	Logger         *slog.Logger
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
	UserStatusOffline UserStatus = 0
	UserStatusAway    UserStatus = 1
	UserStatusOnline  UserStatus = 2
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
	start              func()
	authorize          func(netip.Addr) error
	done               chan error
	ctx                context.Context
	observation        *transferObservation
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
	started time.Time
}

// Client owns one server connection; reconnecting creates a fresh lifecycle.
type Client struct {
	uploadAdmissionMu     sync.Mutex
	downloadLimit         downloadLimiter
	cfg                   ClientConfig
	mu                    sync.Mutex
	writeMu               chan struct{}
	uploads               map[string]*uploadAttempt
	uploadSeq             uint64
	uploadRoot            context.Context
	uploadCancel          context.CancelFunc
	shareResponseRoot     context.Context
	shareResponseCancel   context.CancelFunc
	uploadWG              sync.WaitGroup
	closing               bool
	lifecycleMu           sync.Mutex // Serialize reconnect with complete upload shutdown.
	running               bool
	connectCancel         context.CancelFunc
	closePending          int
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
	peerIPs               map[string]cachedShareAddress
	pierce                map[uint32]chan net.Conn
	peers                 map[string]*messagePeer
	peerConnecting        map[string]chan struct{}
	peerWG                sync.WaitGroup
	requested             map[string]*pendingDownload
	downloads             map[uint32]*pendingDownload
	observations          map[string]*transferObservation
	distributed           *DistributedNode
	incomingSearch        IncomingSearchPolicy
	excludedSearchPhrases []string
	token                 uint32
	selfDescription       string
}

func NewClient(cfg ClientConfig) *Client {
	if cfg.Logger != nil {
		cfg.Logger = cfg.Logger.With("component", "soulseek")
	}
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
	client := &Client{cfg: cfg, peers: make(map[string]*messagePeer), peerConnecting: make(map[string]chan struct{}), writeMu: make(chan struct{}, 1), dialer: net.Dialer{Control: control}, listenConfig: net.ListenConfig{Control: control}, events: make(chan Event, 32), pending: make(map[uint32]chan SearchResponse), addresses: make(map[string]*peerAddressLookup), peerIPs: make(map[string]cachedShareAddress), pierce: make(map[uint32]chan net.Conn), requested: make(map[string]*pendingDownload), downloads: make(map[uint32]*pendingDownload), uploads: make(map[string]*uploadAttempt), uploadRoot: uploadRoot, uploadCancel: uploadCancel, distributed: NewDistributedNode(), incomingSearch: policy, browseSlot: make(chan struct{}, 1), searchSlots: make(chan struct{}, 2)}
	client.ConfigureDownloadLimit(cfg.DownloadLimitBytesPerSecond)
	client.shareResponseRoot, client.shareResponseCancel = context.WithCancel(uploadRoot)
	client.beginUploadRecovery()
	return client
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
	c.RevalidateSharePolicy()
	_ = c.send(c.shareCounts(index))
}

func (c *Client) ConfigureDownloadLimit(bytesPerSecond int64) {
	c.downloadLimit.configure(bytesPerSecond)
}

func (c *Client) DownloadLimit() int64 {
	return c.downloadLimit.limit()
}

func (c *Client) ConfigureUploads(policy UploadPolicy) {
	c.cfg.Uploads.Configure(policy)
}

func (c *Client) SetUploadBandwidth(bytesPerSecond int64) {
	c.cfg.Uploads.SetBandwidth(bytesPerSecond)
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
	if c.closePending != 0 {
		c.mu.Unlock()
		return errors.New("soulseek: client is closing")
	}
	if c.conn != nil || c.running || c.connectCancel != nil {
		c.mu.Unlock()
		return errors.New("soulseek: already connected")
	}
	ctx, cancel := context.WithCancel(ctx)
	c.connectCancel = cancel
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		c.connectCancel = nil
		c.mu.Unlock()
	}()
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	started := time.Now()
	c.log(ctx, slog.LevelDebug, "server_dial_started", nil)
	conn, e := c.dialer.DialContext(ctx, "tcp", c.cfg.Address)
	if e != nil {
		c.log(ctx, slog.LevelWarn, "server_dial_failed", e, slog.String("stage", "dial"), slog.Int64("elapsed_ms", time.Since(started).Milliseconds()))
		return e
	}
	conn = c.traceConn(ctx, conn, "", "S", "server")
	diagnostics.Event(c.peerLogger(ctx, conn), slog.LevelInfo, "server_connected", nil, slog.Int64("elapsed_ms", time.Since(started).Milliseconds()))
	c.mu.Lock()
	c.conn = conn
	if c.closing {
		c.uploadRoot, c.uploadCancel = context.WithCancel(context.Background())
		c.shareResponseRoot, c.shareResponseCancel = context.WithCancel(c.uploadRoot)
		c.beginUploadRecovery()
		c.closing = false
		c.addresses = make(map[string]*peerAddressLookup)
		c.peerIPs = make(map[string]cachedShareAddress)
	}
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
	// Port replacement participates in the daemon's shutdown cutoff. Bound its
	// advertisement so a stalled server cannot hold that cutoff indefinitely.
	ctx, cancel := context.WithTimeout(c.baseContext(), 2*time.Second)
	defer cancel()
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
			return c.sendContext(ctx, ListenPort{Port: uint32(port)})
		}
		return nil
	}
	if c.conn == nil {
		c.cfg.ListenAddr, c.advertisedPort = address, port
		c.mu.Unlock()
		return nil
	}
	listener, err := c.listenConfig.Listen(ctx, "tcp", address)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	oldListener, oldAddress, oldAdvertisedPort := c.listener, c.cfg.ListenAddr, c.advertisedPort
	c.listener, c.cfg.ListenAddr, c.advertisedPort = listener, address, port
	c.mu.Unlock()
	go c.acceptLoop(listener)
	if err := c.sendContext(ctx, ListenPort{Port: uint32(port)}); err != nil {
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
func (c *Client) Login(ctx context.Context) (loginErr error) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	ctx = diagnostics.WithLogger(ctx, c.peerLogger(ctx, conn))
	defer func() {
		level := slog.LevelInfo
		if loginErr != nil {
			level = slog.LevelWarn
		}
		c.log(ctx, level, "server_login_result", loginErr, slog.Bool("success", loginErr == nil), slog.String("stage", "login"))
	}()
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
			err := c.send(ListenPort{Port: uint32(port)})
			level := slog.LevelInfo
			if err != nil {
				level = slog.LevelWarn
			}
			c.log(ctx, level, "listen_port_sent", err, slog.Uint64("port", uint64(port)), slog.Bool("success", err == nil))
		}
		_ = c.SetStatus(UserStatusOnline)
		index := c.shareIndex()
		_ = c.send(c.shareCounts(index))
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
	conn, root := c.conn, c.uploadRoot
	if conn == nil || c.running {
		c.mu.Unlock()
		return ErrNotConnected
	}
	c.running = true
	if c.done == nil {
		c.done = make(chan struct{})
	}
	done := c.done
	c.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	stopClose := context.AfterFunc(root, cancel)
	stopRead := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	defer func() { stopClose(); stopRead(); cancel() }()
	defer func() {
		c.mu.Lock()
		c.running = false
		c.excludedSearchPhrases = nil
		close(done)
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
		if err := ctx.Err(); err != nil {
			return err
		}
		m, e := DecodeServerMessage(cmd, p)
		if e != nil {
			c.emit(Event{Command: cmd, Err: e})
			if _, social := m.(SocialMessage); social {
				return e // An authoritative update cannot be silently skipped.
			}
			continue
		}
		_, connectsPeer := m.(ConnectPeerInstruction)
		if !connectsPeer {
			c.route(cmd, m)
		}
		if message, ok := m.(SocialMessage); ok && c.cfg.SocialUpdate != nil {
			if err := c.cfg.SocialUpdate(ctx, message); err != nil {
				return fmt.Errorf("soulseek: social update: %w", err)
			}
			if private, ok := message.(PrivateMessage); ok {
				// Callback success promises durable content or a discard receipt.
				ackCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := c.sendContext(ackCtx, PrivateMessageAck{ID: private.ID})
				cancel()
				if err != nil {
					return fmt.Errorf("soulseek: private message acknowledgement: %w", err)
				}
			}
		}
		// Publish the server's privilege flag before this peer can queue an upload.
		if connectsPeer {
			c.route(cmd, m)
		}
	}
}
func (c *Client) route(cmd uint32, m any) {
	switch message := m.(type) {
	case PrivateMessage, RoomMessage:
		// Chat content belongs only to the authoritative callback, not diagnostics.
		c.emit(Event{Command: cmd})
		return
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
		if ip, err := netip.ParseAddr(message.IP); err == nil {
			if len(c.peerIPs) >= 4096 {
				for username := range c.peerIPs {
					delete(c.peerIPs, username)
					break
				}
			}
			c.peerIPs[message.Username] = cachedShareAddress{ip.Unmap(), time.Now().Add(time.Minute)}
		}
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
	_, err := c.sendTracked(ctx, m, nil)
	return err
}

func (c *Client) sendTracked(ctx context.Context, m Message, beforeWrite func() error) (bool, error) {
	b, err := EncodeMessage(m)
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return false, ErrNotConnected
	}
	select {
	case c.writeMu <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-c.writeMu }()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if beforeWrite != nil {
		if err := beforeWrite(); err != nil {
			return false, err
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = conn.SetWriteDeadline(time.Now()); close(done) })
	defer func() {
		if !stop() {
			<-done
		}
		_ = conn.SetWriteDeadline(time.Time{})
	}()
	if err := writeAll(conn, b); err != nil {
		// A partial frame cannot be followed by another frame safely. Only close
		// the transport here: Close may wait for the very upload doing this write.
		_ = conn.Close()
		return true, err
	}
	return true, nil
}

// Search collects token-matched responses for five seconds.
func (c *Client) Search(ctx context.Context, rawQuery string, users ...string) ([]SearchResult, error) {
	return c.collectSearch(ctx, rawQuery, false, users...)
}

// WishlistSearch uses the server's rate-limited automatic-search command.
func (c *Client) WishlistSearch(ctx context.Context, rawQuery string) ([]SearchResult, error) {
	return c.collectSearch(ctx, rawQuery, true)
}

func (c *Client) collectSearch(ctx context.Context, rawQuery string, wishlist bool, users ...string) ([]SearchResult, error) {
	targets, err := NormalizeSearchUsers(users)
	if err != nil {
		return nil, err
	}
	return c.collectSearchTargets(ctx, rawQuery, wishlist, targets, nil)
}

func (c *Client) collectSearchTargets(ctx context.Context, rawQuery string, wishlist bool, targets, rooms []string) ([]SearchResult, error) {
	allowed := make(map[string]bool, len(targets))
	for _, user := range targets {
		allowed[user] = true
	}
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
	// One cancellable sender feeds the existing collector while responses arrive.
	// Keep the five-second response window after the final request, with a separate
	// five-second bound on fan-out. Never spawn one worker per buddy or room.
	sendCtx, cancelSend := context.WithTimeout(ctx, 5*time.Second)
	sent := make(chan error, 1)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		send := func(request Message) error {
			_, err := c.sendTracked(sendCtx, request, func() error {
				c.mu.Lock()
				defer c.mu.Unlock()
				if c.done != done {
					return ErrNotConnected
				}
				return nil
			})
			return err
		}
		var err error
		switch {
		case len(rooms) > 0:
			for _, room := range rooms {
				if err = send(RoomSearchRequest{Room: room, Token: token, Query: query.wire}); err != nil {
					break
				}
			}
		case len(targets) > 0:
			for _, user := range targets {
				if err = send(UserSearchRequest{Username: user, Token: token, Query: query.wire}); err != nil {
					break
				}
			}
		case wishlist:
			err = send(WishlistSearchRequest{Token: token, Query: query.wire})
		default:
			err = send(SearchRequest{Token: token, Query: query.wire})
		}
		sent <- err
	}()
	defer func() { cancelSend(); <-workerDone }()
	var timer *time.Timer
	var timeout <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	var results []SearchResult
	for {
		select {
		case err := <-sent:
			sent = nil
			if err != nil {
				return nil, err
			}
			timer = time.NewTimer(5 * time.Second)
			timeout = timer.C
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-done:
			return nil, errors.New("soulseek: connection closed during search")
		case response := <-responses:
			if len(allowed) > 0 && !allowed[response.Username] {
				continue
			}
			for _, result := range response.Results {
				if len(allowed) > 0 && !allowed[result.Username] {
					continue
				}
				if query.matches(result) {
					results = append(results, result)
				}
			}
		case <-timeout:
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
	limits := c.BrowseLimits()
	ctx, operationID := c.browseContext(ctx)
	ctx, peer = c.browsePeer(ctx, peer)
	configurePeerRead(peer, progress, limits.MaxCompressedSize)
	c.log(ctx, slog.LevelInfo, "browse_operation_start", nil, slog.String("operation_id", operationID), slog.String("browse_type", map[bool]string{true: "group", false: "flat"}[path == ""]))
	if path == "" {
		select {
		case c.browseSlot <- struct{}{}:
			defer func() { <-c.browseSlot }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if err := writeMessage(peer, SharedListRequest{}); err != nil {
			c.log(ctx, slog.LevelError, "browse_request_failed", err, slog.String("operation_id", operationID), slog.String("stage", "request"))
			return nil, err
		}
		c.log(ctx, slog.LevelDebug, "browse_request_sent", nil, slog.String("operation_id", operationID), slog.Uint64("command", uint64(PeerGetSharedList)))
		command, payload, err := readFrameContextProgress(ctx, peer, progress, limits.MaxCompressedSize)
		if err != nil {
			c.log(ctx, slog.LevelError, "browse_read_failed", err, slog.String("operation_id", operationID), slog.String("stage", "response"))
			return nil, err
		}
		c.log(ctx, slog.LevelDebug, "browse_response_received", nil, slog.String("operation_id", operationID), slog.Uint64("command", uint64(command)), slog.Uint64("declared_size", uint64(len(payload))))
		if command != PeerSharedList {
			return nil, fmt.Errorf("%w: expected shared list", ErrMalformed)
		}
		started := time.Now()
		response, err := decodeSharedListResponse(payload, limits, c.logger(ctx))
		if err != nil {
			c.log(ctx, slog.LevelError, "browse_decode_failed", err, slog.String("operation_id", operationID), slog.String("stage", "body"))
		} else {
			c.log(ctx, slog.LevelInfo, "browse_decode_complete", nil, slog.String("operation_id", operationID), slog.Duration("decode_elapsed", time.Since(started)), slog.Int("entries", len(response.Entries)))
		}
		return response.Entries, err
	}
	cleanPath, err := NormalizePath(path)
	if err != nil {
		return nil, err
	}
	path = strings.ReplaceAll(cleanPath, "/", "\\")
	token := c.nextToken()
	if err := writeMessage(peer, FolderRequest{Token: token, Path: path}); err != nil {
		c.log(ctx, slog.LevelError, "browse_request_failed", err, slog.String("operation_id", operationID), slog.String("stage", "request"))
		return nil, err
	}
	c.log(ctx, slog.LevelDebug, "browse_request_sent", nil, slog.String("operation_id", operationID), slog.Uint64("command", uint64(PeerFolderContents)))
	command, payload, err := readFrameContextProgress(ctx, peer, nil, limits.MaxCompressedSize)
	if err != nil {
		c.log(ctx, slog.LevelError, "browse_read_failed", err, slog.String("operation_id", operationID), slog.String("stage", "response"))
		return nil, err
	}
	c.log(ctx, slog.LevelDebug, "browse_response_received", nil, slog.String("operation_id", operationID), slog.Uint64("command", uint64(command)), slog.Uint64("declared_size", uint64(len(payload))))
	if command != PeerFolderResponse {
		return nil, fmt.Errorf("%w: expected folder response", ErrMalformed)
	}
	started := time.Now()
	response, err := decodeFolderResponse(payload, limits, c.logger(ctx))
	if err != nil {
		c.log(ctx, slog.LevelError, "browse_decode_failed", err, slog.String("operation_id", operationID), slog.String("stage", "body"))
		return nil, err
	}
	c.log(ctx, slog.LevelInfo, "browse_decode_complete", nil, slog.String("operation_id", operationID), slog.Duration("decode_elapsed", time.Since(started)), slog.Int("entries", len(response.Entries)))
	responsePath, pathErr := NormalizePath(response.Path)
	if response.Token != token || pathErr != nil || !strings.EqualFold(cleanPath, responsePath) {
		return nil, fmt.Errorf("%w: folder response", ErrMalformed)
	}
	return response.Entries, nil
}
func writeMessage(w net.Conn, m Message) error {
	if lease := leasedPeer(w); lease != nil {
		return lease.writeMessage(m)
	}
	b, e := EncodeMessage(m)
	if e != nil {
		return e
	}
	return writeAll(w, b)
}
func readFrameContextProgress(ctx context.Context, c net.Conn, progress func(received, total uint64), maxFrameSize int) (uint32, []byte, error) {
	type rr struct {
		cmd uint32
		p   []byte
		e   error
	}
	ch := make(chan rr, 1)
	go func() { a, b, e := readFrame(c, progress, maxFrameSize); ch <- rr{a, b, e} }()
	select {
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	case x := <-ch:
		return x.cmd, x.p, x.e
	}
}

func (c *Client) connectAddress(ctx context.Context, addr, kind string) (net.Conn, error) {
	started := time.Now()
	c.log(ctx, slog.LevelDebug, "connect_started", nil, slog.String("stage", "dial"), slog.String("type", kind))
	peer, err := c.dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		c.log(ctx, slog.LevelInfo, "connect_failed", err, slog.String("stage", "dial"), slog.String("error_class", "dial"), slog.String("type", kind), slog.Duration("duration", time.Since(started)))
		return nil, err
	}
	peer = c.traceConn(ctx, peer, "", kind, "outgoing")
	logger := c.peerLogger(ctx, peer)
	diagnostics.Event(logger, slog.LevelDebug, "dial_complete", nil, slog.Int64("elapsed_ms", time.Since(started).Milliseconds()))
	stop := context.AfterFunc(ctx, func() { _ = peer.Close() })
	defer stop()
	if err = encodePeerHandshake(peer, PeerInitMessage{Username: c.cfg.Username, Type: kind, Token: 0}); err != nil {
		diagnostics.Event(logger, slog.LevelInfo, "handshake_failed", err, slog.String("stage", "send"), slog.String("error_class", "handshake"))
		peer.Close()
		return nil, err
	}
	diagnostics.Event(logger, slog.LevelDebug, "handshake_sent", nil, slog.String("stage", "send"))
	return peer, nil
}

func (c *Client) connectUser(ctx context.Context, username string) (net.Conn, error) {
	return c.acquirePeer(ctx, username)
}

func (c *Client) connectUserType(ctx context.Context, username, kind string) (net.Conn, error) {
	ctx = diagnostics.WithLogger(ctx, c.logger(ctx, slog.String("peer_username", username)))
	started := time.Now()
	c.log(ctx, slog.LevelDebug, "connect_user_started", nil, slog.String("stage", "address"), slog.String("type", kind), slog.String("peer_username", username))
	if username == "" {
		return nil, errors.New("soulseek: empty username")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	stop := context.AfterFunc(c.uploadRoot, cancel)
	defer stop()
	address, err := c.lookupPeerAddress(ctx, username)
	if err != nil {
		return nil, err
	}
	if address.IP == "0.0.0.0" {
		return nil, errors.New("soulseek: peer is offline")
	}
	// Like Nicotine+, resolve the address first, then overlap both routes.
	return racePeerConnections(ctx,
		func(ctx context.Context) (net.Conn, error) {
			ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			if address.Port == 0 {
				return nil, errors.New("soulseek: peer has no listening port")
			}
			return c.connectAddress(ctx, net.JoinHostPort(address.IP, fmt.Sprint(address.Port)), kind)
		},
		func(ctx context.Context) (net.Conn, error) { return c.connectIndirect(ctx, username, kind) },
		func(route string) {
			c.log(ctx, slog.LevelDebug, "connect_user_complete", nil, slog.String("stage", route), slog.String("type", kind), slog.String("peer_username", username), slog.Duration("duration", time.Since(started)))
		})
}

func (c *Client) lookupPeerAddress(ctx context.Context, username string) (PeerAddress, error) {
	c.mu.Lock()
	done, root := c.done, c.uploadRoot
	for _, pending := range c.addresses {
		if !pending.started.IsZero() && time.Since(pending.started) > time.Minute {
			// Address replies have no token. Retire this connection rather than
			// reusing correlations that a late response could satisfy incorrectly.
			conn := c.conn
			c.mu.Unlock()
			if conn != nil {
				_ = conn.Close()
			}
			return PeerAddress{}, ErrNotConnected
		}
	}
	lookup := c.addresses[username]
	owner := lookup == nil
	if owner && len(c.addresses) >= 256 {
		c.mu.Unlock()
		return PeerAddress{}, errors.New("soulseek: too many pending address lookups")
	}
	if owner {
		lookup = &peerAddressLookup{done: make(chan struct{}), started: time.Now()}
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
	case <-root.Done():
		return PeerAddress{}, ErrNotConnected
	case <-done:
		return PeerAddress{}, ErrNotConnected
	case <-lookup.done:
		return lookup.address, lookup.err
	}
}

func (c *Client) connectIndirect(ctx context.Context, username, kind string) (net.Conn, error) {
	c.log(ctx, slog.LevelDebug, "connect_indirect_started", nil, slog.String("stage", "reverse"), slog.String("type", kind), slog.String("peer_username", username))
	token := randomToken()
	connection := make(chan net.Conn, 1)
	c.mu.Lock()
	c.pierce[token] = connection
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pierce, token)
		select {
		case peer := <-connection:
			_ = peer.Close()
		default:
		}
		c.mu.Unlock()
	}()
	if err := c.sendContext(ctx, ConnectPeer{Token: token, Username: username, Kind: kind}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case peer := <-connection:
		if p, ok := peer.(*diagnosticConn); ok {
			peer = &diagnosticConn{Conn: p.Conn, id: p.id, opened: p.opened, logger: c.logger(ctx).With("connection_id", p.id, "route", "reverse", "peer_username", username, "type", kind)}
			diagnostics.Event(c.peerLogger(ctx, peer), slog.LevelDebug, "connection_associated", nil)
		}
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
	peer = c.traceConn(ctx, peer, instruction.Username, instruction.Kind, "reverse_response")
	var payload Encoder
	payload.U32(instruction.Token)
	if err := WriteInitFrame(peer, PeerPierceFirewall, payload.Payload()); err != nil {
		_ = peer.Close()
		return
	}
	diagnostics.Event(c.peerLogger(ctx, peer), slog.LevelDebug, "handshake_sent", nil, slog.String("stage", "reverse_response"))
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

// BrowseUserDirectoriesWithProgress fetches a complete shared list grouped by directory.
func (c *Client) BrowseUserDirectoriesWithProgress(ctx context.Context, username string, progress func(uint64, uint64)) ([]ShareDirectory, error) {
	ctx, _ = c.browseContext(ctx)
	limits := c.BrowseLimits()
	peer, err := c.connectUser(ctx, username)
	if err != nil {
		return nil, err
	}
	defer peer.Close()
	return c.browseSharedDirectories(ctx, peer, progress, limits)
}

// BrowseUserWithProgress reports compressed frame bytes for complete share lists.
func (c *Client) BrowseUserWithProgress(ctx context.Context, username, path string, progress func(received, total uint64)) ([]ShareEntry, error) {
	ctx, _ = c.browseContext(ctx)
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
	return c.DownloadWithStart(ctx, username, filename, size, offset, dst, progress, nil)
}

// DownloadWithStart is Download with a callback at the exact start of the data stream.
func (c *Client) DownloadWithStart(ctx context.Context, username, filename string, size, offset uint64, dst io.WriterAt, progress ProgressFunc, start func()) error {
	return c.downloadWithStart(ctx, username, filename, size, offset, dst, progress, start, nil, nil)
}

func (c *Client) downloadWithStart(ctx context.Context, username, filename string, size, offset uint64, dst io.WriterAt, progress ProgressFunc, start func(), offer *DownloadOffer, authorize func(netip.Addr) error) error {
	if dst == nil || offset > size {
		return ErrMalformed
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = diagnostics.WithLogger(ctx, c.logger(ctx, slog.String("peer_username", username)))
	observation := c.newObservation(ctx, "download", offset, size)
	defer c.endObservation(observation)
	if observation != nil {
		ctx = diagnostics.WithLogger(ctx, observation.logger)
	}
	pending := &pendingDownload{username: username, filename: filename, size: size, offset: offset, writer: observedWriterAt{WriterAt: dst, observation: observation}, progress: observedProgress(observation, progress), start: start, done: make(chan error, 1), ctx: ctx, observation: observation}
	pending.authorize = authorize
	key := downloadKey(username, filename)
	c.mu.Lock()
	if c.closing || c.closePending > 0 {
		c.mu.Unlock()
		return net.ErrClosed
	}
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
	if offer != nil {
		err := c.acceptDownload(offer.peer, pending, offer.request)
		offer.releaseLease()
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-pending.done:
			return err
		}
	}

	setupCtx, stopSetup := context.WithTimeout(ctx, downloadSetupTimeout)
	peer, err := c.connectUser(setupCtx, username)
	stopSetup()
	if err != nil {
		return err
	}
	defer peer.Close()
	ctx = diagnostics.WithLogger(ctx, c.peerLogger(ctx, peer))
	stopPeer := context.AfterFunc(ctx, func() { _ = peer.Close() })
	defer stopPeer()
	if authorize != nil {
		if err := authorize(peerIP(peer.RemoteAddr())); err != nil {
			return err
		}
	}
	if err := writeMessage(peer, QueueRequest{Filename: filename}); err != nil {
		return err
	}
	c.log(ctx, slog.LevelInfo, "transfer_queued", nil, slog.Uint64("resume_offset", offset), slog.Uint64("size", size))
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
					c.log(ctx, slog.LevelDebug, "control_closed_file_continues", message.err, slog.String("stage", "control"), slog.String("stage_detail", "observed_transport_end"))
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
				c.observationStage(ctx, pending.observation, "rejected", slog.LevelInfo, nil)
				c.log(ctx, slog.LevelWarn, "transfer_rejected", nil, slog.String("error_class", "remote_rejection"))
				return &DownloadRejectedError{Reason: reason}
			case PeerUploadFailed:
				failed, decodeErr := DecodeQueueFailed(message.payload)
				if decodeErr == nil && downloadKey(username, failed.Filename) == downloadKey(username, filename) {
					c.observationStage(ctx, pending.observation, "remote_upload_failed", slog.LevelWarn, ErrUploadFailed)
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
	if pending.authorize != nil {
		if err := pending.authorize(peerIP(peer.RemoteAddr())); err != nil {
			_ = writeMessage(peer, TransferResponse{Token: request.Token, Reason: "Cancelled"})
			return err
		}
	}
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
	attrs := []slog.Attr{slog.String("stage", "control"), slog.Uint64("offset", pending.offset), slog.Uint64("size", request.Size)}
	if pending.observation != nil {
		attrs = append(attrs, slog.String("linked_to", pending.observation.id))
	}
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
	if o := pending.observation; o != nil {
		o.mu.Lock()
		o.size = request.Size
		o.mu.Unlock()
	}
	c.log(pending.ctx, slog.LevelDebug, "transfer_accepted", nil, attrs...)
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
		c.log(context.Background(), slog.LevelInfo, "file_token_unmatched", nil, slog.String("stage", "file_handshake"), slog.String("error_class", "unknown_token"))
		return
	}
	if pending.authorize != nil {
		if err := pending.authorize(peerIP(peer.RemoteAddr())); err != nil {
			pending.finish(err)
			return
		}
	}
	fileLogger := c.linkFileLogger(pending.ctx, peer)
	fileCtx := diagnostics.WithLogger(pending.ctx, fileLogger)
	diagnostics.Event(fileLogger, slog.LevelDebug, "file_token_matched", nil, slog.String("stage", "file_handshake"))
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
		diagnostics.Event(fileLogger, slog.LevelError, "file_setup_failed", err, slog.String("stage", "resume_offset"))
		pending.finish(err)
		return
	}
	if err := peer.SetDeadline(time.Time{}); err != nil {
		diagnostics.Event(fileLogger, slog.LevelError, "file_setup_failed", err, slog.String("stage", "clear_deadline"))
		pending.finish(err)
		return
	}
	if pending.start != nil {
		pending.start()
	}
	pending.observation.begin(fileLogger)
	reader := downloadReader{ctx: pending.ctx, limiter: &c.downloadLimit, src: observedReader{Reader: peer, observation: pending.observation}}
	err := CopyAtMost(pending.ctx, pending.writer, reader, pending.size, pending.offset, pending.progress)
	if err != nil {
		c.observationStage(fileCtx, pending.observation, "transfer_failed", slog.LevelError, err)
	} else {
		c.observationStage(fileCtx, pending.observation, "transfer_completed", slog.LevelInfo, nil)
	}
	pending.finish(err)
}

func (c *Client) incomingSearchResults(query string) []SearchResult {
	return c.incomingSearchResultsFor(query, "", netip.Addr{})
}

func (c *Client) incomingSearchResultsFor(query, username string, address netip.Addr) []SearchResult {
	c.mu.Lock()
	index, policy := c.cfg.Share, c.incomingSearch
	excluded := append([]string(nil), c.excludedSearchPhrases...)
	c.mu.Unlock()
	if index == nil || !policy.Respond || utf8.RuneCountInString(query) < policy.MinimumLength {
		return nil
	}
	permission := c.sharePermission(username, address)
	if permission.Banned {
		return nil
	}
	files := index.search(query, policy.MaximumResults, func(file ShareFile) bool {
		return c.shareVisibility(permission, file.Root) != ShareHidden
	})
	results := searchToResults(files, excluded)
	for i := range results {
		results[i].Public = c.shareVisibility(permission, shareRoot(results[i].Path)) == ShareAllowed
	}
	return results
}

// respondSearch admits work before spawning: excess searches are best-effort and dropped.
func (c *Client) respondSearch(search IncomingSearch) {
	if ValidateUsername(search.Username) != nil {
		return
	}
	select {
	case c.searchSlots <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-c.searchSlots }()
		c.mu.Lock()
		policy, index := c.incomingSearch, c.cfg.Share
		c.mu.Unlock()
		if !policy.Respond || utf8.RuneCountInString(search.Query) < policy.MinimumLength {
			return
		}
		// Avoid peer connections for searches that cannot match any share tier.
		if index == nil || len(index.Search(search.Query, 1)) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(c.baseContext(), 10*time.Second)
		policyCtx := c.shareResponseContext()
		stopPolicy := context.AfterFunc(policyCtx, cancel)
		defer stopPolicy()
		defer cancel()
		// Search replies are one-shot: receivers may close after each response.
		peer, err := c.connectUserType(ctx, search.Username, "P")
		if err != nil {
			return
		}
		defer peer.Close()
		stop := context.AfterFunc(ctx, func() { _ = peer.Close() })
		defer stop()
		results := c.incomingSearchResultsFor(search.Query, search.Username, peerIP(peer.RemoteAddr()))
		if len(results) == 0 {
			return
		}
		_ = writeShareMessage(policyCtx, peer, SearchResponse{Username: c.cfg.Username, Token: search.Token, Results: results, SlotFree: true})
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
	go func() {
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
	p = c.traceConn(context.Background(), p, "", "", "incoming")
	_ = p.SetReadDeadline(time.Now().Add(2 * time.Second))
	initCmd, b, e := ReadInitFrame(p)
	_ = p.SetReadDeadline(time.Time{})
	if e != nil {
		_ = p.Close()
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
		defer c.mu.Unlock()
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
			c.log(context.Background(), slog.LevelInfo, "handshake_failed", err, slog.String("stage", "receive"), slog.String("error_class", "handshake"))
			return
		}
		logger := c.peerLogger(context.Background(), p).With("peer_username", peerInfo.Username, "type", peerInfo.Type)
		diagnostics.Event(logger, slog.LevelDebug, "handshake_received", nil, slog.String("stage", "receive"))
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
	if ValidateUsername(peerInfo.Username) != nil {
		return
	}
	// A completed handshake can still be the losing direct/reverse route.
	// Only replace the active socket once the remote actually uses this one.
	conn := peer
	c.mu.Lock()
	stop := context.AfterFunc(c.uploadRoot, func() { _ = conn.Close() })
	c.mu.Unlock()
	defer stop()
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	buffer := bufio.NewReader(conn)
	header, err := buffer.Peek(8)
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	peer = &bufferedPeer{Conn: conn, reader: buffer}
	if d, ok := conn.(*diagnosticConn); ok {
		peer = &diagnosticConn{Conn: peer, logger: d.logger, id: d.id, opened: d.opened}
	}
	// A separate search response must not evict a browse or transfer socket.
	if binary.LittleEndian.Uint32(header[4:]) == PeerSearch {
		command, payload, err := ReadFrame(peer)
		if err == nil {
			c.handleMessagePeer(peer, peerInfo, command, payload)
		}
		return
	}
	p := c.installPeer(peerInfo.Username, peer, true)
	<-p.done
}

func (c *Client) handleMessagePeer(peer net.Conn, peerInfo PeerInitMessage, command uint32, payload []byte) {
	switch command {
	case PeerUserInfoRequest:
		if len(payload) != 0 {
			return
		}
		if err := writeMessage(peer, c.SelfProfile()); err != nil {
			return
		}
	case PeerGetSharedList:
		if len(payload) == 0 {
			ctx := c.shareResponseContext()
			if writeShareMessage(ctx, peer, SharedListResponse{Entries: c.shareEntriesFor(peerInfo.Username, peerIP(peer.RemoteAddr()))}) != nil {
				return
			}
		}
	case PeerFolderContents:
		d := NewDecoder(payload)
		token, err := d.U32()
		if err != nil {
			return
		}
		path, err := d.String()
		if err != nil || d.Done() != nil {
			return
		}
		ctx := c.shareResponseContext()
		entries, _ := c.shareIndex().Subtree(path)
		permission := c.sharePermission(peerInfo.Username, peerIP(peer.RemoteAddr()))
		entries = c.filterShareEntries(entries, permission)
		if writeShareMessage(ctx, peer, FolderResponse{Token: token, Path: path, Entries: entries}) != nil {
			return
		}
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
			return
		}
		if request.Direction == 0 {
			_, _, err := c.registerUploadWithAddress(peerInfo.Username, request.Filename, true, peerIP(peer.RemoteAddr()), false, "")
			if err != nil {
				_ = writeMessage(peer, TransferResponse{Token: request.Token, Accepted: false, Reason: uploadDenial(err)})
				return
			}
			_ = writeMessage(peer, TransferResponse{Token: request.Token, Accepted: false, Reason: "Queued"})
			return
		}
		if request.Direction != 1 {
			return
		}
		clean, cleanErr := NormalizePath(request.Filename)
		if cleanErr != nil {
			_ = writeMessage(peer, TransferResponse{Token: request.Token, Accepted: false, Reason: "Cancelled"})
			return
		}
		c.mu.Lock()
		pending := c.requested[downloadKey(peerInfo.Username, clean)]
		c.mu.Unlock()
		if pending == nil {
			c.offerDownload(peer, peerInfo.Username, clean, request)
			return
		}
		if err := c.acceptDownload(peer, pending, request); err != nil {
			pending.finish(err)
		}
	case PeerPlaceInQueueRequest:
		filename, err := parseStringPayload(payload)
		if err != nil {
			return
		}
		if c.writeUploadPosition(peer, peerInfo.Username, filename) != nil {
			return
		}
	case PeerQueueUpload:
		filename, err := parseStringPayload(payload)
		if err != nil {
			return
		}
		a, _, err := c.registerUploadWithAddress(peerInfo.Username, filename, true, peerIP(peer.RemoteAddr()), false, "")
		if err != nil {
			_ = writeMessage(peer, QueueDenied{Filename: filename, Reason: uploadDenial(err)})
			return
		}
		if c.writeUploadPosition(peer, peerInfo.Username, a.target.Filename) != nil {
			return
		}
	}
}

func (c *Client) shareEntries() []ShareEntry {
	return c.shareEntriesFor("", netip.Addr{})
}

func (c *Client) shareEntriesFor(username string, address netip.Addr) []ShareEntry {
	permission := c.sharePermission(username, address)
	if permission.Banned {
		return nil
	}
	files := c.shareIndex().Files()
	out := make([]ShareEntry, 0, len(files))
	for _, file := range files {
		visibility := c.shareVisibility(permission, file.Root)
		if visibility == ShareHidden {
			continue
		}
		entry := file.entry(file.Root + "\\" + strings.ReplaceAll(file.Path, "/", "\\"))
		if visibility == ShareLocked {
			entry.Private = true
		}
		out = append(out, entry)
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
			out = append(out, SearchResult{Path: path, Size: file.Size, IsDirectory: file.Directory, Bitrate: file.Bitrate, Duration: file.Duration, SampleRate: file.SampleRate, BitDepth: file.BitDepth, Extension: strings.TrimPrefix(strings.ToLower(filepath.Ext(file.Path)), "."), Public: true})
		}
	}
	return out
}

// Close stops listener and connection, then waits for every upload attempt.
func (c *Client) Close() error {
	// Cancel a blocked dial before waiting for its lifecycle transition.
	c.mu.Lock()
	c.closePending++
	defer func() {
		c.mu.Lock()
		c.closePending--
		c.mu.Unlock()
	}()
	dialCancel := c.connectCancel
	c.mu.Unlock()
	if dialCancel != nil {
		dialCancel()
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
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
	c.peerWG.Wait()
	return closeErr
}
