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

// retryIdleCheck bounds how long the loop sleeps with nothing scheduled, as a
// guard against a missed wake.
const retryIdleCheck = time.Minute

// wakeRetries tells the retry loop a new retry was scheduled.
func (s *Service) wakeRetries() {
	select {
	case s.retryWake <- struct{}{}:
	default:
	}
}

// retryDownloadsLoop sleeps until the earliest scheduled retry instead of
// scanning the journal every second.
func (s *Service) retryDownloadsLoop(ctx context.Context) {
	defer s.sessionWG.Done()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.retryWake:
		case <-timer.C:
		}
		next := s.retryDownloads(time.Now())
		wait := retryIdleCheck
		if !next.IsZero() {
			wait = min(wait, max(0, time.Until(next)))
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
	}
}

// retryDownloads starts every due retry and returns the earliest retry still
// in the future, or the zero time when none is scheduled.
func (s *Service) retryDownloads(now time.Time) time.Time {
	s.mu.RLock()
	var ids []string
	var next time.Time
	if s.client != nil {
		for _, download := range s.journal.Downloads {
			if download.State != "retrying" {
				continue
			}
			if !download.RetryAt.After(now) {
				ids = append(ids, download.ID)
			} else if next.IsZero() || download.RetryAt.Before(next) {
				next = download.RetryAt
			}
		}
	}
	s.mu.RUnlock()
	for _, id := range ids {
		s.startDownload(id)
	}
	return next
}
