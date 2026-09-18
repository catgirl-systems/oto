package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func downloadService(t *testing.T) *Service {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, err := New(testConfig(t), filepath.Join(t.TempDir(), "state.sqlite3"))
	must(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func putPartial(t *testing.T, id, data string) string {
	t.Helper()
	part := incompletePath(id)
	must(t, os.MkdirAll(filepath.Dir(part), 0700))
	must(t, os.WriteFile(part, []byte(data), 0600))
	return part
}

func TestDownloadPauseResumeLifecycle(t *testing.T) {
	s := downloadService(t)
	downloads, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{
		{Filename: "Album/one", Size: 20}, {Filename: "Album/two", Size: 20},
	}}})
	must(t, err)
	id, waiting := downloads[0].ID, downloads[1].ID
	part := putPartial(t, id, "partial")
	// A session without a client exercises cancellation while waiting to connect.
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.startDownload(id)
	waitFor(t, func() bool { return s.Downloads()[0].State == "running" })
	s.startDownload(waiting)
	must(t, s.TransferAction(waiting, "pause"))
	for range 10 {
		must(t, s.TransferAction(id, "pause"))
		s.updateTransferProgress(id, soulseek.Progress{State: "running", Done: 19, Total: 20})
		if d := s.Downloads()[0]; d.State != "paused" || d.Offset != 7 || d.Error != "" {
			t.Fatalf("pause lost partial/state: %+v", d)
		}
		if tr := s.Transfers()[0]; tr.State != "paused" || tr.Done != 7 {
			t.Fatalf("late progress overwrote paused transfer: %+v", tr)
		}
		must(t, s.TransferAction(id, "resume"))
	}
	must(t, s.TransferAction(id, "pause"))
	s.resumeDownloads()
	s.mu.RLock()
	active := len(s.downloadCancels)
	s.mu.RUnlock()
	failIf(t, active != 0, "automatic resume started paused workers")
	must(t, s.Close())
	restored, err := New(s.cfg, s.journalPath)
	must(t, err)
	defer restored.Close()
	s = restored
	if d := restored.Downloads()[0]; d.State != "paused" || d.Offset != 7 {
		t.Fatalf("paused download did not survive restart: %+v", d)
	}
	if got, err := os.ReadFile(part); err != nil || string(got) != "partial" {
		t.Fatalf("partial changed: %q %v", got, err)
	}
	must(t, s.TransferAction(id, "cancel"))
	s.resumeDownloads()
	failIf(t, s.Downloads()[0].State != "cancelled", "automatic resume restarted a cancelled transfer")
}

func TestDownloadRetryClassification(t *testing.T) {
	var wire bytes.Buffer
	must(t, soulseek.WriteFrame(&wire, soulseek.PeerTransferRequest, []byte("abc")))
	_, _, closed := soulseek.ReadFrame(bytes.NewReader(nil))
	_, _, interrupted := soulseek.ReadFrame(bytes.NewReader(wire.Bytes()[:wire.Len()-1]))
	for _, tc := range []struct {
		err  error
		want time.Duration
	}{
		{nil, 0}, {context.Canceled, 0}, {soulseek.ErrTransferCancelled, 0},
		{soulseek.ErrMalformed, 0}, {errors.New("remote file size changed"), 0},
		{soulseek.ErrTruncated, 0}, {soulseek.ErrTooLarge, 0},
		{fmt.Errorf("payload: %w", soulseek.ErrTruncated), 0},
		{closed, 3 * time.Minute}, {interrupted, 3 * time.Minute},
		{&soulseek.DownloadRejectedError{Reason: "File not shared."}, 0},
		{&soulseek.DownloadRejectedError{Reason: "Banned"}, 0},
		{io.EOF, 3 * time.Minute}, {io.ErrUnexpectedEOF, 3 * time.Minute},
		{fmt.Errorf("%w: %w", soulseek.ErrMalformed, io.ErrUnexpectedEOF), 3 * time.Minute},
		{context.DeadlineExceeded, 3 * time.Minute}, {soulseek.ErrNotConnected, 3 * time.Minute},
		{soulseek.ErrUploadFailed, 3 * time.Minute},
		{&net.OpError{Op: "read", Err: syscall.ECONNRESET}, 3 * time.Minute},
		{&soulseek.DownloadRejectedError{Reason: "Pending shutdown."}, 3 * time.Minute},
		{&soulseek.DownloadRejectedError{Reason: "Too many files"}, 3 * time.Minute},
		{&soulseek.DownloadRejectedError{Reason: "File read error."}, 15 * time.Minute},
		{&os.PathError{Op: "write", Err: syscall.ENOSPC}, 15 * time.Minute},
		{&os.LinkError{Op: "rename", Err: syscall.EACCES}, 15 * time.Minute},
	} {
		if got := downloadRetryDelay(tc.err); got != tc.want {
			t.Errorf("retry %v: got %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestDownloadRetryPersistsAndCompletes(t *testing.T) {
	s := downloadService(t)
	downloads, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "Album/song", Size: 4}}}})
	must(t, err)
	id := downloads[0].ID
	putPartial(t, id, "data")
	s.finishDownload(id, "failed", 4, io.EOF)
	d := s.Downloads()[0]
	failIfFmt(t, d.State != "retrying" || d.RetryAt.Sub(d.UpdatedAt) != 3*time.Minute, "missing retry deadline: %+v", d)
	must(t, s.Close())
	restored, err := New(s.cfg, s.journalPath)
	must(t, err)
	defer restored.Close()
	s = restored
	if got := restored.Downloads()[0]; got.State != "retrying" || !got.RetryAt.Equal(d.RetryAt) {
		t.Fatalf("retry not persisted: %+v", got)
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.client = soulseek.NewClient(soulseek.ClientConfig{})
	s.retryDownloads(time.Now())
	failIf(t, s.Downloads()[0].State != "retrying", "retried before deadline")
	s.mu.Lock()
	s.journal.Downloads[0].RetryAt = time.Now().Add(-time.Second)
	s.mu.Unlock()
	s.retryDownloads(time.Now())
	waitFor(t, func() bool { return s.Downloads()[0].State == "completed" })
	must(t, s.TransferAction(id, "resume"))
	failIf(t, s.Downloads()[0].State != "completed", "resume restarted completed download")
}

func TestDownloadCompletionHooks(t *testing.T) {
	s := downloadService(t)
	logPath := filepath.Join(t.TempDir(), "hooks")
	t.Setenv("OTO_TEST_HOOK_LOG", logPath)
	s.cfg.Downloads.AfterFileCommand = `printf 'file:%s\n' "$1" >> "$OTO_TEST_HOOK_LOG"`
	s.cfg.Downloads.AfterFolderCommand = `printf 'folder:%s\n' "$1" >> "$OTO_TEST_HOOK_LOG"`
	downloads, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{
		{Filename: "Album/song's $mix.flac", Size: 4}, {Filename: "Album/two.flac", Size: 4},
	}}})
	must(t, err)
	first, second := downloads[0], downloads[1]
	must(t, s.TransferAction(second.ID, "pause"))
	pausedPart := putPartial(t, second.ID, "data")
	s.completeDownload(second.ID, second.DownloadDir, pausedPart)
	if _, err := os.Stat(pausedPart); err != nil {
		t.Fatalf("paused file was moved: %v", err)
	}
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("paused file invoked a completion command")
	}
	original := filepath.Join(first.DownloadDir, first.Destination)
	must(t, os.MkdirAll(filepath.Dir(original), 0700))
	must(t, os.WriteFile(original, []byte("old"), 0600))
	s.updateDownload(first.ID, "running", 4, nil)
	s.completeDownload(first.ID, first.DownloadDir, putPartial(t, first.ID, "data"))
	firstPath := filepath.Join(first.DownloadDir, s.Downloads()[0].Destination)
	failIf(t, firstPath == original, "completion overwrote an existing file")
	waitFor(t, func() bool { b, _ := os.ReadFile(logPath); return string(b) == "file:"+firstPath+"\n" })
	must(t, s.TransferAction(second.ID, "resume"))
	s.updateDownload(second.ID, "running", 4, nil)
	s.completeDownload(second.ID, second.DownloadDir, putPartial(t, second.ID, "data"))
	waitFor(t, func() bool { b, _ := os.ReadFile(logPath); return strings.Count(string(b), "\n") == 3 })
	b, _ := os.ReadFile(logPath)
	failIfFmt(t, strings.Count(string(b), "folder:"+filepath.Dir(firstPath)+"\n") != 1, "folder hook: %s", b)
	// Calling completion twice, or resuming a completed file, must not replay hooks.
	s.completeDownload(second.ID, second.DownloadDir, incompletePath(second.ID))
	must(t, s.TransferAction(second.ID, "resume"))
	if got, _ := os.ReadFile(logPath); string(got) != string(b) {
		t.Fatal("completion commands repeated")
	}
}

func TestFolderCompletionGrouping(t *testing.T) {
	s := downloadService(t)
	folder := filepath.Join(s.cfg.DownloadDir, "peer", "Album")
	for _, state := range []string{"queued", "running", "paused", "retrying", "failed", "cancelled"} {
		s.journal.Downloads = []Download{{Username: "peer", Destination: "peer/Album/song", State: state}}
		if s.folderCompleteLocked("peer", folder) {
			t.Errorf("%s file failed to block completion", state)
		}
	}
	s.journal.Downloads = []Download{
		{Username: "peer", Destination: "peer/Album/song", State: "completed"},
		{Username: "peer", Destination: "peer/Album/sub/song", State: "paused"},
		{Username: "other", Destination: "peer/Album/song", State: "paused"},
		{Username: "peer", DownloadDir: t.TempDir(), Destination: "peer/Album/song", State: "paused"},
	}
	failIf(t, !s.folderCompleteLocked("peer", folder), "another folder, user or root blocked completion")
}

func TestFailedFinalMoveCanResumeWithoutRedownloading(t *testing.T) {
	s := downloadService(t)
	logPath := filepath.Join(t.TempDir(), "hooks")
	t.Setenv("OTO_TEST_HOOK_LOG", logPath)
	s.cfg.Downloads.AfterFileCommand = `printf 'file\n' >> "$OTO_TEST_HOOK_LOG"`
	s.cfg.Downloads.AfterFolderCommand = `printf 'folder\n' >> "$OTO_TEST_HOOK_LOG"`
	downloads, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "Album/song", Size: 4}}}})
	must(t, err)
	d := downloads[0]
	blocker := filepath.Join(d.DownloadDir, "peer")
	must(t, os.WriteFile(blocker, []byte("not a directory"), 0600))
	part := putPartial(t, d.ID, "data")
	s.updateDownload(d.ID, "running", 4, nil)
	s.completeDownload(d.ID, d.DownloadDir, part)
	if got := s.Downloads()[0]; got.State != "retrying" || got.RetryAt.Sub(got.UpdatedAt) != 15*time.Minute {
		t.Fatalf("move failure not retryable: %+v", got)
	}
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed final move invoked a completion command")
	}
	must(t, os.Remove(blocker))
	s.ctx, s.cancel = context.WithCancel(context.Background())
	must(t, s.TransferAction(d.ID, "resume"))
	waitFor(t, func() bool { return s.Downloads()[0].State == "completed" })
	if b, err := os.ReadFile(filepath.Join(d.DownloadDir, d.Destination)); err != nil || string(b) != "data" {
		t.Fatalf("final file: %q %v", b, err)
	}
	waitFor(t, func() bool { b, _ := os.ReadFile(logPath); return strings.Count(string(b), "\n") == 2 })
}

func TestFinalizingDownloadControlsAndRestart(t *testing.T) {
	s := downloadService(t)
	downloads, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "Album/song", Size: 4}}}})
	must(t, err)
	id := downloads[0].ID
	putPartial(t, id, "data")
	s.updateDownload(id, "finalizing", 4, nil)
	for _, action := range []string{"pause", "cancel", "clear"} {
		if err := s.TransferAction(id, action); err == nil {
			t.Fatalf("%s interrupted a finalizing download", action)
		}
	}
	must(t, s.Close())
	restored, err := New(s.cfg, s.journalPath)
	must(t, err)
	defer restored.Close()
	restored.ctx, restored.cancel = context.WithCancel(context.Background())
	restored.resumeDownloads()
	waitFor(t, func() bool { return restored.Downloads()[0].State == "completed" })
}

func TestDefaultDirectorySkipsFolderHook(t *testing.T) {
	s := downloadService(t)
	logPath := filepath.Join(t.TempDir(), "hooks")
	t.Setenv("OTO_TEST_HOOK_LOG", logPath)
	s.cfg.Downloads.AfterFileCommand = `printf 'file\n' >> "$OTO_TEST_HOOK_LOG"`
	s.cfg.Downloads.AfterFolderCommand = `printf 'folder\n' >> "$OTO_TEST_HOOK_LOG"`
	downloads, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "song", Destination: "song", Size: 4}}}})
	must(t, err)
	d := downloads[0]
	s.updateDownload(d.ID, "running", 4, nil)
	s.completeDownload(d.ID, d.DownloadDir, putPartial(t, d.ID, "data"))
	waitFor(t, func() bool { b, _ := os.ReadFile(logPath); return string(b) == "file\n" })
}
