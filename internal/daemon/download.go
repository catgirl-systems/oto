package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
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

func incompletePath(id string) string {
	return filepath.Join(config.DataDir(), "incomplete", id+".part")
}

func (s *Service) startDownload(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.requeueDownloads || s.ctx == nil || s.ctx.Err() != nil || s.downloadCancels[id] != nil {
		return
	}
	for _, download := range s.journal.Downloads {
		if download.ID != id || (download.State != "queued" && download.State != "incomplete" &&
			!(download.State == "retrying" && !download.RetryAt.After(time.Now()))) {
			continue
		}
		s.prepareTransferLocked(id)
		ctx, cancel := context.WithCancel(s.ctx)
		done := make(chan struct{})
		s.downloadCancels[id], s.downloadDone[id] = cancel, done
		slots := s.downloadSlots
		s.downloadWG.Add(1)
		go func() {
			defer s.downloadWG.Done()
			defer func() {
				cancel()
				s.mu.Lock()
				delete(s.downloadCancels, id)
				delete(s.downloadDone, id)
				close(done)
				s.mu.Unlock()
			}()
			s.runDownload(ctx, download, slots)
		}()
		return
	}
}

func (s *Service) resumeDownloads() {
	s.mu.Lock()
	var ids []string
	for i := range s.journal.Downloads {
		if state := s.journal.Downloads[i].State; state == "queued" || state == "incomplete" || state == "running" || state == "finalizing" {
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

func (s *Service) runDownload(ctx context.Context, download Download, slots chan struct{}) {
	id := download.ID
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
	if ctx.Err() != nil {
		return
	}
	downloadRoot := download.DownloadDir
	if downloadRoot == "" {
		s.mu.RLock()
		downloadRoot = s.cfg.DownloadDir
		s.mu.RUnlock()
	}
	partPath := incompletePath(id)
	if err := os.MkdirAll(filepath.Dir(partPath), 0700); err != nil {
		s.finishDownload(id, "failed", download.Offset, err)
		return
	}
	file, err := os.OpenFile(partPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		s.finishDownload(id, "failed", download.Offset, err)
		return
	}
	offset := download.Offset
	defer func() {
		if ctx.Err() != nil {
			if stat, err := file.Stat(); err == nil && stat.Size() >= 0 {
				offset = uint64(stat.Size())
			}
			// Explicit pause/cancel takes precedence over shutdown requeueing.
			s.updateDownload(id, "queued", offset, nil)
		}
		_ = file.Close()
	}()
	stat, err := file.Stat()
	if err != nil || stat.Size() < 0 || uint64(stat.Size()) > download.Size {
		if err == nil {
			err = errors.New("partial file exceeds expected size")
		}
		s.finishDownload(id, "failed", offset, err)
		return
	}
	offset = uint64(stat.Size())
	s.updateDownload(id, "running", offset, nil)

	// A previous attempt may have downloaded everything but failed to move it.
	if offset < download.Size {
		client, err := s.waitClient(ctx)
		if err == nil {
			err = client.DownloadWithStart(ctx, download.Username, strings.ReplaceAll(download.Filename, "/", "\\"), download.Size, offset, file, func(progress soulseek.Progress) {
				s.updateTransferProgress(id, progress)
			}, func() { s.startTransfer(id, offset) })
			s.mu.Lock()
			s.stopTransferLocked(id)
			s.mu.Unlock()
		}
		if current, statErr := file.Stat(); statErr == nil && current.Size() >= 0 {
			offset = uint64(current.Size())
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			s.finishDownload(id, "failed", offset, err)
			return
		}
	}
	if err := file.Sync(); err != nil {
		s.finishDownload(id, "failed", offset, err)
		return
	}
	if err := file.Close(); err != nil {
		s.finishDownload(id, "failed", offset, err)
		return
	}
	if ctx.Err() == nil {
		s.completeDownload(id, downloadRoot, partPath)
	}
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
	defer s.mu.Unlock()
	if transfer := s.transfers[id]; transfer.ID != "" && (transfer.State == "running" || transfer.State == "queued") {
		if state == "queued" && s.transferTiming[id].streamBegan {
			return
		}
		transfer.Done, transfer.Total, transfer.State, transfer.Queue = progress.Done, progress.Total, state, progress.Queue
		s.transfers[id] = transfer
		s.progressTransferLocked(id, progress.Done)
	}
}

func (s *Service) updateDownload(id, state string, offset uint64, failure error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.journal.Downloads {
		d := &s.journal.Downloads[i]
		if d.ID != id || d.State == "completed" {
			continue
		}
		if d.State == "paused" || d.State == "cancelled" {
			state, failure = d.State, nil
		}
		d.State, d.Offset, d.Error, d.UpdatedAt = state, offset, errString(failure), time.Now().UTC()
		d.RetryAt = time.Time{}
		if state == "failed" {
			if delay := downloadRetryDelay(failure); delay > 0 {
				d.State, d.RetryAt = "retrying", d.UpdatedAt.Add(delay)
			}
		}
		if transfer := s.transfers[id]; transfer.ID != "" {
			transfer.State, transfer.Done, transfer.Error, transfer.Queue = d.State, d.Offset, d.Error, 0
			s.transfers[id] = transfer
			if state != "running" {
				s.stopTransferLocked(id)
			}
		}
		if err := s.saveJournalLocked(); err != nil {
			log.Printf("save download state: %v", err)
		}
		return
	}
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
