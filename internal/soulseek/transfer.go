package soulseek

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var ErrTransferCancelled = errors.New("soulseek: transfer cancelled")

var ErrNotConnected = errors.New("soulseek: not connected")
var ErrUploadFailed = errors.New("remote upload failed")

// DownloadRejectedError keeps a peer's rejection distinct from local/network errors.
type DownloadRejectedError struct{ Reason string }

func (e *DownloadRejectedError) Error() string { return e.Reason }

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
		n, err := src.Read(buf[:min(uint64(len(buf)), left)])
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
		if left == 0 {
			return nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return fmt.Errorf("%w: %w: expected %d bytes, got %d", ErrMalformed, err, expected, done-offset)
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

type paceState struct {
	version uint64
	next    time.Time
}

type UploadJob struct {
	User      string
	Request   TransferRequest
	Ready     chan struct{}
	active    bool
	cancelled bool
	released  bool
	pace      paceState
}

const (
	UploadScheduleFIFO          = "fifo"
	UploadScheduleRoundRobin    = "round_robin"
	UploadScheduleRandom        = "random"
	UploadScheduleSmallestFirst = "smallest_first"
)

type UploadPolicy struct {
	Scheduling     string
	BytesPerSecond int64
	PerTransfer    bool
}

// UploadManager schedules passive uploads with global slots and one slot per user.
type UploadManager struct {
	mu                    sync.Mutex
	max, active           int
	byUser                map[string]int
	q                     []*UploadJob
	policy                UploadPolicy
	sequence, paceVersion uint64
	served                map[string]uint64
	totalPace             paceState
	paceChanged           chan struct{}
	chooseRandom          func(int) int
}

func NewUploadManager(maxSlots int) *UploadManager {
	if maxSlots < 1 {
		maxSlots = 1
	}
	return &UploadManager{max: maxSlots, byUser: make(map[string]int), policy: UploadPolicy{Scheduling: UploadScheduleFIFO}, served: make(map[string]uint64), paceChanged: make(chan struct{}), chooseRandom: rand.IntN}
}

func (m *UploadManager) Configure(policy UploadPolicy) {
	switch policy.Scheduling {
	case UploadScheduleFIFO, UploadScheduleRoundRobin, UploadScheduleRandom, UploadScheduleSmallestFirst:
	default:
		policy.Scheduling = UploadScheduleFIFO
	}
	if policy.BytesPerSecond < 0 {
		policy.BytesPerSecond = 0
	}
	m.mu.Lock()
	m.policy = policy
	m.paceVersion++
	m.totalPace = paceState{}
	close(m.paceChanged)
	m.paceChanged = make(chan struct{})
	m.promote()
	m.mu.Unlock()
}

func (m *UploadManager) Policy() UploadPolicy {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.policy
}

func (m *UploadManager) Enqueue(user string, r TransferRequest) *UploadJob {
	j := &UploadJob{User: user, Request: r, Ready: make(chan struct{})}
	m.mu.Lock()
	m.q = append(m.q, j)
	m.promote()
	m.mu.Unlock()
	return j
}

func (m *UploadManager) nextIndex() int {
	eligible := func(job *UploadJob) bool { return !job.cancelled && m.byUser[job.User] == 0 }
	switch m.policy.Scheduling {
	case UploadScheduleSmallestFirst:
		best := -1
		for i, job := range m.q {
			if eligible(job) && (best < 0 || job.Request.Size < m.q[best].Request.Size) {
				best = i
			}
		}
		return best
	case UploadScheduleRoundRobin, UploadScheduleRandom:
		users := make([]string, 0, len(m.q))
		seen := make(map[string]bool, len(m.q))
		for _, job := range m.q {
			if eligible(job) && !seen[job.User] {
				seen[job.User] = true
				users = append(users, job.User)
			}
		}
		if len(users) == 0 {
			return -1
		}
		target := users[0]
		if m.policy.Scheduling == UploadScheduleRandom {
			target = users[m.chooseRandom(len(users))]
		} else {
			for _, user := range users[1:] {
				if m.served[user] < m.served[target] {
					target = user
				}
			}
		}
		for i, job := range m.q {
			if eligible(job) && job.User == target {
				return i
			}
		}
	default:
		for i, job := range m.q {
			if eligible(job) {
				return i
			}
		}
	}
	return -1
}

func (m *UploadManager) promote() {
	for len(m.q) > 0 && m.active < m.max {
		i := m.nextIndex()
		if i < 0 {
			return
		}
		job := m.q[i]
		m.q = append(m.q[:i], m.q[i+1:]...)
		m.active++
		m.byUser[job.User]++
		m.sequence++
		m.served[job.User] = m.sequence
		job.active = true
		close(job.Ready)
	}
}

// Done releases exactly this job, including a cancelled queue entry.
func (m *UploadManager) Done(job *UploadJob) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job.released {
		return
	}
	job.released = true
	if job.active {
		m.active--
		m.byUser[job.User]--
		job.active = false
	}
	for i, queued := range m.q {
		if queued == job {
			m.q = append(m.q[:i], m.q[i+1:]...)
			break
		}
	}
	m.promote()
}

// CancelJobs prevents promotion of the entire batch before any worker exits.
func (m *UploadManager) CancelJobs(jobs []*UploadJob) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range jobs {
		job.cancelled = true
	}
}

func (m *UploadManager) Wait(ctx context.Context, job *UploadJob) error {
	select {
	case <-job.Ready:
	case <-ctx.Done():
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil || job.cancelled {
		return ErrTransferCancelled
	}
	return nil
}

func (m *UploadManager) reserve(job *UploadJob, bytes int, now time.Time) (time.Duration, <-chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := m.paceChanged
	if bytes <= 0 || m.policy.BytesPerSecond == 0 {
		return 0, changed
	}
	state := &m.totalPace
	if m.policy.PerTransfer {
		state = &job.pace
	}
	if state.version != m.paceVersion {
		*state = paceState{version: m.paceVersion}
	}
	nanos := (int64(bytes)*int64(time.Second) + m.policy.BytesPerSecond - 1) / m.policy.BytesPerSecond
	step := time.Duration(nanos)
	if state.next.Before(now) {
		state.next = now.Add(step)
		return 0, changed
	}
	delay := state.next.Sub(now)
	state.next = state.next.Add(step)
	return delay, changed
}

func (m *UploadManager) chunkSize() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return transferChunkSize(m.policy.BytesPerSecond)
}

func transferChunkSize(bytesPerSecond int64) int {
	if bytesPerSecond == 0 {
		return 32 << 10
	}
	return int(min(max(bytesPerSecond/10, 1024), 32<<10))
}

type uploadWriter struct {
	ctx     context.Context
	manager *UploadManager
	job     *UploadJob
	dst     io.Writer
}

func (w uploadWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		select {
		case <-w.ctx.Done():
			return written, ErrTransferCancelled
		default:
		}
		n := min(len(p), w.manager.chunkSize())
		for {
			delay, changed := w.manager.reserve(w.job, n, time.Now())
			if delay <= 0 {
				break
			}
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
				break
			case <-changed:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				continue
			case <-w.ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return written, ErrTransferCancelled
			}
			break
		}
		nn, err := w.dst.Write(p[:n])
		written += nn
		p = p[nn:]
		if err != nil {
			return written, err
		}
		if nn != n {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func (m *UploadManager) LimitWriter(ctx context.Context, job *UploadJob, dst io.Writer) io.Writer {
	return uploadWriter{ctx: ctx, manager: m, job: job, dst: dst}
}
