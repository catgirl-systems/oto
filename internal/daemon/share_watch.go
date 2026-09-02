package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/fsnotify/fsnotify"
)

const (
	DefaultShareRescanDelay = 5 * time.Minute
	shareRescanMaxDelay     = 30 * time.Minute
)

type shareScanResult struct {
	index    *soulseek.ShareIndex
	revision uint64
	err      error
}

func buildShareIndex(ctx context.Context, shares []config.Share) (*soulseek.ShareIndex, error) {
	index := soulseek.NewShareIndex()
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
	if s.ctx != nil {
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
	if s.shareRescanDelay == 0 || s.ctx == nil || s.closed || len(s.cfg.Shares) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.shareWatchCancel = cancel
	generation := s.shareWatchGeneration
	shares := append([]config.Share(nil), s.cfg.Shares...)
	roots := s.shares.Roots()
	delay := s.shareRescanDelay
	builder := s.shareIndexBuilder
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.watchShares(ctx, generation, shares, roots, delay, shareRescanMaxDelay, builder)
	}()
}

func (s *Service) publishWatchedShareIndex(generation uint64, index *soulseek.ShareIndex) bool {
	s.mu.Lock()
	if s.closed || generation != s.shareWatchGeneration {
		s.mu.Unlock()
		return false
	}
	s.shares = index
	client := s.client
	s.mu.Unlock()
	if client != nil {
		client.SetShareIndex(index)
	}
	return true
}

func (s *Service) watchShares(ctx context.Context, generation uint64, shares []config.Share, roots []soulseek.ShareRoot, quiet, maximum time.Duration, builder func(context.Context, []config.Share) (*soulseek.ShareIndex, error)) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("share watcher: %v", err)
		s.pollShares(ctx, generation, shares, quiet, builder)
		return
	}

	watched := make(map[string]bool)
	watching := len(roots) == len(shares)
	for _, root := range roots {
		if err := addWatchTree(ctx, watcher, watched, root.Path, root.Path); err != nil {
			log.Printf("share watcher %s: %v", root.Path, err)
			watching = false
		}
	}
	if !watching {
		_ = watcher.Close()
		s.pollShares(ctx, generation, shares, quiet, builder)
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
	startScan := func() {
		if scanning || !dirty {
			return
		}
		scanning, dirty = true, false
		quietReady, maxReady = false, false
		stopTimer(&quietTimer, &quietC)
		stopTimer(&maxTimer, &maxC)
		scanRevision := revision
		go func() {
			index, err := builder(ctx, shares)
			scanDone <- shareScanResult{index: index, revision: scanRevision, err: err}
		}()
	}
	markDirty := func() {
		revision++
		dirty, quietReady = true, false
		resetQuiet()
		startMaximum()
	}

	// Reconcile once after installing watches to close the startup scan/watch gap.
	startScan()
	for {
		select {
		case <-ctx.Done():
			stopTimer(&quietTimer, &quietC)
			stopTimer(&maxTimer, &maxC)
			if scanning {
				<-scanDone
			}
			return
		case event, ok := <-events:
			if !ok {
				events = nil
				watching = false
				markDirty()
				continue
			}
			if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			if event.Op&fsnotify.Create != 0 {
				if info, statErr := os.Lstat(event.Name); statErr == nil && info.IsDir() && !strings.HasPrefix(info.Name(), ".") {
					if addErr := addWatchTree(ctx, watcher, watched, event.Name, event.Name); addErr != nil {
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
			quietTimer, quietC, quietReady = nil, nil, true
			if !scanning {
				startScan()
			}
		case <-maxC:
			maxTimer, maxC, maxReady = nil, nil, true
			if !scanning {
				startScan()
			}
		case result := <-scanDone:
			scanning = false
			if result.err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("share rescan: %v", result.err)
				dirty = true
				if quietTimer == nil && !quietReady {
					resetQuiet()
				}
				startMaximum()
			} else if dirty || result.revision != revision {
				dirty = true
			} else {
				for _, root := range result.index.Roots() {
					if addErr := addWatchTree(ctx, watcher, watched, root.Path, root.Path); addErr != nil {
						log.Printf("share watcher %s: %v", root.Path, addErr)
						watching = false
					}
				}
				if !s.publishWatchedShareIndex(generation, result.index) {
					return
				}
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

func (s *Service) pollShares(ctx context.Context, generation uint64, shares []config.Share, delay time.Duration, builder func(context.Context, []config.Share) (*soulseek.ShareIndex, error)) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		index, err := builder(ctx, shares)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("share rescan: %v", err)
		} else if !s.publishWatchedShareIndex(generation, index) {
			return
		}
		timer.Reset(delay)
	}
}

func addWatchTree(ctx context.Context, watcher *fsnotify.Watcher, watched map[string]bool, root, path string) error {
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
