package daemon

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"os/exec"
	"path/filepath"
	"time"
)

// DownloadNotification is a session-local signal, not a durable event queue.
type DownloadNotification struct {
	SessionID string `json:"session_id"`
	Sequence  uint64 `json:"sequence"`
	Message   string `json:"message"`
}

func notifyDesktop(ctx context.Context, title, message string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "notify-send", "--", title, html.EscapeString(message)).Run()
}

// Called with mu held only after the completed journal has been saved.
func (s *Service) notifyDownloadLocked(user, target string, folderFinished bool) {
	var messages []struct{ title, body string }
	if s.cfg.Downloads.FileNotifications {
		messages = append(messages, struct{ title, body string }{"File downloaded", fmt.Sprintf("%q downloaded from %q", filepath.Base(target), user)})
	}
	if folderFinished && s.cfg.Downloads.FolderNotifications {
		messages = append(messages, struct{ title, body string }{"Folder downloaded", fmt.Sprintf("%q downloaded from %q", filepath.Dir(target), user)})
	}
	if len(messages) == 0 {
		return
	}
	s.downloadNotification.Sequence++
	s.downloadNotification.Message = messages[len(messages)-1].body
	ctx := s.runCtx
	if ctx == nil {
		ctx = s.scanCtx
	}
	notify := s.desktopNotify
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for _, message := range messages {
			if err := notify(ctx, message.title, message.body); err != nil {
				s.event(slog.LevelWarn, "download_notification_failed", err)
			}
		}
	}()
}

// Presence hydration never calls this: only a fresh offline -> connected change.
func (s *Service) notifyBuddyOnlineLocked(username string) {
	if !s.community.buddies[username].NotifyOnline {
		return
	}
	s.community.buddyNotification.SessionID = fmt.Sprintf("%s/%s/%d", s.community.identity.Daemon, s.community.identity.Account, s.community.identity.Session)
	s.community.buddyNotification.Sequence++
	s.community.buddyNotification.Message = fmt.Sprintf("Buddy %q is online", username)
	if s.buddyNotifyActive {
		return
	}
	s.buddyNotifyActive = true
	ctx, notify := s.scanCtx, s.desktopNotify
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		var sent DownloadNotification
		for {
			s.mu.Lock()
			next := s.community.buddyNotification
			if s.closed || ctx.Err() != nil || next.Sequence == 0 || next.SessionID == sent.SessionID && next.Sequence == sent.Sequence {
				s.buddyNotifyActive = false
				s.mu.Unlock()
				return
			}
			s.mu.Unlock()
			if err := notify(ctx, "Buddy online", next.Message); err != nil {
				s.event(slog.LevelWarn, "buddy_notification_failed", err)
			}
			sent = next
			// ponytail: coalesce desktop alerts to 1/s; the summary sequence retains
			// the transition count without a second notification event store.
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	}()
}
