package daemon

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func (s *Service) completeDownload(id, root, partPath string) {
	if absolute, err := filepath.Abs(root); err == nil {
		root = absolute
	} else {
		if download, ok := s.downloadByID(id); ok {
			s.finishDownload(id, "failed", download.Offset, err)
		}
		return
	}
	// All bytes have arrived. Commit to finishing before releasing the lock;
	// potentially large cross-device copies must not block the whole daemon.
	s.mu.Lock()
	if s.closed || s.requeueDownloads {
		s.mu.Unlock()
		return
	}
	var download Download
	for i := range s.journal.Downloads {
		d := &s.journal.Downloads[i]
		if d.ID == id && d.State == "running" {
			d.State, d.Offset = "finalizing", d.Size
			s.stopTransferLocked(id)
			download = *d
			tr := s.transfers[id]
			tr.State, tr.Done = d.State, d.Size
			s.transfers[id] = tr
			break
		}
	}
	s.mu.Unlock()
	if download.ID == "" {
		return
	}
	target, err := s.finalizePart(root, partPath, download.Destination, id)
	if err != nil {
		s.finishDownload(id, "failed", download.Size, err)
		return
	}
	// Finalization cannot be paused/cleared midway. Even during shutdown, save
	// a successful move before the worker exits so it isn't downloaded twice.
	s.mu.Lock()
	for i := range s.journal.Downloads {
		d := &s.journal.Downloads[i]
		if d.ID != id {
			continue
		}
		d.Destination, _ = filepath.Rel(root, target)
		d.DownloadDir = root
		d.State, d.Offset, d.Error = "completed", d.Size, ""
		d.RetryAt, d.UpdatedAt = time.Time{}, time.Now().UTC()
		tr := s.transfers[id]
		tr.State, tr.Done, tr.Error, tr.Queue = d.State, d.Size, "", 0
		s.transfers[id] = tr
		s.stopTransferLocked(id)
		break
	}
	commands, ctx := s.cfg.Downloads, s.runCtx
	folder := filepath.Dir(target)
	folderFinished := folder != root && s.folderCompleteLocked(download.Username, folder)
	closed := s.closed
	var completed Download
	for _, item := range s.journal.Downloads {
		if item.ID == id {
			completed = item
			break
		}
	}
	s.statsStateLocked(id, "completed")
	err = s.persistDownloadLocked(completed)
	if err != nil {
		// Never move a completed file back: EXDEV and partial moves make that
		// rollback unsafe. Keep terminal state and retry the durable write.
		if s.completionRetries == nil {
			s.completionRetries = make(map[string]completionRetry)
		}
		s.completionRetries[id] = completionRetry{id: id, username: download.Username, target: target, folder: folder, folderFinished: folderFinished, commands: commands, closed: closed, ctx: ctx}
		s.mu.Unlock()
		log.Printf("save completed download (commands and notifications skipped): %v", err)
		return
	}
	s.mu.Unlock()
	s.runCompletionEffects(completionRetry{id: id, username: download.Username, target: target, folder: folder, folderFinished: folderFinished, commands: commands, closed: closed, ctx: ctx})
}

type completionRetry struct {
	id, username, target, folder string
	folderFinished               bool
	commands                     config.Downloads
	closed                       bool
	ctx                          context.Context
}

func (s *Service) runCompletionEffects(r completionRetry) {
	s.mu.Lock()
	r.closed = r.closed || s.closed
	if !r.closed {
		s.notifyDownloadLocked(r.username, r.target, r.folderFinished)
	}
	s.mu.Unlock()
	if r.commands.AutoClearCompleted {
		if err := s.clearCompletedDownload(r.id); err != nil {
			log.Printf("clear completed download %s (history retained): %v", r.id, err)
		}
	}
	if r.closed {
		return
	}
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := startDownloadCommand(ctx, r.commands.AfterFileCommand, r.target); err != nil {
		log.Printf("start file download command: %v", err)
	}
	if r.folderFinished {
		if err := startDownloadCommand(ctx, r.commands.AfterFolderCommand, r.folder); err != nil {
			log.Printf("start folder download command: %v", err)
		}
	}
}

func (s *Service) folderCompleteLocked(username, folder string) bool {
	// Completion covers the currently known files in this exact destination,
	// not a recursive job. Paused, cancelled and failed files still block it.
	for _, d := range s.journal.Downloads {
		if !strings.EqualFold(d.Username, username) || d.State == "completed" || d.State == "filtered" {
			continue
		}
		root := d.DownloadDir
		if root == "" {
			root = s.cfg.DownloadDir
		}
		target, err := soulseek.SafeJoin(root, d.Destination)
		if err != nil || filepath.Dir(target) == folder {
			return false
		}
	}
	return true
}

func startDownloadCommand(ctx context.Context, command, path string) error {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	// The command is trusted local configuration; the downloaded path is data,
	// passed as $1 rather than inserted into shell source.
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command, "oto-download-hook", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("download command failed: %v", err)
		}
	}()
	return nil
}
