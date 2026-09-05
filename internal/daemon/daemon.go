package daemon

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/portmap"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

var (
	ErrClosed                = errors.New("daemon: closed")
	ErrNotStarted            = errors.New("daemon: not connected")
	ErrSearchNotFound        = errors.New("daemon: search not found")
	ErrListenPortUnavailable = errors.New("daemon: listening port unavailable")
	ErrScanBusy              = errors.New("daemon: share scan already in progress")
)

type Status string

const (
	StatusStopped      Status = "stopped"
	StatusConnecting   Status = "connecting"
	StatusConnected    Status = "connected"
	StatusReconnecting Status = "reconnecting"
	StatusError        Status = "error"
)

type Presence string

const (
	PresenceOffline Presence = "offline"
	PresenceAway    Presence = "away"
	PresenceOnline  Presence = "online"
)

const searchPageSize = 100

type ShareScan struct {
	ID          uint64    `json:"id"`
	State       string    `json:"state"`
	Root        string    `json:"root,omitempty"`
	Files       uint64    `json:"files"`
	Directories uint64    `json:"directories"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	ElapsedMS   int64     `json:"elapsed_ms"`
	Error       string    `json:"error,omitempty"`
}

type Snapshot struct {
	Status               Status               `json:"status"`
	Presence             Presence             `json:"presence"`
	Error                string               `json:"error,omitempty"`
	PublicIP             string               `json:"public_ip,omitempty"`
	PublicPort           uint16               `json:"public_port,omitempty"`
	Config               config.SafeConfig    `json:"config"`
	Shares               []config.Share       `json:"shares"`
	ShareScan            *ShareScan           `json:"share_scan,omitempty"`
	ShareIndexRevision   uint64               `json:"share_index_revision"`
	DownloadNotification DownloadNotification `json:"download_notification"`
	Downloads            []Download           `json:"downloads"`
	Transfers            []Transfer           `json:"transfers"`
}

type PasswordChangeResult struct {
	Changed bool   `json:"changed"`
	Saved   bool   `json:"saved"`
	Warning string `json:"warning,omitempty"`
}

type SearchResult struct {
	Username    string `json:"username"`
	Path        string `json:"path"`
	Extension   string `json:"extension,omitempty"`
	CountryCode string `json:"country_code,omitempty"`
	Size        uint64 `json:"size"`
	Directory   bool   `json:"directory"`
	SlotFree    bool   `json:"slot_free"`
	Speed       uint32 `json:"speed"`
	Queue       uint32 `json:"queue"`
	Bitrate     uint32 `json:"bitrate,omitempty"`
	Duration    uint32 `json:"duration,omitempty"`
	VBR         bool   `json:"vbr,omitempty"`
	SampleRate  uint32 `json:"sample_rate,omitempty"`
	BitDepth    uint32 `json:"bit_depth,omitempty"`
	Public      bool   `json:"public"`
}
type Search struct {
	ID      string
	Query   string
	Results []SearchResult
}
type SearchPage struct {
	ID         string         `json:"id"`
	Query      string         `json:"query"`
	Results    []SearchResult `json:"results"`
	Cursor     int            `json:"cursor"`
	NextCursor int            `json:"next_cursor,omitempty"`
	Total      int            `json:"total"`
	FoundTotal int            `json:"found_total"`
}

type Transfer struct {
	ID         string  `json:"id"`
	Username   string  `json:"username"`
	Filename   string  `json:"filename"`
	Direction  string  `json:"direction"`
	State      string  `json:"state"`
	Done       uint64  `json:"done"`
	Total      uint64  `json:"total"`
	ElapsedMS  *uint64 `json:"elapsed_ms"`
	SpeedBPS   uint64  `json:"speed_bps"`
	ETASeconds *uint64 `json:"eta_seconds"`
	Queue      uint32  `json:"queue,omitempty"`
	Error      string  `json:"error,omitempty"`
}

type Download struct {
	ID          string    `json:"id"`
	Username    string    `json:"username"`
	Filename    string    `json:"filename"`
	Size        uint64    `json:"size"`
	Offset      uint64    `json:"offset"`
	DownloadDir string    `json:"download_dir,omitempty"`
	Destination string    `json:"destination"`
	State       string    `json:"state"` // queued, incomplete, running, finalizing, paused, retrying, completed, cancelled, failed
	RetryAt     time.Time `json:"retry_at,omitempty"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type Journal struct {
	DownloadSequence uint64     `json:"download_sequence"`
	Downloads        []Download `json:"downloads"`
}

type DownloadItem struct {
	Filename    string `json:"filename"`
	Size        uint64 `json:"size"`
	Offset      uint64 `json:"offset,omitempty"`
	Destination string `json:"destination,omitempty"`
}
type DownloadRequest struct {
	Username    string         `json:"username"`
	DownloadDir string         `json:"download_dir,omitempty"`
	Files       []DownloadItem `json:"files"`
}

type FolderDownloadRequest struct {
	Username    string         `json:"username"`
	DownloadDir string         `json:"download_dir,omitempty"`
	Folder      string         `json:"folder"`
	Subfolders  []string       `json:"subfolders,omitempty"`
	Files       []DownloadItem `json:"files,omitempty"`
	Recursive   bool           `json:"recursive"`
}

type portMapping interface {
	Close() error
}

type portMappingOpener func(context.Context, uint16, bool, bool, func(uint16)) (portMapping, error)

type Service struct {
	mu                     sync.RWMutex
	lifecycleMu            sync.Mutex
	uploadMu               sync.Mutex
	uploadEpoch            uint64
	uploadOwners           map[string]uploadOwner
	cfg                    config.Config
	configPath             string
	runCtx                 context.Context
	runCancel              context.CancelFunc
	shares                 *soulseek.ShareIndex
	client                 *soulseek.Client
	mapping                portMapping
	portMapOpen            portMappingOpener
	portCheck              listeningPortChecker
	journal                Journal
	searches               map[string]Search
	wishlist               []wishlistEntry
	wishlistPath           string
	wishlistNextID         uint64
	wishlistCursor         int
	wishlistServerInterval time.Duration
	wishlistWake           chan struct{}
	wishlistSearch         func(context.Context, *soulseek.Client, string, bool) ([]soulseek.SearchResult, error)
	wishlistNotify         func(context.Context, string, int) error
	browses                map[string]loadedBrowse
	browseProgress         map[string]trackedBrowse
	fullBrowse             fullBrowseFunc
	browseSeq              uint64
	browseProgressSeq      uint64
	remoteSharesDir        string
	transfers              map[string]Transfer
	transferTiming         map[string]transferTiming
	downloadSlots          chan struct{}
	downloadCancels        map[string]context.CancelFunc
	downloadDone           map[string]chan struct{}
	downloadPeers          map[string]chan struct{}
	ctx                    context.Context
	cancel                 context.CancelFunc
	shareWatchCancel       context.CancelFunc
	shareIndexBuilder      func(context.Context, []config.Share) (*soulseek.ShareIndex, error)
	downloadNotification   DownloadNotification
	desktopNotify          func(context.Context, string, string) error
	shareScanGate          chan struct{}
	scanCtx                context.Context
	scanCancel             context.CancelFunc
	activeScanCancel       context.CancelCauseFunc
	shareCancelEpoch       uint64
	shareCancelWake        chan struct{}
	shareScanID            uint64
	shareScan              *ShareScan
	shareIndexRevision     uint64
	shareIndexPath         string
	shareRescanDelay       time.Duration
	shareWatchGeneration   uint64
	listenPortFile         string
	listenPortInterval     time.Duration
	listenPort             uint16
	reconnectWake          chan struct{}
	closed                 bool
	requeueDownloads       bool
	status                 Status
	presence               Presence
	lastErr                string
	seq                    uint64
	journalPath            string
	wg                     sync.WaitGroup
	sessionWG              sync.WaitGroup
	downloadWG             sync.WaitGroup
}

func validateConfig(cfg config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := ValidateSearchFilter(cfg.Search.DefaultFilter); err != nil {
		return fmt.Errorf("config: invalid search default filter: %w", err)
	}
	return nil
}

func New(cfg config.Config, path string) (*Service, error) {
	rules, err := config.NormalizeShareExclusions(cfg.ShareExclusions)
	if err != nil {
		return nil, err
	}
	cfg.ShareExclusions = rules
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	index, err := soulseek.NewShareIndexWithExclusions(rules)
	if err != nil {
		return nil, err
	}
	scanCtx, scanCancel := context.WithCancel(context.Background())
	s := &Service{downloadNotification: DownloadNotification{SessionID: rand.Text()}, desktopNotify: notifyDesktop, scanCtx: scanCtx, scanCancel: scanCancel, cfg: cfg, configPath: config.ConfigPath(), shares: index, searches: make(map[string]Search), wishlistWake: make(chan struct{}, 1), wishlistSearch: func(ctx context.Context, client *soulseek.Client, query string, automatic bool) ([]soulseek.SearchResult, error) {
		if automatic {
			return client.WishlistSearch(ctx, query)
		}
		return client.Search(ctx, query)
	}, wishlistNotify: notifyWishlist, browses: make(map[string]loadedBrowse), browseProgress: make(map[string]trackedBrowse), fullBrowse: func(ctx context.Context, client *soulseek.Client, username string, progress func(received, total uint64)) ([]soulseek.ShareEntry, error) {
		return client.BrowseUserWithProgress(ctx, username, "", progress)
	}, transfers: make(map[string]Transfer), downloadSlots: make(chan struct{}, cfg.DownloadSlots), downloadCancels: make(map[string]context.CancelFunc), downloadDone: make(map[string]chan struct{}), downloadPeers: make(map[string]chan struct{}), shareScanGate: make(chan struct{}, 1), shareRescanDelay: DefaultShareRescanDelay, listenPortInterval: DefaultListenPortReconcileInterval, portMapOpen: func(ctx context.Context, port uint16, natPMP, upnp bool, changed func(uint16)) (portMapping, error) {
		return portmap.Open(ctx, port, natPMP, upnp, changed)
	}, portCheck: defaultListeningPortCheck, reconnectWake: make(chan struct{}, 1), status: StatusStopped, presence: PresenceOffline, journalPath: path}
	s.shareIndexPath = filepath.Join(filepath.Dir(path), "shares.json")
	s.remoteSharesDir = filepath.Join(filepath.Dir(path), "usershares")
	s.wishlistPath = filepath.Join(filepath.Dir(path), "wishlist.json")
	wishlist, err := loadWishlist(s.wishlistPath)
	if err != nil {
		return nil, fmt.Errorf("daemon: load wishlist: %w", err)
	}
	s.wishlist, s.wishlistNextID = wishlist.Items, wishlist.NextID
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &s.journal); err != nil {
			return nil, fmt.Errorf("daemon: load journal: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	s.seq = s.journal.DownloadSequence
	for i := range s.journal.Downloads {
		if s.journal.Downloads[i].State == "" {
			s.journal.Downloads[i].State = "queued"
		}
		if n := strings.TrimPrefix(s.journal.Downloads[i].ID, "d-"); n != s.journal.Downloads[i].ID {
			if v, err := strconv.ParseUint(n, 10, 64); err == nil && v > s.seq {
				s.seq = v
			}
		}
		d := s.journal.Downloads[i]
		s.transfers[d.ID] = Transfer{ID: d.ID, Username: d.Username, Filename: d.Filename, Direction: "download", State: d.State, Done: d.Offset, Total: d.Size, Error: d.Error}
	}
	return s, nil
}

func (s *Service) SetConfigPath(path string) {
	s.mu.Lock()
	if path != "" {
		s.configPath = path
	}
	s.mu.Unlock()
}
func (s *Service) Config() config.SafeConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Redacted()
}

func (s *Service) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	publicIP, publicPort := "", uint16(0)
	if s.status == StatusConnected && s.client != nil {
		publicIP, publicPort = s.client.PublicIP(), s.client.PublicPort()
	}
	var scan *ShareScan
	if s.shareScan != nil {
		x := *s.shareScan
		if x.State == "scanning" || x.State == "cancelling" || x.State == "publishing" {
			x.ElapsedMS = time.Since(x.StartedAt).Milliseconds()
		}
		scan = &x
	}
	return Snapshot{Status: s.status, Presence: s.presence, Error: s.lastErr, PublicIP: publicIP, PublicPort: publicPort, Config: s.cfg.Redacted(), Shares: append([]config.Share(nil), s.cfg.Shares...), ShareScan: scan, ShareIndexRevision: s.shareIndexRevision, DownloadNotification: s.downloadNotification, Downloads: append([]Download(nil), s.journal.Downloads...), Transfers: s.transferValuesLocked(time.Now())}
}

// Start initializes daemon-owned work and optionally starts a Soulseek session.
func (s *Service) Start(ctx context.Context) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.runCancel != nil {
		s.mu.Unlock()
		return nil
	}
	s.runCtx, s.runCancel = context.WithCancel(ctx)
	runCtx := s.runCtx
	shares := append([]config.Share(nil), s.cfg.Shares...)
	rules := slices.Clone(s.cfg.ShareExclusions)
	generation := s.shareWatchGeneration
	revision := s.shareIndexRevision
	connect := s.cfg.Soulseek.ConnectOnStartup
	s.mu.Unlock()

	if idx, err := loadShareIndexCache(s.shareIndexPath, shares, rules); err == nil {
		s.mu.Lock()
		// A manual scan may have published while the cache was loading.
		if !s.closed && runCtx.Err() == nil && s.shareIndexRevision == revision {
			s.shares = idx
			s.shareIndexRevision++
		}
		s.mu.Unlock()
	} else {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("load share index cache: %v", err)
		}
		if err := s.runShareScan(runCtx, shares, rules, generation, false, nil, func(index *soulseek.ShareIndex) error {
			return s.publishShareIndex(generation, index)
		}); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrScanCancelled) {
			s.mu.Lock()
			s.lastErr = err.Error()
			s.mu.Unlock()
		}
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	s.restartShareWatcherLocked()
	s.startListenPortWatcherLocked()
	s.wg.Add(1)
	go s.wishlistLoop(s.runCtx)
	s.mu.Unlock()
	if connect {
		return s.setPresenceLocked(PresenceOnline)
	}
	return nil
}

func (s *Service) SetPresence(presence Presence) error {
	if presence != PresenceOffline && presence != PresenceAway && presence != PresenceOnline {
		return fmt.Errorf("daemon: invalid presence %q", presence)
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	if presence == PresenceOffline {
		s.stopSessionLocked(true)
		return nil
	}
	return s.setPresenceLocked(presence)
}

func (s *Service) ChangePassword(ctx context.Context, password string) (PasswordChangeResult, error) {
	if strings.TrimSpace(password) == "" {
		return PasswordChangeResult{}, errors.New("daemon: password cannot be empty")
	}
	if _, overridden := os.LookupEnv("OTO_PASSWORD"); overridden {
		return PasswordChangeResult{}, errors.New("daemon: password is controlled by OTO_PASSWORD; update that environment source directly")
	}

	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()
	if client == nil {
		return PasswordChangeResult{}, ErrNotStarted
	}
	if err := client.ChangePassword(ctx, password); err != nil {
		return PasswordChangeResult{}, err
	}

	result := PasswordChangeResult{Changed: true}
	s.mu.Lock()
	s.cfg.Soulseek.Password = password
	err := s.cfg.Save(s.configPath)
	s.mu.Unlock()
	if err != nil {
		result.Warning = fmt.Sprintf("password changed on Soulseek but config was not saved: %v; fix the config path and press s to retry", err)
		return result, nil
	}
	result.Saved = true
	return result, nil
}

func (s *Service) setPresenceLocked(presence Presence) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.runCtx == nil {
		s.mu.Unlock()
		return ErrNotStarted
	}
	if s.presence == presence && s.client != nil {
		s.mu.Unlock()
		return nil
	}
	s.presence = presence
	client, active := s.client, s.cancel != nil
	s.mu.Unlock()

	if client != nil {
		status := soulseek.UserStatusOnline
		if presence == PresenceAway {
			status = soulseek.UserStatusAway
		}
		if err := client.SetStatus(status); err != nil {
			_ = client.Close()
			s.wakeReconnect()
		}
		return nil
	}
	if active {
		s.wakeReconnect()
		return nil
	}
	return s.startSessionLocked()
}

func (s *Service) startSessionLocked() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.runCtx == nil {
		s.mu.Unlock()
		return ErrNotStarted
	}
	if s.cancel != nil {
		s.mu.Unlock()
		s.wakeReconnect()
		return nil
	}
	select {
	case <-s.reconnectWake:
	default:
	}
	ctx, cancel := context.WithCancel(s.runCtx)
	s.ctx, s.cancel = ctx, cancel
	s.status, s.lastErr = StatusConnecting, ""
	s.sessionWG.Add(1)
	s.mu.Unlock()

	s.resumeDownloads()
	go s.reconnectLoop(ctx)
	s.sessionWG.Add(1)
	go s.retryDownloadsLoop(ctx)
	return nil
}

// stopSessionLocked requires lifecycleMu and preserves partial downloads.
func (s *Service) stopSessionLocked(offline bool) {
	s.uploadMu.Lock()
	s.mu.Lock()
	s.retireUploadsLocked()
	cancel, client, mapping := s.cancel, s.client, s.mapping
	s.ctx, s.cancel, s.client, s.mapping = nil, nil, nil, nil
	s.wishlistServerInterval = 0
	s.requeueDownloads = cancel != nil
	s.status, s.lastErr = StatusStopped, ""
	if offline {
		s.presence = PresenceOffline
	}
	s.mu.Unlock()
	s.wakeWishlist()
	if cancel != nil {
		cancel()
	}
	closePortMapping(mapping)
	if client != nil {
		_ = client.Close()
	}
	s.uploadMu.Unlock()
	if cancel != nil {
		s.sessionWG.Wait()
		s.downloadWG.Wait()
	}
	s.mu.Lock()
	s.requeueDownloads = false
	s.mu.Unlock()
}

func uploadPolicy(c config.Config) soulseek.UploadPolicy {
	return soulseek.UploadPolicy{Scheduling: string(c.Uploads.Scheduling), BytesPerSecond: int64(c.Uploads.ActiveSpeedLimitKiB()) * 1024, PerTransfer: c.Uploads.LimitScope == config.UploadLimitPerTransfer}
}

func incomingSearchPolicy(c config.Config) soulseek.IncomingSearchPolicy {
	return soulseek.IncomingSearchPolicy{
		Respond:        c.Search.RespondToIncomingSearches,
		MinimumLength:  c.Search.MinimumIncomingSearchLength,
		MaximumResults: c.Search.MaximumIncomingSearchResults,
	}
}

func newUploadManager(c config.Config) *soulseek.UploadManager {
	uploads := soulseek.NewUploadManager(c.UploadSlots)
	uploads.Configure(uploadPolicy(c))
	return uploads
}
func (s *Service) connectOnce(ctx context.Context) error {
	s.mu.RLock()
	if s.ctx != ctx {
		s.mu.RUnlock()
		return context.Canceled
	}
	cfg, idx := s.cfg, s.shares
	portFile, port, openMapping := s.listenPortFile, s.listenPort, s.portMapOpen
	s.mu.RUnlock()
	if portFile != "" {
		if port == 0 {
			return ErrListenPortUnavailable
		}
		var err error
		cfg.Soulseek.ListenAddr, err = listenAddressWithPort(cfg.Soulseek.ListenAddr, port)
		if err != nil {
			return err
		}
	}
	searchPolicy := incomingSearchPolicy(cfg)
	s.mu.Lock()
	if s.closed || s.ctx != ctx || s.presence == PresenceOffline {
		s.mu.Unlock()
		return context.Canceled
	}
	s.uploadEpoch++
	epoch := s.uploadEpoch
	s.mu.Unlock()
	client := soulseek.NewClient(soulseek.ClientConfig{
		Address: cfg.Soulseek.Server, Username: cfg.Soulseek.Username, Password: cfg.Soulseek.Password,
		ListenAddr: cfg.Soulseek.ListenAddr, NetworkInterface: cfg.Soulseek.NetworkInterface,
		Share: idx, Uploads: newUploadManager(cfg), IncomingSearch: &searchPolicy,
		UploadUpdate:      func(event soulseek.TransferEvent) { s.uploadUpdate(epoch, event) },
		UploadStreamStart: func(event soulseek.TransferEvent) { s.uploadStreamStart(epoch, event) },
	})
	if err := client.Connect(ctx); err != nil {
		return err
	}

	var mapping portMapping
	if portFile != "" {
		if cfg.Soulseek.NATPMPPortMapping || cfg.Soulseek.UPnPPortMapping {
			log.Printf("port mapping: skipped because --listen-port-file is configured")
		}
	} else if cfg.Soulseek.NetworkInterface != "" {
		if cfg.Soulseek.NATPMPPortMapping || cfg.Soulseek.UPnPPortMapping {
			log.Printf("port mapping: skipped because network interface %q is configured", cfg.Soulseek.NetworkInterface)
		}
	} else if cfg.Soulseek.NATPMPPortMapping || cfg.Soulseek.UPnPPortMapping {
		opened, err := openMapping(ctx, client.ListenPort(), cfg.Soulseek.NATPMPPortMapping, cfg.Soulseek.UPnPPortMapping, func(port uint16) {
			if err := client.SetAdvertisedPort(port); err != nil {
				log.Printf("port mapping: advertise external port %d: %v", port, err)
			}
		})
		if err != nil {
			log.Printf("port mapping: %v; continuing without automatic forwarding", err)
		} else {
			mapping = opened
		}
	}
	if err := client.Login(ctx); err != nil {
		closePortMapping(mapping)
		_ = client.Close()
		return err
	}
	s.mu.Lock()
	if s.closed || s.ctx != ctx || s.presence == PresenceOffline {
		s.mu.Unlock()
		closePortMapping(mapping)
		_ = client.Close()
		return context.Canceled
	}
	if s.presence == PresenceAway {
		if err := client.SetStatus(soulseek.UserStatusAway); err != nil {
			s.mu.Unlock()
			closePortMapping(mapping)
			_ = client.Close()
			return err
		}
	}
	s.client, s.mapping = client, mapping
	idx = s.shares
	s.status, s.lastErr = StatusConnected, ""
	s.mu.Unlock()
	client.SetShareIndex(idx)
	return nil
}

func closePortMapping(mapping portMapping) {
	if mapping != nil {
		if err := mapping.Close(); err != nil {
			log.Printf("port mapping: remove: %v", err)
		}
	}
}

func (s *Service) setSessionStatus(ctx context.Context, status Status, message string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx != ctx {
		return false
	}
	s.status, s.lastErr = status, message
	return true
}

func (s *Service) reconnectLoop(ctx context.Context) {
	defer s.sessionWG.Done()
	backoff, delay := time.Second, time.Duration(0)
	for {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-s.reconnectWake:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				backoff = time.Second
			case <-timer.C:
			}
			delay = 0
		}

		s.mu.RLock()
		client, mapping, current := s.client, s.mapping, s.ctx == ctx
		s.mu.RUnlock()
		if !current || ctx.Err() != nil {
			return
		}

		var err error
		if client == nil {
			err = s.connectOnce(ctx)
		} else {
			eventCtx, stopEvents := context.WithCancel(ctx)
			eventsDone := make(chan struct{})
			go func() { s.consumeClientEvents(eventCtx, client); close(eventsDone) }()
			err = client.Run(ctx)
			stopEvents()
			<-eventsDone
			closePortMapping(mapping)
			s.uploadMu.Lock()
			s.mu.Lock()
			if s.client == client {
				s.retireUploadsLocked()
				s.client, s.mapping = nil, nil
				s.wishlistServerInterval = 0
			}
			s.mu.Unlock()
			_ = client.Close()
			s.uploadMu.Unlock()
			s.wakeWishlist()
		}
		if err == nil {
			backoff = time.Second
			continue
		}
		if ctx.Err() != nil {
			return
		}

		message := s.safeError(err)
		if isPermanentLoginError(err) {
			if !s.setSessionStatus(ctx, StatusError, message) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-s.reconnectWake:
			}
			if !s.setSessionStatus(ctx, StatusConnecting, "") {
				return
			}
			backoff = time.Second
			continue
		}
		if !s.setSessionStatus(ctx, StatusReconnecting, message) {
			return
		}
		delay = backoff
		if backoff < time.Minute {
			backoff = min(2*backoff, time.Minute)
		}
	}
}

func isPermanentLoginError(err error) bool {
	x := strings.ToLower(errString(err))
	return strings.Contains(x, "invalid") && strings.Contains(x, "credential") || strings.Contains(x, "password") || strings.Contains(x, "same user") || strings.Contains(x, "already logged") || strings.Contains(x, "kicked")
}
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
func (s *Service) safeError(err error) string {
	x := errString(err)
	s.mu.RLock()
	p := s.cfg.Soulseek.Password
	s.mu.RUnlock()
	if p != "" {
		x = strings.ReplaceAll(x, p, "[redacted]")
	}
	return x
}

func (s *Service) Close() error {
	s.scanCancel()
	// Cancel the daemon lifetime before waiting for a config mutation, so a
	// blocked scan can always unblock shutdown.
	s.mu.RLock()
	runCancel := s.runCancel
	s.mu.RUnlock()
	if runCancel != nil {
		runCancel()
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.requeueDownloads = s.cancel != nil
	runCancel, cancel, client, mapping := s.runCancel, s.cancel, s.client, s.mapping
	s.runCtx, s.runCancel, s.ctx, s.cancel, s.client, s.mapping = nil, nil, nil, nil, nil, nil
	s.status, s.presence = StatusStopped, PresenceOffline
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	closePortMapping(mapping)
	if client != nil {
		_ = client.Close()
	}
	if runCancel != nil {
		runCancel()
	}
	s.sessionWG.Wait()
	s.downloadWG.Wait()
	s.wg.Wait()
	return nil
}

func (s *Service) Search(ctx context.Context, query, expression string) (SearchPage, error) {
	filter, err := parseSearchFilter(expression)
	if err != nil {
		return SearchPage{}, err
	}
	s.mu.RLock()
	c := s.client
	s.mu.RUnlock()
	if c == nil {
		return SearchPage{}, ErrNotStarted
	}
	r, err := c.Search(ctx, query)
	if err != nil {
		return SearchPage{}, err
	}
	out := fromSoulseekResults(r)
	sortSearchResults(out)
	search := Search{ID: fmt.Sprintf("%d", time.Now().UnixNano()), Query: query, Results: out}
	s.mu.Lock()
	s.searches[search.ID] = search
	s.mu.Unlock()
	return filteredSearchPage(search, filter, 0), nil
}

func sortSearchResults(results []SearchResult) {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Public != results[j].Public {
			return results[i].Public
		}
		if results[i].SlotFree != results[j].SlotFree {
			return results[i].SlotFree
		}
		if results[i].Queue != results[j].Queue {
			return results[i].Queue < results[j].Queue
		}
		if results[i].Speed != results[j].Speed {
			return results[i].Speed > results[j].Speed
		}
		return results[i].Size < results[j].Size
	})
}

func filteredSearchPage(search Search, filter searchFilter, cursor int) SearchPage {
	results := make([]SearchResult, 0, len(search.Results))
	for _, result := range search.Results {
		if filter.matches(result) {
			results = append(results, result)
		}
	}
	cursor = max(0, min(cursor, len(results)))
	end := min(cursor+searchPageSize, len(results))
	page := SearchPage{ID: search.ID, Query: search.Query, Results: append([]SearchResult(nil), results[cursor:end]...), Cursor: cursor, Total: len(results), FoundTotal: len(search.Results)}
	if end < len(results) {
		page.NextCursor = end
	}
	return page
}

func searchPage(search Search, cursor int, expression string) (SearchPage, error) {
	filter, err := parseSearchFilter(expression)
	if err != nil {
		return SearchPage{}, err
	}
	return filteredSearchPage(search, filter, cursor), nil
}

func (s *Service) SearchPage(id string, cursor int, expression string) (SearchPage, error) {
	filter, err := parseSearchFilter(expression)
	if err != nil {
		return SearchPage{}, err
	}
	s.mu.RLock()
	search, ok := s.searches[id]
	s.mu.RUnlock()
	if !ok {
		return SearchPage{}, ErrSearchNotFound
	}
	return filteredSearchPage(search, filter, cursor), nil
}

func (s *Service) Browse(ctx context.Context, username, path string) ([]soulseek.ShareEntry, error) {
	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()
	if client == nil {
		return nil, ErrNotStarted
	}
	return client.BrowseUser(ctx, username, path)
}
func (s *Service) BrowseLocal(path string) ([]soulseek.ShareEntry, error) {
	s.mu.RLock()
	idx := s.shares
	s.mu.RUnlock()
	return idx.Browse(path)
}

func folderDownloadItems(req FolderDownloadRequest, entries []soulseek.ShareEntry) ([]DownloadItem, error) {
	folder, err := soulseek.NormalizePath(req.Folder)
	if err != nil {
		return nil, err
	}
	folderLower := strings.ToLower(folder)
	prefix := folderLower + "/"
	seen := make(map[string]bool)
	var items []DownloadItem
	for _, entry := range entries {
		name, err := soulseek.NormalizePath(entry.Name)
		if err != nil {
			return nil, err
		}
		nameLower := strings.ToLower(name)
		if nameLower != folderLower && !strings.HasPrefix(nameLower, prefix) {
			return nil, fmt.Errorf("daemon: folder response entry outside %q", folder)
		}
		if entry.Directory || seen[name] || (!req.Recursive && !strings.EqualFold(path.Dir(name), folder)) {
			continue
		}
		seen[name] = true
		items = append(items, DownloadItem{Filename: name, Size: entry.Size})
	}
	if len(items) == 0 {
		return nil, errors.New("daemon: folder contains no downloadable files")
	}
	return items, nil
}

func withoutExistingFolderDownloads(items []DownloadItem, downloads []Download, username, downloadDir, defaultDownloadDir string) []DownloadItem {
	existing := make(map[string]bool)
	for _, download := range downloads {
		root := download.DownloadDir
		if root == "" {
			root = defaultDownloadDir
		}
		if strings.EqualFold(download.Username, username) && filepath.Clean(root) == filepath.Clean(downloadDir) {
			if name, err := soulseek.NormalizePath(download.Filename); err == nil {
				existing[name] = true
			}
		}
	}
	return slices.DeleteFunc(items, func(item DownloadItem) bool { return existing[item.Filename] })
}

func (s *Service) QueueFolder(ctx context.Context, req FolderDownloadRequest) ([]Download, error) {
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		return nil, errors.New("daemon: download username is required")
	}
	folder, err := soulseek.NormalizePath(req.Folder)
	if err != nil {
		return nil, err
	}
	req.Folder = folder
	folders := []string{folder}
	seen := map[string]bool{strings.ToLower(folder): true}
	if req.Recursive {
		prefix := strings.ToLower(folder) + "/"
		for _, subfolder := range req.Subfolders {
			subfolder, err = soulseek.NormalizePath(subfolder)
			if err != nil {
				return nil, err
			}
			key := strings.ToLower(subfolder)
			if key != strings.ToLower(folder) && !strings.HasPrefix(key, prefix) {
				return nil, fmt.Errorf("daemon: subfolder outside %q", folder)
			}
			if !seen[key] {
				seen[key] = true
				folders = append(folders, subfolder)
			}
		}
	}
	entries := make([]soulseek.ShareEntry, 0, len(req.Files))
	for _, file := range req.Files {
		entries = append(entries, soulseek.ShareEntry{Name: file.Filename, Size: file.Size})
	}
	type browseResult struct {
		entries []soulseek.ShareEntry
		err     error
	}
	jobs := make(chan string, len(folders))
	results := make(chan browseResult, len(folders))
	for _, folder := range folders {
		jobs <- folder
	}
	close(jobs)
	for range min(4, len(folders)) {
		go func() {
			for folder := range jobs {
				response, browseErr := s.Browse(ctx, req.Username, folder)
				results <- browseResult{response, browseErr}
			}
		}()
	}
	var browseErr error
	for range folders {
		result := <-results
		if result.err != nil {
			if browseErr == nil {
				browseErr = result.err
			}
			continue
		}
		entries = append(entries, result.entries...)
	}
	items, err := folderDownloadItems(req, entries)
	if err != nil {
		if browseErr != nil && len(req.Files) == 0 {
			return nil, browseErr
		}
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Filename < items[j].Filename })
	s.mu.RLock()
	downloadDir := strings.TrimSpace(req.DownloadDir)
	if downloadDir == "" {
		downloadDir = s.cfg.DownloadDir
	}
	items = withoutExistingFolderDownloads(items, s.journal.Downloads, req.Username, downloadDir, s.cfg.DownloadDir)
	s.mu.RUnlock()
	if len(items) == 0 {
		return nil, nil
	}
	return s.QueueDownloads([]DownloadRequest{{Username: req.Username, DownloadDir: req.DownloadDir, Files: items}})
}

func (s *Service) QueueDownloads(reqs []DownloadRequest) ([]Download, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	now := time.Now().UTC()
	var out []Download
	for _, req := range reqs {
		if strings.TrimSpace(req.Username) == "" {
			s.mu.Unlock()
			return nil, errors.New("daemon: download username is required")
		}
		downloadDir := strings.TrimSpace(req.DownloadDir)
		if downloadDir == "" {
			downloadDir = s.cfg.DownloadDir
		}
		for _, item := range req.Files {
			safeName, err := soulseek.NormalizePath(item.Filename)
			if err != nil {
				s.mu.Unlock()
				return nil, err
			}
			name := strings.ReplaceAll(safeName, "/", "\\")
			if item.Offset > item.Size {
				s.mu.Unlock()
				return nil, soulseek.ErrMalformed
			}
			dest := item.Destination
			if dest == "" {
				dest = safeSegment(req.Username) + "/" + safeName
			}
			if _, err = soulseek.SafeJoin(downloadDir, dest); err != nil {
				s.mu.Unlock()
				return nil, err
			}
			state := "queued"
			if item.Offset > 0 {
				state = "incomplete"
			}
			s.seq++
			d := Download{ID: fmt.Sprintf("d-%d", s.seq), Username: req.Username, Filename: name, Size: item.Size, Offset: item.Offset, DownloadDir: downloadDir, Destination: dest, State: state, CreatedAt: now, UpdatedAt: now}
			s.journal.Downloads = append(s.journal.Downloads, d)
			s.transfers[d.ID] = Transfer{ID: d.ID, Username: d.Username, Filename: d.Filename, Direction: "download", State: d.State, Done: d.Offset, Total: d.Size}
			out = append(out, d)
		}
	}
	if err := s.saveJournalLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Unlock()
	for _, download := range out {
		s.startDownload(download.ID)
	}
	return out, nil
}
func (s *Service) saveJournalLocked() error {
	s.journal.DownloadSequence = s.seq
	return config.SaveJSON(s.journalPath, s.journal)
}
func (s *Service) Downloads() []Download {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Download(nil), s.journal.Downloads...)
}
func (s *Service) Transfers() []Transfer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.transferValuesLocked(time.Now())
}
func (s *Service) TransferAction(id, action string) error {
	if strings.HasPrefix(id, "upload:") {
		result, err := s.UploadAction(UploadActionRequest{Action: strings.ToLower(action), IDs: []string{id}})
		if err != nil {
			return err
		}
		if len(result.Errors) > 0 {
			return errors.New(result.Errors[0].Error)
		}
		return nil
	}
	// Serialize stop/resume with each other and session shutdown. A stopped
	// worker must release its file before it can be resumed or cleared.
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	for i := range s.journal.Downloads {
		d := &s.journal.Downloads[i]
		if d.ID != id {
			continue
		}
		start := false
		switch strings.ToLower(action) {
		case "pause", "cancel":
			if d.State == "finalizing" {
				s.mu.Unlock()
				return errors.New("daemon: download is finishing")
			}
			if d.State == "completed" {
				s.mu.Unlock()
				if strings.EqualFold(action, "pause") {
					return nil
				}
				return errors.New("daemon: download already completed")
			}
			d.State = "paused"
			if strings.EqualFold(action, "cancel") {
				d.State = "cancelled"
			}
			d.Error, d.RetryAt = "", time.Time{}
			if cancel := s.downloadCancels[id]; cancel != nil {
				cancel()
			}
		case "retry", "resume":
			if s.downloadCancels[id] != nil {
				s.mu.Unlock()
				return nil
			}
			if d.State != "paused" && d.State != "cancelled" && d.State != "failed" && d.State != "retrying" {
				s.mu.Unlock()
				return nil
			}
			d.State, d.Error, d.RetryAt = "queued", "", time.Time{}
			start = true
		case "clear":
			if d.State == "running" || d.State == "finalizing" || d.State == "queued" || d.State == "incomplete" || s.downloadCancels[id] != nil {
				s.mu.Unlock()
				return errors.New("daemon: active download cannot be cleared")
			}
			if err := os.Remove(incompletePath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
				s.mu.Unlock()
				return err
			}
			s.journal.Downloads = append(s.journal.Downloads[:i], s.journal.Downloads[i+1:]...)
			delete(s.transfers, id)
			delete(s.transferTiming, id)
			err := s.saveJournalLocked()
			s.mu.Unlock()
			return err
		default:
			s.mu.Unlock()
			return fmt.Errorf("daemon: unsupported transfer action %q", action)
		}
		d.UpdatedAt = time.Now().UTC()
		if tr := s.transfers[id]; tr.ID != "" {
			tr.State, tr.Error, tr.Queue = d.State, d.Error, 0
			s.transfers[id] = tr
			s.stopTransferLocked(id)
		}
		done := s.downloadDone[id]
		err := s.saveJournalLocked()
		s.mu.Unlock()
		if done != nil {
			<-done
		}
		if start && err == nil {
			s.startDownload(id)
		}
		return err
	}
	defer s.mu.Unlock()
	return os.ErrNotExist
}
func (s *Service) Shares() []config.Share {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]config.Share(nil), s.cfg.Shares...)
}
func (s *Service) mutateShares(candidate []config.Share, next config.Config) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	s.stopShareWatcherLocked()
	generation := s.shareWatchGeneration
	runCtx := s.runCtx
	configPath := s.configPath
	s.mu.Unlock()
	if runCtx == nil {
		runCtx = context.Background()
	}
	err := s.runShareScan(runCtx, candidate, next.ShareExclusions, generation, false, nil, func(index *soulseek.ShareIndex) error {
		s.mu.Lock()
		if s.closed || s.scanCtx.Err() != nil || s.shareWatchGeneration != generation {
			s.mu.Unlock()
			return errShareScanDiscarded
		}
		s.mu.Unlock()
		if err := next.Save(configPath); err != nil {
			return err
		}
		s.mu.Lock()
		s.cfg, s.shares = next, index
		s.shareIndexRevision++
		client := s.client
		s.restartShareWatcherLocked()
		s.mu.Unlock()
		if client != nil {
			client.SetShareIndex(index)
		}
		s.persistShareIndex(index)
		return nil
	})
	s.mu.Lock()
	if err != nil && !s.closed && s.shareWatchGeneration == generation {
		s.restartShareWatcherLocked()
	}
	s.mu.Unlock()
	return err
}

func (s *Service) AddShare(sh config.Share) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.RLock()
	for _, x := range s.cfg.Shares {
		if x.Name == sh.Name {
			s.mu.RUnlock()
			return errors.New("daemon: duplicate share")
		}
	}
	candidate := append(append([]config.Share(nil), s.cfg.Shares...), sh)
	next := s.cfg
	s.mu.RUnlock()
	next.Shares = candidate
	if err := next.Validate(); err != nil {
		return err
	}
	return s.mutateShares(candidate, next)
}
func (s *Service) RemoveShare(name string) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.RLock()
	candidate := append([]config.Share(nil), s.cfg.Shares...)
	index := -1
	for i, x := range candidate {
		if x.Name == name {
			index = i
			break
		}
	}
	next := s.cfg
	s.mu.RUnlock()
	if index < 0 {
		return os.ErrNotExist
	}
	candidate = append(candidate[:index], candidate[index+1:]...)
	next.Shares = candidate
	return s.mutateShares(candidate, next)
}
func (s *Service) Rescan() error {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return ErrClosed
	}
	shares := append([]config.Share(nil), s.cfg.Shares...)
	rules := slices.Clone(s.cfg.ShareExclusions)
	generation := s.shareWatchGeneration
	runCtx := s.runCtx
	s.mu.RUnlock()
	if runCtx == nil {
		runCtx = context.Background()
	}
	return s.runShareScan(runCtx, shares, rules, generation, true, nil, func(index *soulseek.ShareIndex) error {
		return s.publishShareIndex(generation, index)
	})
}

func hotConfigUpdate(old, next config.Config) bool {
	oldSoulseek := old.Soulseek
	oldSoulseek.ConnectOnStartup = next.Soulseek.ConnectOnStartup
	return oldSoulseek == next.Soulseek &&
		slices.Equal(old.Shares, next.Shares) &&
		old.DownloadSlots == next.DownloadSlots &&
		old.UploadSlots == next.UploadSlots
}

func sameUploadConfig(a, b config.Uploads) bool {
	return a.ActiveProfile == b.ActiveProfile && a.LimitScope == b.LimitScope && a.Scheduling == b.Scheduling && slices.Equal(a.Profiles, b.Profiles)
}

func (s *Service) UpdateConfig(c config.Config) error {
	rules, err := config.NormalizeShareExclusions(c.ShareExclusions)
	if err != nil {
		return err
	}
	c.ShareExclusions = rules
	if err := validateConfig(c); err != nil {
		return err
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	reconnect := !hotConfigUpdate(s.cfg, c)
	if !reconnect && slices.Equal(s.cfg.ShareExclusions, c.ShareExclusions) {
		oldInterval := s.cfg.Search.WishlistIntervalMinutes
		uploadsChanged := !sameUploadConfig(s.cfg.Uploads, c.Uploads)
		client := s.client
		err := c.Save(s.configPath)
		if err == nil {
			s.cfg = c
		}
		s.mu.Unlock()
		if err == nil && uploadsChanged && client != nil {
			client.ConfigureUploads(uploadPolicy(c))
		}
		if err == nil && client != nil {
			client.ConfigureIncomingSearch(incomingSearchPolicy(c))
		}
		if err == nil && oldInterval != c.Search.WishlistIntervalMinutes {
			s.wakeWishlist()
		}
		return err
	}
	s.mu.Unlock()

	s.mu.Lock()
	s.stopShareWatcherLocked()
	generation := s.shareWatchGeneration
	runCtx := s.runCtx
	configPath := s.configPath
	active := s.cancel != nil
	s.mu.Unlock()
	if runCtx == nil {
		runCtx = context.Background()
	}
	err = s.runShareScan(runCtx, append([]config.Share(nil), c.Shares...), c.ShareExclusions, generation, false, nil, func(index *soulseek.ShareIndex) error {
		s.mu.Lock()
		if s.closed || s.scanCtx.Err() != nil || s.shareWatchGeneration != generation {
			s.mu.Unlock()
			return errShareScanDiscarded
		}
		s.mu.Unlock()
		if err := c.Save(configPath); err != nil {
			return err
		}
		if active && reconnect {
			s.stopSessionLocked(false)
		}
		s.mu.Lock()
		s.cfg, s.shares = c, index
		s.shareIndexRevision++
		if reconnect {
			s.downloadSlots = make(chan struct{}, c.DownloadSlots)
		}
		client := s.client
		s.restartShareWatcherLocked()
		s.mu.Unlock()
		if client != nil {
			client.SetShareIndex(index)
			client.ConfigureUploads(uploadPolicy(c))
			client.ConfigureIncomingSearch(incomingSearchPolicy(c))
		}
		s.persistShareIndex(index)
		return nil
	})
	s.mu.Lock()
	if err != nil && !s.closed && s.shareWatchGeneration == generation {
		s.restartShareWatcherLocked()
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.wakeWishlist()
	if active && reconnect {
		return s.startSessionLocked()
	}
	return nil
}

// WatchEOF cancels ctx when r reaches EOF or fails. It is useful for a daemon
// launched as a child with stdin inherited from its parent.
func WatchEOF(ctx context.Context, r io.Reader, cancel context.CancelFunc) {
	go func() {
		var b [1]byte
		for {
			if _, err := r.Read(b[:]); err != nil {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}()
}
func ContextWithEOF(parent context.Context, r io.Reader) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	WatchEOF(ctx, r, cancel)
	return ctx, cancel
}
