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
		nmax := uint64(len(buf))
		if left < nmax {
			nmax = left
		}
		n, e := io.ReadFull(src, buf[:nmax])
		if n > 0 {
			wn, we := dst.WriteAt(buf[:n], int64(done))
			if we != nil {
				return we
			}
			if wn != n {
				return io.ErrShortWrite
			}
			done += uint64(n)
			left -= uint64(n)
			if progress != nil {
				progress(Progress{Done: done, Total: expected, State: "running"})
			}
		}
		if e != nil {
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
	left := expected - offset
	buf := make([]byte, 32<<10)
	done := offset
	for left > 0 {
		select {
		case <-ctx.Done():
			return ErrTransferCancelled
		default:
		}
		nmax := uint64(len(buf))
		if left < nmax {
			nmax = left
		}
		n, e := f.Read(buf[:nmax])
		if n > 0 {
			wn, we := dst.Write(buf[:n])
			if we != nil {
				return we
			}
			if wn != n {
				return io.ErrShortWrite
			}
			done += uint64(n)
			left -= uint64(n)
			if progress != nil {
				progress(Progress{Done: done, Total: expected, State: "running"})
			}
		}
		if e != nil {
			return e
		}
	}
	return nil
}

type UploadJob struct {
	User    string
	Request TransferRequest
	Ready   chan struct{}
	Err     error
}

// UploadManager schedules passive uploads FIFO with global and per-user slots.
type UploadManager struct {
	mu                   sync.Mutex
	max, perUser, active int
	byUser               map[string]int
	q                    []*UploadJob
	wake                 chan struct{}
}

func NewUploadManager(maxSlots, maxPerUser int) *UploadManager {
	if maxSlots < 1 {
		maxSlots = 1
	}
	if maxPerUser < 1 {
		maxPerUser = 1
	}
	return &UploadManager{max: maxSlots, perUser: maxPerUser, byUser: make(map[string]int), wake: make(chan struct{}, 1)}
}
func (m *UploadManager) Enqueue(user string, r TransferRequest) *UploadJob {
	j := &UploadJob{User: user, Request: r, Ready: make(chan struct{})}
	m.mu.Lock()
	m.q = append(m.q, j)
	m.promote()
	m.mu.Unlock()
	return j
}

// Cancel removes a queued upload. Active jobs are cancelled by their transfer context.
func (m *UploadManager) Cancel(j *UploadJob) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, q := range m.q {
		if q == j {
			m.q = append(m.q[:i], m.q[i+1:]...)
			j.Err = ErrTransferCancelled
			return true
		}
	}
	return false
}
func (m *UploadManager) promote() {
	for len(m.q) > 0 && m.active < m.max {
		j := m.q[0]
		if m.byUser[j.User] >= m.perUser {
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
func (m *UploadManager) QueueLen() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.q) }
func (m *UploadManager) Place(user string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, j := range m.q {
		if j.User == user {
			return i + 1
		}
	}
	return 0
}
func (m *UploadManager) Wait(ctx context.Context, j *UploadJob) error {
	select {
	case <-j.Ready:
		return nil
	case <-ctx.Done():
		return ErrTransferCancelled
	}
}
