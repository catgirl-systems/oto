package diagnostics

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockedOutput struct {
	entered, release chan struct{}
	once             sync.Once
}

func (w *blockedOutput) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}
func TestBoundedQueueAndShutdown(t *testing.T) {
	w := &blockedOutput{entered: make(chan struct{}), release: make(chan struct{})}
	m := New(t.TempDir(), slog.LevelDebug, w)
	m.Logger().Info("first")
	<-w.entered
	for i := 0; i < queueSize+10; i++ {
		m.Logger().Debug("pending", "number", i)
	}
	if got := m.Status().DroppedRecords; got != 10 {
		t.Fatalf("dropped=%d", got)
	}
	started := time.Now()
	m.Close()
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("close blocked: %v", elapsed)
	}
	close(w.release)
	<-m.done
	b, err := os.ReadFile(filepath.Join(m.directory, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("diagnostic records dropped")) {
		t.Fatal("missing loss summary")
	}
}

type failedOutput struct{}

func (failedOutput) Write([]byte) (int, error) { return 0, errors.New("SENSITIVE-output-path") }
func TestOutputFailuresIndependent(t *testing.T) {
	for _, brokenFile := range []bool{false, true} {
		t.Run(fmt.Sprint(brokenFile), func(t *testing.T) {
			dir := t.TempDir()
			var mirror bytes.Buffer
			var output io.Writer = failedOutput{}
			if brokenFile {
				if err := os.Mkdir(filepath.Join(dir, "daemon.log"), 0700); err != nil {
					t.Fatal(err)
				}
				output = &mirror
			}
			m := New(dir, slog.LevelInfo, output)
			m.Logger().Info("still_available")
			m.Close()
			if m.Status().Warning == "" {
				t.Fatal("missing output warning")
			}
			if brokenFile {
				if !strings.Contains(mirror.String(), "still_available") {
					t.Fatal("stderr lost")
				}
			} else {
				b, _ := os.ReadFile(filepath.Join(dir, "daemon.log"))
				if !bytes.Contains(b, []byte("still_available")) {
					t.Fatal("file lost")
				}
			}
		})
	}
}
func TestRotationContinuityAndMetadata(t *testing.T) {
	old := rotationBytes
	rotationBytes = 2048
	t.Cleanup(func() { rotationBytes = old })
	dir := t.TempDir()
	var mirror bytes.Buffer
	m := New(dir, slog.LevelDebug, &mirror)
	for i := 0; i < 25; i++ {
		m.Logger().Info("numbered_event", "number", i)
	}
	m.Close()
	names, err := archiveNames(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) < 1 || len(names) > 3 {
		t.Fatalf("expected actual rotations, got %v", names)
	}
	names = append(names, "daemon.log")
	seen := map[int]bool{}
	var stored uint64
	for _, name := range names {
		p := filepath.Join(dir, name)
		st, _ := os.Stat(p)
		stored += uint64(st.Size())
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		var r io.Reader = f
		if strings.HasSuffix(name, ".gz") {
			z, e := gzip.NewReader(f)
			if e != nil {
				t.Fatal(e)
			}
			defer z.Close()
			r = z
		}
		d := json.NewDecoder(r)
		for {
			var v map[string]any
			e := d.Decode(&v)
			if e == io.EOF {
				break
			}
			if e != nil {
				t.Fatal(e)
			}
			seen[int(v["number"].(float64))] = true
		}
		f.Close()
	}
	if len(seen) != 25 {
		t.Fatalf("lost records during rotation: %d", len(seen))
	}
	if m.Status().StoredBytes != stored || m.Status().FileCount != len(names) {
		t.Fatal("incorrect metadata")
	}
	if strings.Count(mirror.String(), "numbered_event") != 25 {
		t.Fatal("not every event mirrored")
	}
}
func TestPrivacyGroupsAndOversize(t *testing.T) {
	var mirror bytes.Buffer
	m := New(t.TempDir(), slog.LevelDebug, &mirror)
	m.Logger().WithGroup("secret").Info("allowed_event", "value", "SENSITIVE")
	m.Logger().Info("safe", "config", struct{ Password string }{"SENSITIVE"}, "filename", "SENSITIVE", "error", fmt.Errorf("SENSITIVE: %w", &os.PathError{Op: "SENSITIVE", Path: "SENSITIVE", Err: os.ErrPermission}))
	m.Logger().Info(strings.Repeat("x", maxRecordBytes+1))
	m.Logger().With("before", 1).WithGroup("group").Info("grouped", "inside", 2)
	m.Close()
	if strings.Contains(mirror.String(), "SENSITIVE") {
		t.Fatal("sensitive content leaked")
	}
	for _, line := range bytes.Split(bytes.TrimSpace(mirror.Bytes()), []byte{'\n'}) {
		if len(line)+1 > maxRecordBytes || !json.Valid(line) {
			t.Fatal("unbounded or invalid JSON")
		}
	}
	if !strings.Contains(mirror.String(), "record_omitted") {
		t.Fatal("oversized record not omitted")
	}
	if !strings.Contains(mirror.String(), `"group":{"inside":2}`) {
		t.Fatal("group semantics lost")
	}
}
func TestCorruptArchivePreservesSource(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "daemon-000001.log")
	os.WriteFile(raw, []byte("source\n"), 0600)
	os.WriteFile(raw+".gz", []byte("broken"), 0600)
	m := New(dir, slog.LevelInfo, io.Discard)
	m.Logger().Info("not_to_file")
	m.Close()
	if m.Status().Warning == "" {
		t.Fatal("corruption not reported")
	}
	if b, _ := os.ReadFile(raw); string(b) != "source\n" {
		t.Fatal("recovery source lost")
	}
	if _, e := os.Stat(filepath.Join(dir, "daemon.log")); !os.IsNotExist(e) {
		t.Fatal("continued after failed recovery")
	}
}
func BenchmarkLogging(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		b.Run(fmt.Sprint(enabled), func(b *testing.B) {
			m := New(b.TempDir(), slog.LevelInfo, io.Discard)
			defer m.Close()
			if enabled {
				m.SetLevel(slog.LevelDebug)
			}
			logger := m.Logger()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				logger.LogAttrs(context.Background(), slog.LevelDebug, "transfer_summary", slog.Int("bytes", i))
			}
			b.StopTimer()
			b.ReportMetric(float64(m.Status().DroppedRecords), "dropped")
			b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "records/s")
		})
	}
}

func TestInterruptedCompressionRecovery(t *testing.T) {
	for _, state := range []string{"raw_temp", "published_raw_temp", "complete_orphan", "incomplete_orphan"} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			raw := filepath.Join(dir, "daemon-000001.log")
			tmp := raw + ".gz.tmp"
			var encoded bytes.Buffer
			z := gzip.NewWriter(&encoded)
			z.Write([]byte("original\n"))
			z.Close()
			if state == "raw_temp" || state == "published_raw_temp" {
				os.WriteFile(raw, []byte("original\n"), 0600)
			}
			if state == "published_raw_temp" {
				os.WriteFile(raw+".gz", encoded.Bytes(), 0600)
			}
			data := []byte("incomplete")
			if state == "complete_orphan" {
				data = encoded.Bytes()
			}
			os.WriteFile(tmp, data, 0600)
			m := New(dir, slog.LevelInfo, nil)
			m.Logger().Info("ready")
			m.Close()
			if state == "incomplete_orphan" {
				if m.Status().Warning == "" {
					t.Fatal("orphan did not suspend files")
				}
				if b, _ := os.ReadFile(tmp); string(b) != "incomplete" {
					t.Fatal("orphan discarded")
				}
				return
			}
			if m.Status().Warning != "" {
				t.Fatal(m.Status().Warning)
			}
			if err := validGzip(raw + ".gz"); err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{raw, tmp} {
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Fatal("recovery residue", p)
				}
			}
		})
	}
}
func TestWriteRetryAndRotationFailures(t *testing.T) {
	dir := t.TempDir()
	var mirror bytes.Buffer
	m := New(dir, slog.LevelInfo, &mirror)
	defer m.Close()
	m.fileMu.Lock()
	m.file.Close()
	m.fileMu.Unlock()
	line := m.encodeRecord(slog.LevelInfo, "write_test")
	m.writeDirect(line)
	m.fileMu.Lock()
	suspended, retry := m.suspended, m.nextRetry
	m.openFileLocked()
	early := m.file != nil
	m.nextRetry = time.Time{}
	m.openFileLocked()
	recovered := m.file != nil
	m.fileMu.Unlock()
	if !suspended || retry.Before(time.Now().Add(29*time.Second)) || early || !recovered {
		t.Fatal("incorrect bounded retry")
	}
	if !bytes.Contains(mirror.Bytes(), []byte("write_test")) {
		t.Fatal("stderr lost during file failure")
	}
	os.Mkdir(filepath.Join(dir, "daemon-000001.log.gz"), 0700)
	m.fileMu.Lock()
	before := m.fileBytes
	err := m.rotateLocked()
	after := m.fileBytes
	m.fileMu.Unlock()
	if err == nil || after != before {
		t.Fatal("unsafe pruning or failed rotation grew file")
	}
	raw := filepath.Join(t.TempDir(), "daemon-000001.log")
	os.WriteFile(raw, []byte("source"), 0600)
	os.Mkdir(raw+".gz.tmp", 0700)
	if err := compressRaw(raw); err == nil {
		t.Fatal("unsafe compression target accepted")
	}
	if b, _ := os.ReadFile(raw); string(b) != "source" {
		t.Fatal("compression lost source")
	}
}
func TestExactRotationBoundary(t *testing.T) {
	m := New(t.TempDir(), slog.LevelInfo, nil)
	line := m.encodeRecord(slog.LevelInfo, "boundary")
	old := rotationBytes
	rotationBytes = uint64(2 * len(line))
	defer func() { m.Close(); rotationBytes = old }()
	m.writeDirect(line)
	m.writeDirect(line)
	if names, _ := archiveNames(m.directory); len(names) != 0 {
		t.Fatal("rotated before exact boundary")
	}
	m.writeDirect(line)
	if names, _ := archiveNames(m.directory); len(names) != 1 {
		t.Fatal("failed to rotate before exceeding boundary")
	}
	st, err := os.Stat(filepath.Join(m.directory, "daemon.log"))
	if err != nil || st.Size() != int64(len(line)) {
		t.Fatal("active segment size")
	}
}
