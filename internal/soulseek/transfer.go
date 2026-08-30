package soulseek

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var ErrTransferCancelled = errors.New("soulseek: transfer cancelled")

// NormalizePath accepts only a relative, slash-separated shared path.
func NormalizePath(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	if name == "" || strings.ContainsRune(name, 0) || strings.HasPrefix(name, "/") || (len(name) >= 2 && name[1] == ':') {
		return "", ErrOutsideShare
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", ErrOutsideShare
		}
	}
	p := filepath.ToSlash(filepath.Clean(name))
	if p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return "", ErrOutsideShare
	}
	return p, nil
}
func SafeJoin(root, name string) (string, error) {
	p, e := NormalizePath(name)
	if e != nil {
		return "", e
	}
	base, e := filepath.Abs(root)
	if e != nil {
		return "", e
	}
	out := filepath.Join(base, filepath.FromSlash(p))
	// Reject symlink components, including a symlink target outside the root.
	cur := base
	for _, part := range strings.Split(p, "/") {
		cur = filepath.Join(cur, part)
		if st, err := os.Lstat(cur); err == nil && st.Mode()&os.ModeSymlink != 0 {
			return "", ErrOutsideShare
		}
	}
	rel, e := filepath.Rel(base, out)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrOutsideShare
	}
	return out, nil
}

// Progress reports bytes copied and the expected total.
type Progress struct {
	Done, Total uint64
	State       string
	Queue       uint32
}
type ProgressFunc func(Progress)

// CopyAtMost copies exactly expected bytes from src to dst, starting at offset.
func CopyAtMost(ctx context.Context, dst io.WriterAt, src io.Reader, expected, offset uint64, progress ProgressFunc) error {
	return copyAtMost(ctx, io.NewOffsetWriter(dst, int64(offset)), src, expected, offset, progress)
}

func copyAtMost(ctx context.Context, dst io.Writer, src io.Reader, expected, offset uint64, progress ProgressFunc) error {
	if offset > expected {
		return ErrMalformed
	}
	left := expected - offset
	buf := make([]byte, 32<<10)
	done := offset
	for left > 0 {
		select {
		case <-ctx.Done():
			return ErrTransferCancelled
		default:
		}
		n, err := io.ReadFull(src, buf[:min(uint64(len(buf)), left)])
		if n > 0 {
			written, writeErr := dst.Write(buf[:n])
			if writeErr != nil {
				return writeErr
			}
			if written != n {
				return io.ErrShortWrite
			}
			done += uint64(n)
			left -= uint64(n)
			if progress != nil {
				progress(Progress{Done: done, Total: expected, State: "running"})
			}
		}
		if err != nil {
			return fmt.Errorf("%w: expected %d bytes, got %d", ErrMalformed, expected, done-offset)
		}
	}
	return nil
}

// ReceiveFile safely creates or resumes a file below root and enforces the wire size.
func ReceiveFile(ctx context.Context, root, name string, src io.Reader, expected, offset uint64, progress ProgressFunc) (string, error) {
	p, e := SafeJoin(root, name)
	if e != nil {
		return "", e
	}
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return "", err
	}
	f, e := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0600)
	if e != nil {
		return "", e
	}
	defer f.Close()
	if err := CopyAtMost(ctx, f, src, expected, offset, progress); err != nil {
		return "", err
	}
	if err := f.Truncate(int64(expected)); err != nil {
		return "", err
	}
	return p, nil
}

// SendFile streams a bounded file region and rejects short files.
func SendFile(ctx context.Context, root, name string, dst io.Writer, expected, offset uint64, progress ProgressFunc) error {
	p, e := SafeJoin(root, name)
	if e != nil {
		return e
	}
	st, e := os.Stat(p)
	if e != nil {
		return e
	}
	if st.IsDir() || uint64(st.Size()) != expected || offset > expected {
		return ErrMalformed
	}
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	if _, e = f.Seek(int64(offset), io.SeekStart); e != nil {
		return e
	}
	return copyAtMost(ctx, dst, f, expected, offset, progress)
}

type UploadJob struct {
	User    string
	Request TransferRequest
	Ready   chan struct{}
}

// UploadManager schedules passive uploads FIFO with global slots and one slot per user.
type UploadManager struct {
	mu          sync.Mutex
	max, active int
	byUser      map[string]int
	q           []*UploadJob
}

func NewUploadManager(maxSlots int) *UploadManager {
	if maxSlots < 1 {
		maxSlots = 1
	}
	return &UploadManager{max: maxSlots, byUser: make(map[string]int)}
}
func (m *UploadManager) Enqueue(user string, r TransferRequest) *UploadJob {
	j := &UploadJob{User: user, Request: r, Ready: make(chan struct{})}
	m.mu.Lock()
	m.q = append(m.q, j)
	m.promote()
	m.mu.Unlock()
	return j
}

func (m *UploadManager) promote() {
	for len(m.q) > 0 && m.active < m.max {
		j := m.q[0]
		if m.byUser[j.User] > 0 {
			break
		}
		m.q = m.q[1:]
		m.active++
		m.byUser[j.User]++
		close(j.Ready)
	}
}
func (m *UploadManager) Done(user string) {
	m.mu.Lock()
	if m.active > 0 {
		m.active--
	}
	if m.byUser[user] > 0 {
		m.byUser[user]--
	}
	m.promote()
	m.mu.Unlock()
}
func (m *UploadManager) Wait(ctx context.Context, j *UploadJob) error {
	select {
	case <-j.Ready:
		return nil
	case <-ctx.Done():
		return ErrTransferCancelled
	}
}
