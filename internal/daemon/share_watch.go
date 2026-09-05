package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/fsnotify/fsnotify"
)

const (
	DefaultShareRescanDelay = 5 * time.Minute
	shareRescanMaxDelay     = 30 * time.Minute
	shareIndexCacheVersion  = 2
)

type shareScanResult struct {
	index    *soulseek.ShareIndex
	revision uint64
	err      error
	reply    chan error
}

var errShareScanDiscarded = errors.New("daemon: share scan discarded")

func (s *Service) acquireShareScan(ctx context.Context, manual bool) error {
	if manual {
		select {
		case s.shareScanGate <- struct{}{}:
			return nil
		default:
			return ErrScanBusy
		}
	}
	select {
	case s.shareScanGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) finishShareScan(id uint64, state string, started time.Time, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shareScan == nil || s.shareScan.ID != id {
		return
	}
	s.shareScan.State = state
	s.shareScan.FinishedAt = time.Now().UTC()
	s.shareScan.ElapsedMS = time.Since(started).Milliseconds()
	s.activeScanCancel = nil
	if err != nil && !errors.Is(err, ErrScanCancelled) {
		s.shareScan.Error = err.Error()
	}
}

// runShareScan owns the single scan gate through building and publication.
func (s *Service) runShareScan(ctx context.Context, shares []config.Share, rules []string, generation uint64, manual bool, builder func(context.Context, []config.Share) (*soulseek.ShareIndex, error), publish func(*soulseek.ShareIndex) error) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	s.wg.Add(1)
	cancelEpoch := s.shareCancelEpoch
	if builder == nil {
		builder = s.shareIndexBuilder
	}
	if builder == nil {
		builder = func(ctx context.Context, roots []config.Share) (*soulseek.ShareIndex, error) {
			return buildFilteredShareIndex(ctx, roots, rules)
		}
	}
	s.mu.Unlock()
	defer s.wg.Done()
	ctx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(s.scanCtx, func() { cancel(context.Canceled) })
	defer stop()
	defer cancel(context.Canceled)
	if err := s.acquireShareScan(ctx, manual); err != nil {
		return err
	}
	defer func() { <-s.shareScanGate }()
	if err := ctx.Err(); err != nil {
		return err
	}
	started := time.Now()
	s.mu.Lock()
	if cancelEpoch != s.shareCancelEpoch {
		s.mu.Unlock()
		return ErrScanCancelled
	}
	if s.closed || s.scanCtx.Err() != nil || generation != s.shareWatchGeneration {
		s.mu.Unlock()
		return errShareScanDiscarded
	}
	s.shareScanID++
	id := s.shareScanID
	s.shareScan = &ShareScan{ID: id, State: "scanning", StartedAt: started}
	s.activeScanCancel = cancel
	s.mu.Unlock()

	var progressMu sync.Mutex
	var files, directories uint64
	var last time.Time
	var lastRoot string
	progress := func(root string, directory bool) {
		progressMu.Lock()
		defer progressMu.Unlock()
		if directory {
			directories++
		} else {
			files++
		}
		now := time.Now()
		if root == lastRoot && !last.IsZero() && now.Sub(last) < 200*time.Millisecond {
			return
		}
		last, lastRoot = now, root
		s.mu.Lock()
		if s.shareScan != nil && s.shareScan.ID == id && s.shareScan.State == "scanning" && ctx.Err() == nil {
			s.shareScan.Root, s.shareScan.Files, s.shareScan.Directories = root, files, directories
		}
		s.mu.Unlock()
	}
	index, err := builder(soulseek.WithShareScanProgress(ctx, progress), shares)
	if err == nil && index == nil {
		err = errors.New("daemon: share scan returned nil index")
	}
	progressMu.Lock()
	s.mu.Lock()
	// Cancellation and the publication commitment share the same mutex.
	if ctx.Err() != nil {
		err = context.Cause(ctx)
	}
	if s.shareScan != nil && s.shareScan.ID == id {
		s.shareScan.Files, s.shareScan.Directories = files, directories
		if err == nil {
			s.shareScan.State = "publishing"
		}
	}
	s.mu.Unlock()
	progressMu.Unlock()
	if err == nil {
		err = publish(index)
	}
	state := "completed"
	if err != nil {
		switch {
		case errors.Is(err, errShareScanDiscarded):
			state = "discarded"
		case ctx.Err() != nil || errors.Is(err, ErrScanCancelled):
			state = "cancelled"
		default:
			state = "failed"
		}
	}
	s.finishShareScan(id, state, started, err)
	return err
}

type shareIndexCache struct {
	Version    int                  `json:"version"`
	Roots      []soulseek.ShareRoot `json:"roots"`
	Files      []soulseek.ShareFile `json:"files"`
	Exclusions []string             `json:"exclusions"`
}

func loadShareIndexCache(path string, shares []config.Share, rules []string) (*soulseek.ShareIndex, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cache shareIndexCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, err
	}
	if cache.Version != shareIndexCacheVersion {
		return nil, fmt.Errorf("unsupported share index cache version %d", cache.Version)
	}
	rules, err = config.NormalizeShareExclusions(rules)
	if err != nil {
		return nil, err
	}
	if cache.Exclusions == nil || !slices.Equal(cache.Exclusions, rules) {
		return nil, errors.New("share index cache exclusions changed")
	}
	roots := make([]soulseek.ShareRoot, len(shares))
	for i, share := range shares {
		roots[i] = soulseek.ShareRoot{Name: share.Name, Path: share.Path}
	}
	index, err := soulseek.RestoreShareIndexWithExclusions(roots, cache.Files, rules)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(cache.Roots, index.Roots()) {
		return nil, errors.New("share index cache roots changed")
	}
	return index, nil
}

func saveShareIndexCache(path string, index *soulseek.ShareIndex) error {
	return config.SaveJSON(path, shareIndexCache{Version: shareIndexCacheVersion, Roots: index.Roots(), Files: index.Files(), Exclusions: index.Exclusions()})
}

func (s *Service) persistShareIndex(index *soulseek.ShareIndex) {
	if err := saveShareIndexCache(s.shareIndexPath, index); err != nil {
		log.Printf("save share index cache: %v", err)
	}
}

func buildShareIndex(ctx context.Context, shares []config.Share) (*soulseek.ShareIndex, error) {
	return buildFilteredShareIndex(ctx, shares, nil)
}

func buildFilteredShareIndex(ctx context.Context, shares []config.Share, rules []string) (*soulseek.ShareIndex, error) {
	index, err := soulseek.NewShareIndexWithExclusions(rules)
	if err != nil {
		return nil, err
	}
	for _, share := range shares {
		if err := index.AddRoot(share.Name, share.Path); err != nil {
			return nil, err
		}
	}
	if err := index.ScanContext(ctx); err != nil {
		return nil, err
	}
	return index, nil
}

func (s *Service) SetShareRescanDelay(delay time.Duration) error {
	if delay < 0 {
		return errors.New("daemon: share rescan delay cannot be negative")
	}
	s.mu.Lock()
	s.shareRescanDelay = delay
	if s.runCtx != nil {
		s.restartShareWatcherLocked()
	}
	s.mu.Unlock()
	return nil
}

func (s *Service) stopShareWatcherLocked() {
	if s.shareWatchCancel != nil {
		s.shareWatchCancel()
		s.shareWatchCancel = nil
	}
	s.shareWatchGeneration++
}

func (s *Service) restartShareWatcherLocked() {
	s.stopShareWatcherLocked()
	if s.shareRescanDelay == 0 || s.runCtx == nil || s.closed || len(s.cfg.Shares) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(s.runCtx)
	s.shareWatchCancel = cancel
	generation := s.shareWatchGeneration
	shares := append([]config.Share(nil), s.cfg.Shares...)
	roots := s.shares.Roots()
	policy := s.shares
	delay := s.shareRescanDelay
	builder := s.shareIndexBuilder
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.watchShares(ctx, generation, shares, roots, policy, delay, shareRescanMaxDelay, builder)
	}()
}

func (s *Service) publishShareIndex(generation uint64, index *soulseek.ShareIndex) error {
	s.mu.Lock()
	if s.closed || s.scanCtx.Err() != nil || generation != s.shareWatchGeneration {
		s.mu.Unlock()
		return errShareScanDiscarded
	}
	s.shares = index
	s.shareIndexRevision++
	client := s.client
	s.mu.Unlock()
	if client != nil {
		client.SetShareIndex(index)
	}
	s.persistShareIndex(index)
	return nil
}

func (s *Service) watchShares(ctx context.Context, generation uint64, shares []config.Share, roots []soulseek.ShareRoot, policy *soulseek.ShareIndex, quiet, maximum time.Duration, builder func(context.Context, []config.Share) (*soulseek.ShareIndex, error)) {
	rules := policy.Exclusions()
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("share watcher: %v", err)
		s.pollShares(ctx, generation, shares, policy.Exclusions(), quiet, builder)
		return
	}

	watched := make(map[string]bool)
	watching := len(roots) == len(shares)
	for _, root := range roots {
		if err := addWatchTree(ctx, watcher, watched, root.Path, root.Path, policy); err != nil {
			log.Printf("share watcher %s: %v", root.Path, err)
			watching = false
		}
	}
	if !watching {
		_ = watcher.Close()
		s.pollShares(ctx, generation, shares, policy.Exclusions(), quiet, builder)
		return
	}
	defer watcher.Close()
	events, watcherErrors := watcher.Events, watcher.Errors

	var quietTimer, maxTimer *time.Timer
	var quietC, maxC <-chan time.Time
	stopTimer := func(timer **time.Timer, channel *<-chan time.Time) {
		if *timer != nil {
			if !(*timer).Stop() {
				select {
				case <-(*timer).C:
				default:
				}
			}
		}
		*timer, *channel = nil, nil
	}
	resetQuiet := func() {
		stopTimer(&quietTimer, &quietC)
		quietTimer = time.NewTimer(quiet)
		quietC = quietTimer.C
	}
	startMaximum := func() {
		if maxTimer == nil {
			maxTimer = time.NewTimer(maximum)
			maxC = maxTimer.C
		}
	}

	revision := uint64(0)
	dirty, scanning := true, false
	quietReady, maxReady := false, false
	scanDone := make(chan shareScanResult, 1)
	scanPublish := make(chan shareScanResult)
	startScan := func() {
		if scanning || !dirty {
			return
		}
		scanning, dirty = true, false
		quietReady, maxReady = false, false
		stopTimer(&quietTimer, &quietC)
		stopTimer(&maxTimer, &maxC)
		scanRevision := revision
		workWake := s.scanCancellationWake()
		guardedBuilder := func(ctx context.Context, roots []config.Share) (*soulseek.ShareIndex, error) {
			select {
			case <-workWake:
				return nil, ErrScanCancelled
			default:
			}
			if builder != nil {
				return builder(ctx, roots)
			}
			return buildFilteredShareIndex(ctx, roots, rules)
		}
		go func() {
			err := s.runShareScan(ctx, shares, rules, generation, false, guardedBuilder, func(index *soulseek.ShareIndex) error {
				reply := make(chan error, 1)
				select {
				case scanPublish <- shareScanResult{index: index, revision: scanRevision, reply: reply}:
				case <-ctx.Done():
					return ctx.Err()
				}
				select {
				case err := <-reply:
					return err
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			scanDone <- shareScanResult{revision: scanRevision, err: err}
		}()
	}
	markDirty := func() {
		revision++
		dirty, quietReady = true, false
		resetQuiet()
		startMaximum()
	}

	cancelWake := s.scanCancellationWake()
	consumeCancellation := func() {
		select {
		case <-cancelWake:
			cancelWake = s.scanCancellationWake()
			revision++
			dirty, quietReady, maxReady = false, false, false
			stopTimer(&quietTimer, &quietC)
			stopTimer(&maxTimer, &maxC)
		default:
		}
	}
	// Reconcile the startup gap unless the user just cancelled that work.
	if !s.userCancelledScan() {
		startScan()
	} else {
		dirty = false
	}
	for {
		select {
		case <-ctx.Done():
			stopTimer(&quietTimer, &quietC)
			stopTimer(&maxTimer, &maxC)
			if scanning {
				<-scanDone
			}
			return
		case <-cancelWake:
			consumeCancellation()
		case event, ok := <-events:
			consumeCancellation()
			if !ok {
				events = nil
				watching = false
				markDirty()
				continue
			}
			if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			info, statErr := os.Lstat(event.Name)
			if statErr == nil {
				if info.Mode()&os.ModeSymlink != 0 || policy.ExcludedLocalPath(event.Name, info.IsDir()) {
					continue
				}
			} else if policy.ExcludedLocalPath(event.Name, false) && policy.ExcludedLocalPath(event.Name, true) {
				continue // Removed entries of ambiguous type conservatively trigger a scan.
			}
			if event.Op&fsnotify.Create != 0 {
				if info, statErr := os.Lstat(event.Name); statErr == nil && info.IsDir() && !strings.HasPrefix(info.Name(), ".") {
					if addErr := addWatchTree(ctx, watcher, watched, event.Name, event.Name, policy); addErr != nil {
						log.Printf("share watcher %s: %v", event.Name, addErr)
						watching = false
					}
				}
			}
			if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				removeWatchTree(watcher, watched, event.Name)
			}
			markDirty()
		case watchErr, ok := <-watcherErrors:
			if !ok {
				watcherErrors = nil
				continue
			}
			log.Printf("share watcher: %v", watchErr)
			markDirty()
		case <-quietC:
			consumeCancellation()
			quietTimer, quietC, quietReady = nil, nil, true
			if !scanning {
				startScan()
			}
		case <-maxC:
			consumeCancellation()
			maxTimer, maxC, maxReady = nil, nil, true
			if !scanning {
				startScan()
			}
		case result := <-scanPublish:
			if ctx.Err() != nil || dirty || result.revision != revision {
				result.reply <- errShareScanDiscarded
				continue
			}
			for _, root := range result.index.Roots() {
				if addErr := addWatchTree(ctx, watcher, watched, root.Path, root.Path, policy); addErr != nil {
					log.Printf("share watcher %s: %v", root.Path, addErr)
					watching = false
				}
			}
			result.reply <- s.publishShareIndex(generation, result.index)
		case result := <-scanDone:
			consumeCancellation()
			scanning = false
			if errors.Is(result.err, ErrScanCancelled) {
				// Only events arriving after cancellation may schedule another scan.
				if !watching {
					dirty = true
					resetQuiet() // Lost watches resume polling at the normal interval.
				}
			} else if result.err != nil {
				if ctx.Err() != nil {
					return
				}
				if !errors.Is(result.err, errShareScanDiscarded) {
					log.Printf("share rescan: %v", result.err)
				}
				dirty = true
				if quietTimer == nil && !quietReady {
					resetQuiet()
				}
				startMaximum()
			} else if result.revision != revision {
				dirty = true
			} else {
				if !watching {
					dirty = true
					resetQuiet()
					startMaximum()
				}
			}
			if dirty && (quietReady || maxReady) {
				startScan()
			}
		}
	}
}

func (s *Service) pollShares(ctx context.Context, generation uint64, shares []config.Share, rules []string, delay time.Duration, builder func(context.Context, []config.Share) (*soulseek.ShareIndex, error)) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		err := s.runShareScan(ctx, shares, rules, generation, false, builder, func(index *soulseek.ShareIndex) error {
			return s.publishShareIndex(generation, index)
		})
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, errShareScanDiscarded) {
				return
			}
			if !errors.Is(err, ErrScanCancelled) {
				log.Printf("share rescan: %v", err)
			}
		}
		timer.Reset(delay)
	}
}

func addWatchTree(ctx context.Context, watcher *fsnotify.Watcher, watched map[string]bool, root, path string, policy *soulseek.ShareIndex) error {
	return filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if current != root && strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if policy.ExcludedLocalPath(current, true) {
			return filepath.SkipDir
		}
		current = filepath.Clean(current)
		if watched[current] {
			return nil
		}
		if err := watcher.Add(current); err != nil {
			return fmt.Errorf("watch %s: %w", current, err)
		}
		watched[current] = true
		return nil
	})
}

func removeWatchTree(watcher *fsnotify.Watcher, watched map[string]bool, root string) {
	root = filepath.Clean(root)
	prefix := root + string(filepath.Separator)
	for path := range watched {
		if path == root || strings.HasPrefix(path, prefix) {
			_ = watcher.Remove(path)
			delete(watched, path)
		}
	}
}
