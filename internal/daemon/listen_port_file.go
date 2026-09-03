package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	DefaultListenPortReconcileInterval = 30 * time.Second
	listenPortFileDebounce             = 100 * time.Millisecond
)

func (s *Service) SetListenPortFile(path string, reconcileInterval time.Duration) error {
	if reconcileInterval < 0 {
		return errors.New("daemon: listen port reconcile interval cannot be negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runCancel != nil {
		return errors.New("daemon: listen port file must be configured before starting")
	}
	s.listenPortFile = filepath.Clean(path)
	if path == "" {
		s.listenPortFile = ""
	}
	s.listenPortInterval = reconcileInterval
	s.listenPort = 0
	return nil
}

func (s *Service) startListenPortWatcherLocked() {
	if s.listenPortFile == "" || s.runCtx == nil {
		return
	}
	ctx, path, interval := s.runCtx, s.listenPortFile, s.listenPortInterval
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		watchListenPortFile(ctx, path, interval, s.applyListenPort)
	}()
}

func (s *Service) applyListenPort(port uint16, available bool) {
	if !available {
		port = 0
	}
	s.mu.Lock()
	if s.closed || s.listenPortFile == "" || s.listenPort == port {
		s.mu.Unlock()
		return
	}
	s.listenPort = port
	client, active := s.client, s.cancel != nil
	if port == 0 && active {
		s.status, s.lastErr = StatusReconnecting, ErrListenPortUnavailable.Error()
	}
	s.mu.Unlock()
	if !active {
		return
	}

	if port == 0 {
		if client != nil {
			_ = client.Close()
		}
		s.wakeReconnect()
		return
	}
	if client != nil {
		if err := client.SetListenPort(port); err == nil {
			log.Printf("listening port updated to %d", port)
			return
		} else {
			log.Printf("listening port %d: %v", port, err)
			_ = client.Close()
		}
	}
	s.wakeReconnect()
}

func (s *Service) wakeReconnect() {
	select {
	case s.reconnectWake <- struct{}{}:
	default:
	}
}

func listenAddressWithPort(address string, port uint16) (string, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

func readListenPortFile(path string) (uint16, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return 0, false, nil
	}
	port, err := strconv.Atoi(text)
	if err != nil || port < 1024 || port > 65535 {
		return 0, false, fmt.Errorf("invalid listening port %q", text)
	}
	return uint16(port), true, nil
}

func watchListenPortFile(ctx context.Context, path string, interval time.Duration, apply func(uint16, bool)) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("listen port watcher: %v", err)
	}
	if watcher != nil {
		defer watcher.Close()
		if err := watcher.Add(filepath.Dir(path)); err != nil {
			log.Printf("listen port watcher %s: %v", path, err)
		}
	}

	var ticker *time.Ticker
	var tickerC <-chan time.Time
	if interval > 0 {
		ticker = time.NewTicker(interval)
		tickerC = ticker.C
		defer ticker.Stop()
	}
	var events <-chan fsnotify.Event
	var watcherErrors <-chan error
	if watcher != nil {
		events, watcherErrors = watcher.Events, watcher.Errors
	}
	var timer *time.Timer
	var timerC <-chan time.Time
	schedule := func() {
		if timer == nil {
			timer = time.NewTimer(listenPortFileDebounce)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(listenPortFileDebounce)
		}
		timerC = timer.C
	}
	reconcile := func() {
		port, available, err := readListenPortFile(path)
		if err != nil {
			log.Printf("listen port file %s: %v", path, err)
			return
		}
		apply(port, available)
	}

	// The directory watch is installed first so this read closes the startup gap.
	reconcile()
	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if filepath.Clean(event.Name) == filepath.Clean(path) && event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) != 0 {
				schedule()
			}
		case err, ok := <-watcherErrors:
			if !ok {
				watcherErrors = nil
				continue
			}
			log.Printf("listen port watcher: %v", err)
			schedule()
		case <-timerC:
			timerC = nil
			reconcile()
		case <-tickerC:
			reconcile()
		}
	}
}
