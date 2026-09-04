package daemon

import (
	"context"
	"fmt"
	"html"
	"log"
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
				log.Printf("download notification: %v", err)
			}
		}
	}()
}
