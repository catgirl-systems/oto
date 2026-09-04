package daemon

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

// Like Nicotine+, retry connection failures every three minutes and file I/O
// failures every fifteen. Unknown errors require an explicit Resume.
func downloadRetryDelay(err error) time.Duration {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, soulseek.ErrTransferCancelled) {
		return 0
	}
	var rejected *soulseek.DownloadRejectedError
	if errors.As(err, &rejected) {
		switch strings.TrimSuffix(strings.ToLower(strings.TrimSpace(rejected.Reason)), ".") {
		case "pending shutdown", "too many files", "too many megabytes":
			return 3 * time.Minute
		case "file read error":
			return 15 * time.Minute
		}
		return 0
	}
	var pathErr *os.PathError
	var linkErr *os.LinkError
	if errors.As(err, &pathErr) || errors.As(err, &linkErr) || errors.Is(err, io.ErrShortWrite) {
		return 15 * time.Minute
	}
	var netErr net.Error
	if errors.As(err, &netErr) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, soulseek.ErrNotConnected) ||
		errors.Is(err, soulseek.ErrUploadFailed) {
		return 3 * time.Minute
	}
	return 0
}

func (s *Service) retryDownloadsLoop(ctx context.Context) {
	defer s.sessionWG.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.retryDownloads(now)
		}
	}
}

func (s *Service) retryDownloads(now time.Time) {
	s.mu.RLock()
	var ids []string
	if s.client != nil {
		for _, download := range s.journal.Downloads {
			if download.State == "retrying" && !download.RetryAt.After(now) {
				ids = append(ids, download.ID)
			}
		}
	}
	s.mu.RUnlock()
	for _, id := range ids {
		s.startDownload(id)
	}
}
