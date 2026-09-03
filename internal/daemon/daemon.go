package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

var (
	ErrClosed                = errors.New("daemon: closed")
	ErrNotStarted            = errors.New("daemon: not connected")
	ErrSearchNotFound        = errors.New("daemon: search not found")
	ErrListenPortUnavailable = errors.New("daemon: listening port unavailable")
)

type Status string

const (
	StatusStopped      Status = "stopped"
	StatusConnecting   Status = "connecting"
	StatusConnected    Status = "connected"
	StatusReconnecting Status = "reconnecting"
	StatusError        Status = "error"
)

const searchPageSize = 100

type Snapshot struct {
	Status    Status            `json:"status"`
	Error     string            `json:"error,omitempty"`
	Config    config.SafeConfig `json:"config"`
	Shares    []config.Share    `json:"shares"`
	Downloads []Download        `json:"downloads"`
	Transfers []Transfer        `json:"transfers"`
}

type SearchResult struct {
	Username   string `json:"username"`
	Path       string `json:"path"`
	Extension  string `json:"extension,omitempty"`
	Size       uint64 `json:"size"`
	Directory  bool   `json:"directory"`
	SlotFree   bool   `json:"slot_free"`
	Speed      uint32 `json:"speed"`
	Queue      uint32 `json:"queue"`
	Bitrate    uint32 `json:"bitrate,omitempty"`
	Duration   uint32 `json:"duration,omitempty"`
	VBR        bool   `json:"vbr,omitempty"`
	SampleRate uint32 `json:"sample_rate,omitempty"`
	BitDepth   uint32 `json:"bit_depth,omitempty"`
	Public     bool   `json:"public"`
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
	ID        string `json:"id"`
	Username  string `json:"username"`
	Filename  string `json:"filename"`
	Direction string `json:"direction"`
	State     string `json:"state"`
	Done      uint64 `json:"done"`
	Total     uint64 `json:"total"`
	Queue     uint32 `json:"queue,omitempty"`
	Error     string `json:"error,omitempty"`
}

type Download struct {
	ID          string    `json:"id"`
	Username    string    `json:"username"`
	Filename    string    `json:"filename"`
	Size        uint64    `json:"size"`
	Offset      uint64    `json:"offset"`
	Destination string    `json:"destination"`
	State       string    `json:"state"` // queued, incomplete, completed, cancelled, failed
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type Journal struct {
	Downloads []Download `json:"downloads"`
}

type DownloadItem struct {
	Filename    string `json:"filename"`
	Size        uint64 `json:"size"`
	Offset      uint64 `json:"offset,omitempty"`
	Destination string `json:"destination,omitempty"`
}
type DownloadRequest struct {
	Username string         `json:"username"`
	Files    []DownloadItem `json:"files"`
}

type FolderDownloadRequest struct {
	Username   string         `json:"username"`
	Folder     string         `json:"folder"`
	Subfolders []string       `json:"subfolders,omitempty"`
	Files      []DownloadItem `json:"files,omitempty"`
	Recursive  bool           `json:"recursive"`
}

type Service struct {
	mu                   sync.RWMutex
	cfg                  config.Config
	configPath           string
	parentCtx            context.Context
	shares               *soulseek.ShareIndex
	client               *soulseek.Client
	journal              Journal
	searches             map[string]Search
	transfers            map[string]Transfer
	downloadSlots        chan struct{}
	downloadCancels      map[string]context.CancelFunc
	downloadPeers        map[string]chan struct{}
	ctx                  context.Context
	cancel               context.CancelFunc
	shareWatchCancel     context.CancelFunc
	shareIndexBuilder    func(context.Context, []config.Share) (*soulseek.ShareIndex, error)
	shareRescanDelay     time.Duration
	shareWatchGeneration uint64
	listenPortFile       string
	listenPortInterval   time.Duration
	listenPort           uint16
	reconnectWake        chan struct{}
	closed               bool
	restarting           bool
	status               Status
	lastErr              string
	seq                  uint64
	journalPath          string
	wg                   sync.WaitGroup
	downloadWG           sync.WaitGroup
}

func New(cfg config.Config, path string) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	s := &Service{cfg: cfg, configPath: config.ConfigPath(), shares: soulseek.NewShareIndex(), searches: make(map[string]Search), transfers: make(map[string]Transfer), downloadSlots: make(chan struct{}, cfg.DownloadSlots), downloadCancels: make(map[string]context.CancelFunc), downloadPeers: make(map[string]chan struct{}), shareIndexBuilder: buildShareIndex, shareRescanDelay: DefaultShareRescanDelay, listenPortInterval: DefaultListenPortReconcileInterval, reconnectWake: make(chan struct{}, 1), status: StatusStopped, journalPath: path}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &s.journal); err != nil {
			return nil, fmt.Errorf("daemon: load journal: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
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
	return Snapshot{Status: s.status, Error: s.lastErr, Config: s.cfg.Redacted(), Shares: append([]config.Share(nil), s.cfg.Shares...), Downloads: append([]Download(nil), s.journal.Downloads...), Transfers: transferValues(s.transfers)}
}
func transferValues(m map[string]Transfer) []Transfer {
	out := make([]Transfer, 0, len(m))
	for _, x := range m {
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Start connects, logs in, and starts the reconnecting protocol loop.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.cancel != nil {
		s.mu.Unlock()
		return nil
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.parentCtx = ctx
	s.status, s.lastErr = StatusConnecting, ""
	if err := s.configureSharesLocked(); err != nil {
		s.lastErr = err.Error()
	}
	s.restartShareWatcherLocked()
	s.startListenPortWatcherLocked()
	s.mu.Unlock()
	s.resumeDownloads()
	if err := s.connectOnce(ctx); err != nil {
		message := s.safeError(err)
		if isPermanentLoginError(err) {
			s.mu.Lock()
			s.status, s.lastErr = StatusError, message
			s.mu.Unlock()
			return err
		}
		s.mu.Lock()
		s.status, s.lastErr = StatusReconnecting, message
		s.mu.Unlock()
		s.wg.Add(1)
		go s.reconnectLoop()
		return err
	}
	s.wg.Add(1)
	go s.reconnectLoop()
	return nil
}

func (s *Service) configureSharesLocked() error {
	idx, err := s.shareIndexBuilder(context.Background(), append([]config.Share(nil), s.cfg.Shares...))
	if err != nil {
		return err
	}
	s.shares = idx
	if s.client != nil {
		s.client.SetShareIndex(idx)
	}
	return nil
}
func (s *Service) connectOnce(ctx context.Context) error {
	s.mu.RLock()
	cfg, idx := s.cfg, s.shares
	portFile, port := s.listenPortFile, s.listenPort
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
	c := soulseek.NewClient(soulseek.ClientConfig{Address: cfg.Soulseek.Server, Username: cfg.Soulseek.Username, Password: cfg.Soulseek.Password, ListenAddr: cfg.Soulseek.ListenAddr, Share: idx, Uploads: soulseek.NewUploadManager(cfg.UploadSlots)})
	if err := c.Connect(ctx); err != nil {
		return err
	}
	if err := c.Login(ctx); err != nil {
		_ = c.Close()
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = c.Close()
		return ErrClosed
	}
	s.client = c
	idx = s.shares
	s.status = StatusConnected
	s.lastErr = ""
	s.mu.Unlock()
	c.SetShareIndex(idx)
	return nil
}
func (s *Service) reconnectLoop() {
	defer s.wg.Done()
	backoff := time.Second
	for {
		s.mu.RLock()
		ctx, cancel, client := s.ctx, s.cancel, s.client
		closed := s.closed
		s.mu.RUnlock()
		if closed || cancel == nil {
			return
		}
		if client != nil {
			eventCtx, stopEvents := context.WithCancel(ctx)
			eventsDone := make(chan struct{})
			go func() { s.consumeClientEvents(eventCtx, client); close(eventsDone) }()
			err := client.Run(ctx)
			stopEvents()
			<-eventsDone
			_ = client.Close()
			s.mu.Lock()
			if s.client == client {
				s.client = nil
			}
			s.mu.Unlock()
			if ctx.Err() != nil {
				return
			}
			message := s.safeError(err)
			if isPermanentLoginError(err) {
				s.mu.Lock()
				s.status = StatusError
				s.lastErr = message
				s.mu.Unlock()
				return
			}
			s.mu.Lock()
			s.status = StatusReconnecting
			s.lastErr = message
			s.mu.Unlock()
		}
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-s.reconnectWake:
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
		case <-t.C:
		}
		if backoff < time.Minute {
			backoff *= 2
			if backoff > time.Minute {
				backoff = time.Minute
			}
		}
		if err := s.connectOnce(ctx); err == nil {
			backoff = time.Second
			continue
		} else {
			message := s.safeError(err)
			if isPermanentLoginError(err) {
				s.mu.Lock()
				s.status, s.lastErr = StatusError, message
				s.mu.Unlock()
				return
			}
			s.mu.Lock()
			s.status, s.lastErr = StatusReconnecting, message
			s.mu.Unlock()
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
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cancel := s.cancel
	c := s.client
	s.cancel = nil
	s.client = nil
	s.status = StatusStopped
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if c != nil {
		_ = c.Close()
	}
	s.wg.Wait()
	s.downloadWG.Wait()
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
	out := make([]SearchResult, 0, len(r))
	for _, x := range r {
		out = append(out, SearchResult{Username: x.Username, Path: x.Path, Extension: x.Extension, Size: x.Size, Directory: x.IsDirectory, SlotFree: x.SlotFree, Speed: x.Speed, Queue: x.QueueLength, Bitrate: x.Bitrate, Duration: x.Duration, VBR: x.VBR, SampleRate: x.SampleRate, BitDepth: x.BitDepth, Public: x.Public})
	}
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

func withoutExistingFolderDownloads(items []DownloadItem, downloads []Download, username string) []DownloadItem {
	existing := make(map[string]bool)
	for _, download := range downloads {
		if strings.EqualFold(download.Username, username) {
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
	items = withoutExistingFolderDownloads(items, s.journal.Downloads, req.Username)
	s.mu.RUnlock()
	if len(items) == 0 {
		return nil, nil
	}
	return s.QueueDownloads([]DownloadRequest{{Username: req.Username, Files: items}})
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
			if _, err = soulseek.SafeJoin(s.cfg.DownloadDir, dest); err != nil {
				s.mu.Unlock()
				return nil, err
			}
			state := "queued"
			if item.Offset > 0 {
				state = "incomplete"
			}
			s.seq++
			d := Download{ID: fmt.Sprintf("d-%d", s.seq), Username: req.Username, Filename: name, Size: item.Size, Offset: item.Offset, Destination: dest, State: state, CreatedAt: now, UpdatedAt: now}
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
func (s *Service) saveJournalLocked() error { return config.SaveJSON(s.journalPath, s.journal) }
func (s *Service) Downloads() []Download {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Download(nil), s.journal.Downloads...)
}
func (s *Service) Transfers() []Transfer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return transferValues(s.transfers)
}
func (s *Service) TransferAction(id, action string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.journal.Downloads {
		d := &s.journal.Downloads[i]
		if d.ID != id {
			continue
		}
		switch strings.ToLower(action) {
		case "cancel":
			if d.State == "completed" {
				return errors.New("daemon: download already completed")
			}
			d.State = "cancelled"
			if cancel := s.downloadCancels[d.ID]; cancel != nil {
				cancel()
			}
			d.UpdatedAt = time.Now().UTC()
			if tr := s.transfers[d.ID]; tr.ID != "" {
				tr.State, tr.Error = d.State, d.Error
				s.transfers[d.ID] = tr
			}
			return s.saveJournalLocked()
		case "retry", "resume":
			if d.State == "cancelled" || d.State == "failed" {
				d.State = "queued"
				d.Error = ""
				d.UpdatedAt = time.Now().UTC()
				if tr := s.transfers[d.ID]; tr.ID != "" {
					tr.State, tr.Error = d.State, d.Error
					s.transfers[d.ID] = tr
				}
				err := s.saveJournalLocked()
				if err == nil {
					go s.startDownload(d.ID)
				}
				return err
			}
			return nil
		case "clear":
			if d.State == "running" || d.State == "queued" || d.State == "incomplete" {
				return errors.New("daemon: active download cannot be cleared")
			}
			if err := os.Remove(incompletePath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			s.journal.Downloads = append(s.journal.Downloads[:i], s.journal.Downloads[i+1:]...)
			delete(s.transfers, id)
			return s.saveJournalLocked()
		default:
			return fmt.Errorf("daemon: unsupported transfer action %q", action)
		}
	}
	if transfer, ok := s.transfers[id]; ok && transfer.Direction == "upload" {
		if strings.ToLower(action) == "clear" && (transfer.State == "completed" || transfer.State == "failed") {
			delete(s.transfers, id)
			return nil
		}
		return errors.New("daemon: upload action unavailable")
	}
	return os.ErrNotExist
}
func (s *Service) Shares() []config.Share {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]config.Share(nil), s.cfg.Shares...)
}
func (s *Service) AddShare(sh config.Share) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.cfg.Shares {
		if x.Name == sh.Name {
			return errors.New("daemon: duplicate share")
		}
	}
	candidate := append(append([]config.Share(nil), s.cfg.Shares...), sh)
	check := s.cfg
	check.Shares = candidate
	if err := check.Validate(); err != nil {
		return err
	}
	s.cfg.Shares = candidate
	if err := s.configureSharesLocked(); err != nil {
		s.cfg.Shares = s.cfg.Shares[:len(s.cfg.Shares)-1]
		_ = s.configureSharesLocked()
		s.restartShareWatcherLocked()
		return err
	}
	s.restartShareWatcherLocked()
	return s.cfg.Save(s.configPath)
}
func (s *Service) RemoveShare(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, x := range s.cfg.Shares {
		if x.Name != name {
			continue
		}
		previous := append([]config.Share(nil), s.cfg.Shares...)
		s.cfg.Shares = append(append([]config.Share(nil), s.cfg.Shares[:i]...), s.cfg.Shares[i+1:]...)
		if err := s.configureSharesLocked(); err != nil {
			s.cfg.Shares = previous
			_ = s.configureSharesLocked()
			s.restartShareWatcherLocked()
			return err
		}
		s.restartShareWatcherLocked()
		return s.cfg.Save(s.configPath)
	}
	return os.ErrNotExist
}
func (s *Service) Rescan() error {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return ErrClosed
	}
	shares := append([]config.Share(nil), s.cfg.Shares...)
	generation, builder := s.shareWatchGeneration, s.shareIndexBuilder
	s.mu.RUnlock()
	idx, err := builder(context.Background(), shares)
	if err != nil {
		return err
	}
	if !s.publishWatchedShareIndex(generation, idx) {
		return errors.New("daemon: share roots changed during rescan")
	}
	return nil
}

func hotConfigUpdate(old, next config.Config) bool {
	return old.Soulseek == next.Soulseek &&
		slices.Equal(old.Shares, next.Shares) &&
		old.DownloadSlots == next.DownloadSlots &&
		old.UploadSlots == next.UploadSlots
}

func (s *Service) UpdateConfig(c config.Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if hotConfigUpdate(s.cfg, c) {
		err := c.Save(s.configPath)
		if err == nil {
			s.cfg = c
		}
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return ErrClosed
	}
	builder, configPath := s.shareIndexBuilder, s.configPath
	s.mu.RUnlock()
	idx, err := builder(context.Background(), append([]config.Share(nil), c.Shares...))
	if err != nil {
		return err
	}
	if err := c.Save(configPath); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	s.cfg, s.shares = c, idx
	s.stopShareWatcherLocked()
	old, stop, parent := s.client, s.cancel, s.parentCtx
	s.client, s.cancel, s.ctx = nil, nil, nil
	s.restarting = true
	s.status = StatusStopped
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	if old != nil {
		_ = old.Close()
	}
	s.wg.Wait()
	s.downloadWG.Wait()
	s.mu.Lock()
	s.downloadSlots = make(chan struct{}, c.DownloadSlots)
	s.restarting = false
	s.mu.Unlock()
	if parent == nil {
		parent = context.Background()
	}
	startErr := s.Start(parent)
	if startErr != nil && isPermanentLoginError(startErr) {
		return startErr
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
