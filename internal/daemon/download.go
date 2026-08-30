package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func safeSegment(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-_.", r) {
			return r
		}
		return '_'
	}, value)
	if value == "" || value == "." || value == ".." {
		return "peer"
	}
	return value
}

func (s *Service) startDownload(id string) {
	s.mu.Lock()
	if s.closed || s.restarting || s.ctx == nil {
		s.mu.Unlock()
		return
	}
	s.downloadWG.Add(1)
	s.mu.Unlock()
	go func() { defer s.downloadWG.Done(); s.runDownload(id) }()
}

func (s *Service) resumeDownloads() {
	s.mu.Lock()
	var ids []string
	for i := range s.journal.Downloads {
		if state := s.journal.Downloads[i].State; state == "queued" || state == "incomplete" || state == "running" {
			s.journal.Downloads[i].State = "queued"
			ids = append(ids, s.journal.Downloads[i].ID)
		}
	}
	_ = s.saveJournalLocked()
	s.mu.Unlock()
	for _, id := range ids {
		s.startDownload(id)
	}
}

func (s *Service) downloadPeerSlot(username string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	peerSlot := s.downloadPeers[username]
	if peerSlot == nil {
		// ponytail: serialize downloads per peer; multiplex P connections if parallel same-peer queues matter.
		peerSlot = make(chan struct{}, 1)
		s.downloadPeers[username] = peerSlot
	}
	return peerSlot
}

func (s *Service) runDownload(id string) {
	s.mu.Lock()
	if _, running := s.downloadCancels[id]; running || s.ctx == nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	slots := s.downloadSlots
	s.downloadCancels[id] = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.downloadCancels, id)
		s.mu.Unlock()
	}()

	download, ok := s.downloadByID(id)
	if !ok {
		return
	}
	peerSlot := s.downloadPeerSlot(download.Username)
	select {
	case peerSlot <- struct{}{}:
		defer func() { <-peerSlot }()
	case <-ctx.Done():
		return
	}
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return
	}
	s.mu.RLock()
	downloadRoot := s.cfg.DownloadDir
	s.mu.RUnlock()
	partDir := filepath.Join(config.DataDir(), "incomplete")
	if err := os.MkdirAll(partDir, 0700); err != nil {
		s.finishDownload(id, "failed", 0, err)
		return
	}
	partPath := filepath.Join(partDir, id+".part")
	file, err := os.OpenFile(partPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		s.finishDownload(id, "failed", 0, err)
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || stat.Size() < 0 || uint64(stat.Size()) > download.Size {
		if err == nil {
			err = errors.New("partial file exceeds expected size")
		}
		s.finishDownload(id, "failed", 0, err)
		return
	}
	offset := uint64(stat.Size())
	s.updateDownload(id, "running", offset, nil)

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		client, err := s.waitClient(ctx)
		if err != nil {
			lastErr = err
			break
		}
		lastErr = client.Download(ctx, download.Username, strings.ReplaceAll(download.Filename, "/", "\\"), download.Size, offset, file, func(progress soulseek.Progress) {
			s.updateTransferProgress(id, progress)
		})
		if lastErr == nil {
			break
		}
		if ctx.Err() != nil {
			break
		}
		if current, err := file.Stat(); err == nil && current.Size() >= 0 {
			offset = uint64(current.Size())
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			break
		case <-timer.C:
		}
	}
	if ctx.Err() != nil {
		s.mu.RLock()
		restarting := s.restarting
		s.mu.RUnlock()
		if restarting {
			s.updateDownload(id, "queued", offset, nil)
		} else {
			s.updateDownload(id, "cancelled", offset, ctx.Err())
		}
		return
	}
	if lastErr != nil {
		s.finishDownload(id, "failed", offset, lastErr)
		return
	}
	if err := file.Sync(); err != nil {
		s.finishDownload(id, "failed", offset, err)
		return
	}
	_ = file.Close()
	target, err := s.finalizePart(downloadRoot, partPath, download.Destination, id)
	if err != nil {
		s.finishDownload(id, "failed", download.Size, err)
		return
	}
	s.mu.Lock()
	for i := range s.journal.Downloads {
		if s.journal.Downloads[i].ID == id {
			rel, _ := filepath.Rel(downloadRoot, target)
			s.journal.Downloads[i].Destination = rel
			break
		}
	}
	s.mu.Unlock()
	s.finishDownload(id, "completed", download.Size, nil)
}

func (s *Service) waitClient(ctx context.Context) (*soulseek.Client, error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		s.mu.RLock()
		client := s.client
		s.mu.RUnlock()
		if client != nil {
			return client, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) downloadByID(id string) (Download, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, download := range s.journal.Downloads {
		if download.ID == id {
			return download, true
		}
	}
	return Download{}, false
}

func (s *Service) updateTransferProgress(id string, progress soulseek.Progress) {
	state := progress.State
	if state == "" {
		state = "running"
	}
	s.mu.Lock()
	if transfer := s.transfers[id]; transfer.ID != "" {
		transfer.Done, transfer.Total, transfer.State, transfer.Queue = progress.Done, progress.Total, state, progress.Queue
		s.transfers[id] = transfer
	}
	s.mu.Unlock()
}

func (s *Service) updateDownload(id, state string, offset uint64, failure error) {
	s.mu.Lock()
	for i := range s.journal.Downloads {
		if s.journal.Downloads[i].ID == id {
			s.journal.Downloads[i].State = state
			s.journal.Downloads[i].Offset = offset
			s.journal.Downloads[i].Error = errString(failure)
			s.journal.Downloads[i].UpdatedAt = time.Now().UTC()
			break
		}
	}
	if transfer := s.transfers[id]; transfer.ID != "" {
		transfer.State, transfer.Done, transfer.Error = state, offset, errString(failure)
		s.transfers[id] = transfer
	}
	_ = s.saveJournalLocked()
	s.mu.Unlock()
}

func (s *Service) finishDownload(id, state string, offset uint64, failure error) {
	s.updateDownload(id, state, offset, failure)
}

func (s *Service) finalizePart(downloadRoot, partPath, relative, id string) (string, error) {
	target, err := soulseek.SafeJoin(downloadRoot, relative)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", err
	}
	target = unusedName(target)
	if err := os.Rename(partPath, target); err == nil {
		return target, nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return "", err
	}
	temp := target + ".part-" + id
	source, err := os.Open(partPath)
	if err != nil {
		return "", err
	}
	defer source.Close()
	destination, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(destination, source)
	if syncErr := destination.Sync(); copyErr == nil {
		copyErr = syncErr
	}
	if closeErr := destination.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(temp)
		return "", copyErr
	}
	if err := os.Rename(temp, target); err != nil {
		_ = os.Remove(temp)
		return "", err
	}
	_ = os.Remove(partPath)
	return target, nil
}

func unusedName(path string) string {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for n := 1; ; n++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, n, ext)
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
}
