// Package diagnostics provides bounded, privacy-conscious daemon diagnostics.
package diagnostics

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	maxRecordBytes = 8192
	queueSize      = 256
	retryDelay     = 30 * time.Second
	shutdownWait   = 2 * time.Second
	maxSafeString  = 64
)

var (
	rotationBytes = uint64(10 << 20)
	maxArchives   = 3
	nowUTC        = func() time.Time { return time.Now().UTC() }
)

var processRunID = rand.Text()

// StartupError reports a daemon failure even before state ownership is acquired.
// It never opens a managed file or includes arbitrary error text.
func StartupError(w io.Writer, err error) {
	attrs := []slog.Attr{slog.String("run_id", processRunID), slog.String("component", "daemon")}
	logger := slog.New(newJSONHandler(w, new(slog.LevelVar), attrs))
	Event(logger, slog.LevelError, "daemon_failed", err)
}

type Status struct {
	Level          string `json:"level"`
	Directory      string `json:"directory"`
	StoredBytes    uint64 `json:"stored_bytes"`
	FileCount      int    `json:"file_count"`
	RotationBytes  uint64 `json:"rotation_bytes"`
	MaxArchives    int    `json:"max_archives"`
	DroppedRecords uint64 `json:"dropped_records"`
	Warning        string `json:"warning"`
}

type Manager struct {
	directory string
	stderr    io.Writer
	level     *slog.LevelVar
	runID     string
	logger    *slog.Logger
	queue     chan []byte
	queueMu   sync.Mutex
	closed    bool
	done      chan struct{}
	closeOnce sync.Once

	droppedTotal  atomic.Uint64
	droppedWait   atomic.Uint64
	warning       atomic.Value // string
	stderrWarning atomic.Value // string

	fileMu    sync.Mutex // protects only health/file state read by Status
	file      *os.File
	fileBytes uint64
	nextRetry time.Time
	suspended bool

	statusMu    sync.Mutex
	statusAt    time.Time
	statusBytes uint64
	statusFiles int
}

// New starts a bounded diagnostics writer. File-system failures are retained in
// Status and records remain available through stderr when possible.
func New(directory string, level slog.Level, stderr io.Writer) *Manager {
	m := &Manager{
		directory: directory,
		stderr:    stderr,
		level:     new(slog.LevelVar),
		queue:     make(chan []byte, queueSize),
		done:      make(chan struct{}),
	}
	m.level.Set(level)
	m.warning.Store("")
	m.stderrWarning.Store("")
	m.runID = processRunID
	defaults := []slog.Attr{slog.String("run_id", m.runID), slog.String("component", "daemon")}
	m.logger = slog.New(newJSONHandler(recordWriter{m}, m.level, defaults))
	m.startup()
	go m.run()
	return m
}

// Logger returns the manager's structured logger.
func (m *Manager) Logger() *slog.Logger { return m.logger }

// SetLevel changes the minimum level without stopping the writer.
func (m *Manager) SetLevel(level slog.Level) { m.level.Set(level) }

// Status returns cached filesystem metadata (for no longer than five seconds).
func (m *Manager) Status() Status {
	now := nowUTC()
	m.statusMu.Lock()
	if now.Sub(m.statusAt) >= 5*time.Second {
		m.statusAt = now
		m.statusBytes, m.statusFiles = scanManaged(m.directory)
	}
	stored, files := m.statusBytes, m.statusFiles
	m.statusMu.Unlock()
	return Status{
		Level:          m.level.Level().String(),
		Directory:      m.directory,
		StoredBytes:    stored,
		FileCount:      files,
		RotationBytes:  rotationBytes,
		MaxArchives:    maxArchives,
		DroppedRecords: m.droppedTotal.Load(),
		Warning:        m.outputWarning(),
	}
}

func (m *Manager) outputWarning() string {
	return strings.Trim(strings.Join([]string{m.warning.Load().(string), m.stderrWarning.Load().(string)}, "; "), "; ")
}

// Close stops accepting records and waits at most two seconds for the queue to drain.
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		m.queueMu.Lock()
		m.closed = true
		close(m.queue)
		m.queueMu.Unlock()
	})
	select {
	case <-m.done:
	case <-time.After(shutdownWait):
	}
}

// Done closes after the output worker has stopped touching managed files.
func (m *Manager) Done() <-chan struct{} { return m.done }

func (m *Manager) startup() {
	if err := prepareDirectory(m.directory); err != nil {
		m.suspend("diagnostics directory unavailable")
		return
	}
	if err := m.recoverFiles(); err != nil {
		m.suspend("diagnostics recovery incomplete")
		return
	}
	m.openFile()
}

func (m *Manager) suspend(warning string) {
	m.fileMu.Lock()
	m.suspended = true
	m.nextRetry = nowUTC().Add(retryDelay)
	m.fileMu.Unlock()
	m.setWarning(warning)
}

func (m *Manager) run() {
	defer close(m.done)
	lastWarning := ""
	var nextWarning time.Time
	for line := range m.queue {
		m.writeDirect(line)
		warning := m.outputWarning()
		if warning != lastWarning && (warning == "" || !nowUTC().Before(nextWarning)) {
			lastWarning, nextWarning = warning, nowUTC().Add(retryDelay)
			level, event := slog.LevelWarn, "diagnostic_output_failed"
			if warning == "" {
				level, event = slog.LevelInfo, "diagnostic_output_recovered"
			}
			if level >= m.level.Level() {
				m.writeDirect(m.encodeRecord(level, event, slog.String("warning", warning)))
			}
		}
		if len(m.queue) < queueSize/2 {
			m.writeDropSummary()
		}
	}
	m.writeDropSummary()
	m.fileMu.Lock()
	if m.file != nil {
		if err := m.file.Sync(); err != nil {
			m.setWarning("diagnostics file sync failed")
		}
		m.closeFileLocked()
	}
	m.fileMu.Unlock()
}

func (m *Manager) writeDropSummary() {
	if m.level.Level() > slog.LevelWarn {
		return
	}
	if n := m.droppedWait.Swap(0); n != 0 {
		m.writeDirect(m.encodeRecord(slog.LevelWarn, "diagnostic records dropped", slog.Uint64("dropped", n)))
	}
}

func (m *Manager) writeDirect(line []byte) {
	m.fileMu.Lock()
	_ = m.writeFileLocked(line)
	m.fileMu.Unlock()
	if m.stderr != nil {
		if err := writeAll(m.stderr, line); err != nil {
			m.stderrWarning.Store("diagnostics stderr write failed")
		} else {
			m.stderrWarning.Store("")
		}
	}
}

func (m *Manager) writeFileLocked(line []byte) bool {
	if m.file == nil {
		m.openFileLocked()
		if m.file == nil {
			return false
		}
	}
	if m.fileBytes+uint64(len(line)) > rotationBytes {
		if err := m.rotateLocked(); err != nil {
			m.setWarning("diagnostics rotation suspended")
			m.closeFileLocked()
			m.nextRetry = nowUTC().Add(retryDelay)
			m.suspended = true
			return false
		}
		m.openFileLocked()
		if m.file == nil {
			return false
		}
	}
	if err := writeAll(m.file, line); err != nil {
		m.setWarning("diagnostics file write failed")
		m.closeFileLocked()
		m.nextRetry = nowUTC().Add(retryDelay)
		m.suspended = true
		return false
	}
	m.fileBytes += uint64(len(line))
	return true
}

func (m *Manager) openFile() {
	m.fileMu.Lock()
	m.openFileLocked()
	m.fileMu.Unlock()
}

func (m *Manager) openFileLocked() {
	if m.file != nil || nowUTC().Before(m.nextRetry) {
		return
	}
	if err := prepareDirectory(m.directory); err != nil {
		m.setWarning("diagnostics directory unavailable")
		m.nextRetry = nowUTC().Add(retryDelay)
		m.suspended = true
		return
	}
	if m.suspended {
		if err := m.recoverFiles(); err != nil {
			m.setWarning("diagnostics recovery incomplete")
			m.nextRetry = nowUTC().Add(retryDelay)
			return
		}
	}
	path := filepath.Join(m.directory, "daemon.log")
	f, err := openRegular(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
	if err != nil {
		m.setWarning("diagnostics file unavailable")
		m.nextRetry = nowUTC().Add(retryDelay)
		m.suspended = true
		return
	}
	st, err := f.Stat()
	if err == nil && uint64(st.Size()) > rotationBytes {
		err = errors.New("active log exceeds rotation limit")
	}
	if err != nil {
		_ = f.Close()
		m.setWarning("diagnostics file unavailable")
		m.nextRetry = nowUTC().Add(retryDelay)
		m.suspended = true
		return
	}
	m.file, m.fileBytes = f, uint64(st.Size())
	m.suspended = false
	m.setWarning("")
}

func (m *Manager) rotateLocked() error {
	if m.file == nil {
		return errors.New("no active file")
	}
	if err := pruneArchives(m.directory, maxArchives-1); err != nil {
		return err
	}
	if err := m.file.Sync(); err != nil {
		return err
	}
	if err := m.file.Close(); err != nil {
		return err
	}
	m.file = nil
	seq, err := nextSequence(m.directory)
	if err != nil {
		return err
	}
	raw := filepath.Join(m.directory, fmt.Sprintf("daemon-%06d.log", seq))
	if err := os.Rename(filepath.Join(m.directory, "daemon.log"), raw); err != nil {
		return err
	}
	if err := os.Chmod(raw, 0600); err != nil {
		return err
	}
	if err := compressRaw(raw); err != nil {
		return err
	}
	return pruneArchives(m.directory, maxArchives)
}

func (m *Manager) closeFileLocked() {
	if m.file != nil {
		if err := m.file.Close(); err != nil {
			m.setWarning("diagnostics file close failed")
		}
		m.file = nil
	}
}

func (m *Manager) setWarning(s string) { m.warning.Store(s) }

func (m *Manager) recoverFiles() error {
	entries, err := os.ReadDir(m.directory)
	if err != nil {
		return err
	}
	// Reject every managed non-regular entry before doing any deletion.
	for _, e := range entries {
		if managedRE.MatchString(e.Name()) {
			if _, err := checkedRegular(filepath.Join(m.directory, e.Name())); err != nil {
				return err
			}
		}
	}
	var first error
	for _, e := range entries {
		if !rawRE.MatchString(e.Name()) {
			continue
		}
		raw := filepath.Join(m.directory, e.Name())
		if err := compressRaw(raw); err != nil && first == nil {
			first = err
		}
	}
	entries, _ = os.ReadDir(m.directory)
	for _, e := range entries {
		if !tmpRE.MatchString(e.Name()) {
			continue
		}
		tmp := filepath.Join(m.directory, e.Name())
		raw := strings.TrimSuffix(tmp, ".gz.tmp")
		if _, err := checkedRegular(raw); err == nil {
			if err := os.Remove(tmp); err != nil && first == nil {
				first = err
			}
		} else {
			// Without a source, only a complete gzip can be recovered safely.
			gz := strings.TrimSuffix(tmp, ".tmp")
			if err := validGzip(tmp); err != nil {
				if first == nil {
					first = errors.New("orphaned compression temporary")
				}
				continue
			}
			if _, err := os.Lstat(gz); !os.IsNotExist(err) {
				if first == nil {
					first = errors.New("orphaned compression temporary conflicts with archive")
				}
				continue
			}
			if err := pruneArchives(m.directory, maxArchives-1); err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			if err := os.Rename(tmp, gz); err != nil && first == nil {
				first = err
			}
		}
	}
	if err := pruneArchives(m.directory, maxArchives); err != nil && first == nil {
		first = err
	}
	return first
}

type recordWriter struct{ manager *Manager }

func (w recordWriter) Write(line []byte) (int, error) {
	n := len(line)
	if n > maxRecordBytes {
		line = w.manager.encodeRecord(slog.LevelWarn, "diagnostic_record_omitted", slog.Bool("record_omitted", true), slog.String("reason", "oversize"), slog.Int("original_bytes", n))
	}
	w.manager.enqueue(line)
	return n, nil
}

func newJSONHandler(w io.Writer, level *slog.LevelVar, attrs []slog.Attr) slog.Handler {
	var h slog.Handler = slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			for _, group := range groups {
				if forbiddenKey(group) {
					return slog.Attr{}
				}
			}
			if forbiddenKey(a.Key) {
				return slog.Attr{}
			}
			if a.Key == slog.TimeKey && a.Value.Kind() == slog.KindTime {
				return slog.Time(a.Key, a.Value.Time().UTC())
			}
			if a.Value.Kind() == slog.KindString && a.Key != slog.MessageKey {
				if a.Key == "type" {
					switch a.Value.String() {
					case "P", "F", "D", "S":
					default:
						return slog.String(a.Key, "unknown")
					}
				}
				return slog.String(a.Key, boundString(a.Value.String()))
			}
			if a.Value.Kind() != slog.KindAny {
				return a
			}
			v := a.Value.Any()
			if err, ok := v.(error); ok {
				return slog.Attr{Key: a.Key, Value: slog.GroupValue(SafeError(err)...)}
			}
			switch x := v.(type) {
			case slog.Level:
				return slog.String(a.Key, x.String())
			default:
				return slog.Attr{}
			}
		},
	})
	return h.WithAttrs(attrs)
}

func (m *Manager) encodeRecord(level slog.Level, msg string, attrs ...slog.Attr) []byte {
	var b bytes.Buffer
	r := slog.NewRecord(nowUTC(), level, msg, 0)
	r.AddAttrs(attrs...)
	_ = newJSONHandler(&b, m.level, []slog.Attr{slog.String("run_id", m.runID), slog.String("component", "daemon")}).Handle(context.Background(), r)
	return b.Bytes()
}

func (m *Manager) enqueue(line []byte) {
	m.queueMu.Lock()
	defer m.queueMu.Unlock()
	if m.closed {
		return
	}
	select {
	case m.queue <- append([]byte(nil), line...):
	default:
		m.droppedTotal.Add(1)
		m.droppedWait.Add(1)
	}
}

// SafeError exposes only stable classifications and allowlisted operation names.
func SafeError(err error) []slog.Attr {
	if err == nil {
		return nil
	}
	class := "error"
	var syscallName, errnoText, netOp, netNetwork string
	var timeout, temporary, canceled, permission bool
	walkErrors(err, func(e error) {
		switch x := e.(type) {
		case *net.OpError:
			class = "network"
			netOp, netNetwork = safeOp(x.Op), safeNetwork(x.Net)
		case *net.DNSError:
			timeout, temporary = timeout || x.IsTimeout, temporary || x.IsTemporary
		case *os.SyscallError:
			class = "syscall"
			syscallName = safeOp(x.Syscall)
			if errno, ok := x.Err.(syscall.Errno); ok {
				errnoText = strconv.Itoa(int(errno))
			}
		case *os.PathError:
			if sameError(x.Err, os.ErrPermission) {
				class, permission = "permission", true
			}
			if op := safeOp(x.Op); op != "" {
				netOp = op
			}
		case syscall.Errno:
			class = "syscall"
			errnoText = strconv.Itoa(int(x))
		default:
			switch {
			case sameError(e, context.Canceled):
				canceled = true
			case sameError(e, context.DeadlineExceeded):
				timeout = true
			case sameError(e, io.ErrUnexpectedEOF):
				class = "unexpected_eof"
			case sameError(e, io.EOF):
				class = "eof"
			case sameError(e, os.ErrPermission):
				class, permission = "permission", true
			}
		}
	})
	if canceled {
		class = "canceled"
	} else if timeout {
		class = "timeout"
	}
	attrs := []slog.Attr{slog.String("error_class", class)}
	if syscallName != "" {
		attrs = append(attrs, slog.String("syscall", syscallName))
	}
	if errnoText != "" {
		attrs = append(attrs, slog.String("errno", errnoText))
	}
	if permission {
		attrs = append(attrs, slog.String("reason", "permission denied"))
	}
	if netOp != "" {
		attrs = append(attrs, slog.String("op", netOp))
	}
	if netNetwork != "" {
		attrs = append(attrs, slog.String("network", netNetwork))
	}
	if timeout {
		attrs = append(attrs, slog.Bool("timeout", true))
	}
	if temporary {
		attrs = append(attrs, slog.Bool("temporary", true))
	}
	return attrs
}

func walkErrors(err error, visit func(error)) {
	queue := []error{err}
	for steps := 0; len(queue) > 0 && steps < 32; steps++ {
		e := queue[0]
		queue = queue[1:]
		if e == nil {
			continue
		}
		visit(e)
		switch u := e.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range u.Unwrap() {
				if len(queue)+steps < 32 {
					queue = append(queue, child)
				}
			}
		case interface{ Unwrap() error }:
			if steps+len(queue) < 32 {
				queue = append(queue, u.Unwrap())
			}
		}
	}
}

func sameError(a, b error) bool {
	if a == nil || b == nil {
		return false
	}
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	return ta.Comparable() && tb.Comparable() && a == b
}

func safeOp(s string) string {
	switch s {
	case "accept", "bind", "close", "connect", "dial", "listen", "lookup", "open", "read", "recvfrom", "rename", "sendto", "stat", "write":
		return s
	}
	return ""
}
func safeNetwork(s string) string {
	switch s {
	case "ip", "ip4", "ip6", "tcp", "tcp4", "tcp6", "udp", "udp4", "udp6", "unix", "unixpacket":
		return s
	}
	return ""
}
func boundString(s string) string {
	if len(s) > maxSafeString {
		return s[:maxSafeString]
	}
	return s
}
func forbiddenKey(key string) bool {
	key = strings.ToLower(key)
	for _, bad := range []string{"secret", "password", "passwd", "token", "credential", "authorization", "cookie", "private", "path", "filename", "payload", "query", "hook", "config"} {
		if strings.Contains(key, bad) {
			return true
		}
	}
	return false
}

func prepareDirectory(dir string) error {
	if st, err := os.Lstat(dir); err == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
			return errors.New("not a directory")
		}
	} else if os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	} else {
		return err
	}
	return os.Chmod(dir, 0700)
}

func openRegular(path string, flags int) (*os.File, error) {
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return nil, errors.New("managed file is not regular")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, flags|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func checkedRegular(path string) (os.FileInfo, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return nil, errors.New("not regular")
	}
	return st, nil
}

var (
	rawRE     = regexp.MustCompile(`^daemon-[0-9]{6}\.log$`)
	gzRE      = regexp.MustCompile(`^daemon-[0-9]{6}\.log\.gz$`)
	tmpRE     = regexp.MustCompile(`^daemon-[0-9]{6}\.log\.gz\.tmp$`)
	managedRE = regexp.MustCompile(`^daemon-[0-9]{6}\.log(?:\.gz|\.gz\.tmp)?$`)
	seqRE     = regexp.MustCompile(`^daemon-([0-9]{6})\.log(?:\.gz)?$`)
)

func archiveNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !rawRE.MatchString(e.Name()) && !gzRE.MatchString(e.Name()) {
			continue
		}
		if _, err := checkedRegular(filepath.Join(dir, e.Name())); err != nil {
			return nil, err
		}
		names = append(names, e.Name())
	}
	return names, nil
}

func pruneArchives(dir string, keep int) error {
	if keep < 0 {
		keep = 0
	}
	names, err := archiveNames(dir)
	if err != nil {
		return err
	}
	for len(names) > keep {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
			return err
		}
		names = names[1:]
	}
	return nil
}

func nextSequence(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	max := 0
	for _, e := range entries {
		match := seqRE.FindStringSubmatch(e.Name())
		if match == nil {
			continue
		}
		var n int
		_, _ = fmt.Sscanf(match[1], "%d", &n)
		if n > max {
			max = n
		}
	}
	if max >= 999999 {
		return 0, errors.New("archive sequence exhausted")
	}
	return max + 1, nil
}

func compressRaw(raw string) error {
	if st, err := checkedRegular(raw); err != nil {
		return err
	} else if uint64(st.Size()) > rotationBytes {
		return errors.New("recovery segment exceeds rotation limit")
	}
	gz := strings.TrimSuffix(raw, ".log") + ".log.gz"
	if _, err := checkedRegular(gz); err == nil {
		if err := validGzip(gz); err != nil {
			return err
		}
		if _, err := os.Lstat(gz + ".tmp"); err == nil {
			if _, err := checkedRegular(gz + ".tmp"); err != nil {
				return err
			}
			if err := os.Remove(gz + ".tmp"); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		return os.Remove(raw)
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp := gz + ".tmp"
	if _, err := os.Lstat(tmp); err == nil {
		if _, err := checkedRegular(tmp); err != nil {
			return err
		}
		if err := os.Remove(tmp); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	in, err := os.OpenFile(raw, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		_ = in.Close()
		return err
	}
	zw, err := gzip.NewWriterLevel(out, gzip.BestSpeed)
	if err == nil {
		_, err = io.Copy(zw, in)
	}
	if closeErr := zw.Close(); err == nil {
		err = closeErr
	}
	if chmodErr := out.Chmod(0600); err == nil {
		err = chmodErr
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	_ = in.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if _, err := os.Lstat(gz); err == nil {
		return errors.New("archive appeared during compression")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(tmp, gz); err != nil {
		return err
	}
	return os.Remove(raw)
}

func validGzip(path string) error {
	if _, err := checkedRegular(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	z, err := gzip.NewReader(f)
	if err == nil {
		n, copyErr := io.Copy(io.Discard, io.LimitReader(z, int64(rotationBytes)+1))
		err = copyErr
		if n > int64(rotationBytes) {
			err = errors.New("archive exceeds rotation limit")
		}
		if closeErr := z.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func scanManaged(dir string) (uint64, int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	var total uint64
	count := 0
	for _, e := range entries {
		name := e.Name()
		if name != "daemon.log" && !managedRE.MatchString(name) {
			continue
		}
		st, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		total += uint64(st.Size())
		count++
	}
	return total, count
}

func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if n > 0 {
			p = p[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
