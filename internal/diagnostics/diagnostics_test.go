package diagnostics

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestJSONLevelsAndSafeErrors(t *testing.T) {
	dir := t.TempDir()
	m := New(dir, slog.LevelInfo, io.Discard)
	m.Logger().Debug("not stored")
	m.Logger().Info("hello", "secret", "/private/peer")
	netErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("peer secret")}
	attrs := SafeError(netErr)
	m.Logger().LogAttrs(context.Background(), slog.LevelError, "failed", attrs...)
	m.Close()

	f, err := os.Open(filepath.Join(dir, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var records []map[string]any
	s := bufio.NewScanner(f)
	for s.Scan() {
		var record map[string]any
		if err := json.Unmarshal(s.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records=%d, want 2", len(records))
	}
	if records[0]["level"] != "INFO" || records[1]["level"] != "ERROR" {
		t.Fatal("missing or incorrect severity", records)
	}
	for _, record := range records {
		if record["run_id"] == nil || record["component"] != "daemon" || record["time"] == nil {
			t.Fatalf("missing defaults: %#v", record)
		}
		if strings.Contains(string(mustJSON(record)), "peer secret") || strings.Contains(string(mustJSON(record)), "/private") {
			t.Fatalf("private error data leaked: %#v", record)
		}
	}
}

func TestSafeErrorSyscallDoesNotExposePath(t *testing.T) {
	attrs := SafeError(&os.PathError{Op: "open", Path: "/secret/file", Err: os.ErrPermission})
	var b bytes.Buffer
	r := slog.NewRecord(time.Unix(0, 0), slog.LevelError, "failed", 0)
	r.AddAttrs(attrs...)
	if err := slog.NewJSONHandler(&b, nil).Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if strings.Contains(got, "/secret/file") || !strings.Contains(got, "permission denied") {
		t.Fatalf("unexpected safe attrs: %s", got)
	}
}

func TestRotationAndRecoveryArchive(t *testing.T) {
	oldRotation, oldArchives := rotationBytes, maxArchives
	rotationBytes, maxArchives = 220, 2
	t.Cleanup(func() { rotationBytes, maxArchives = oldRotation, oldArchives })
	dir := t.TempDir()
	m := New(dir, slog.LevelInfo, io.Discard)
	for i := 0; i < 30; i++ {
		m.Logger().Info("line", "n", i, "payload", strings.Repeat("x", 20))
	}
	m.Close()
	archives, _ := filepath.Glob(filepath.Join(dir, "daemon-*.log.gz"))
	if len(archives) == 0 || len(archives) > maxArchives {
		t.Fatalf("archives=%v", archives)
	}
	for _, archive := range archives {
		f, err := os.Open(archive)
		if err != nil {
			t.Fatal(err)
		}
		z, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, z); err != nil {
			t.Fatal(err)
		}
		_ = z.Close()
		_ = f.Close()
		if st, err := os.Stat(archive); err != nil || st.Mode().Perm() != 0600 {
			t.Fatalf("archive perms: %v %v", archive, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "unrelated.txt")); !os.IsNotExist(err) {
		t.Fatal("unexpected unrelated file")
	}

	// A raw segment is compressed on the next startup.
	if err := os.WriteFile(filepath.Join(dir, "daemon-999999.log"), []byte("recovery\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m2 := New(dir, slog.LevelInfo, io.Discard)
	m2.Close()
	if _, err := os.Stat(filepath.Join(dir, "daemon-999999.log.gz")); err != nil {
		t.Fatal(err)
	}
}

func TestSymlinkAndUnrelatedPreserved(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "daemon.log")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	m := New(dir, slog.LevelInfo, io.Discard)
	m.Logger().Info("not written through symlink")
	m.Close()
	if got, _ := os.ReadFile(outside); string(got) != "keep" {
		t.Fatal("symlink target changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	if m.Status().Warning == "" {
		t.Fatal("expected health warning")
	}
}

func TestConcurrentLoggingAndLevel(t *testing.T) {
	m := New(t.TempDir(), slog.LevelInfo, io.Discard)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				m.Logger().Debug("disabled")
				m.Logger().Info("ok")
			}
		}()
	}
	m.SetLevel(slog.LevelDebug)
	wg.Wait()
	m.Close()
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

var _ = time.Second
