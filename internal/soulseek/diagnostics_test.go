package soulseek

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/diagnostics"
)

func protocolLogger(t *testing.T, check func([]map[string]any)) *slog.Logger {
	return protocolDiagnostics(t, check, nil).Logger().With("transfer_id", "d-test", "attempt_id", 2)
}

type protocolBlockedOutput chan struct{}

func (w protocolBlockedOutput) Write(p []byte) (int, error) { <-w; return len(p), nil }
func protocolDiagnostics(t *testing.T, check func([]map[string]any), stderr io.Writer) *diagnostics.Manager {
	t.Helper()
	dir := t.TempDir()
	m := diagnostics.New(dir, slog.LevelDebug, stderr)
	t.Cleanup(func() {
		m.Close()
		data, err := os.ReadFile(filepath.Join(dir, "daemon.log"))
		if err != nil {
			t.Fatal(err)
		}
		for _, sentinel := range []string{"song", "File not shared", "PRIVATE_PATH", "PRIVATE_CONTENT"} {
			if bytes.Contains(data, []byte(sentinel)) {
				t.Fatalf("leaked %s", sentinel)
			}
		}
		var records []map[string]any
		for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
			var r map[string]any
			if err := json.Unmarshal(line, &r); err != nil {
				t.Fatal(err)
			}
			if r["run_id"] == nil || r["component"] == nil {
				t.Fatal("missing identity")
			}
			records = append(records, r)
		}
		check(records)
	})
	return m
}
func requireEvent(t *testing.T, records []map[string]any, event string) map[string]any {
	t.Helper()
	for _, r := range records {
		if r["msg"] == event {
			return r
		}
	}
	t.Fatalf("missing event %s: %v", event, records)
	return nil
}
func checkDownloadEvents(t *testing.T, action string, records []map[string]any) {
	requireEvent(t, records, "transfer_queued")
	if action == "before_accept" || action == "separate_size_change" || action == "blocked_logging" {
		return
	}
	matched := requireEvent(t, records, "file_token_matched")
	first := requireEvent(t, records, "transfer_first_read")
	if matched["connection_id"] != first["connection_id"] || matched["operation_id"] != first["operation_id"] || matched["transfer_id"] != "d-test" || matched["attempt_id"] != float64(2) {
		t.Fatal("file correlation changed")
	}
	if action == "disk_failure" {
		requireEvent(t, records, "transfer_write_ended")
		requireEvent(t, records, "transfer_failed")
		return
	}
	requireEvent(t, records, "transfer_first_write")
	switch action {
	case "separate_failure":
		requireEvent(t, records, "remote_upload_failed")
	case "rejected", "separate_rejection":
		requireEvent(t, records, "transfer_rejected")
	case "short_file":
		requireEvent(t, records, "transfer_failed")
	default:
		control := requireEvent(t, records, "control_closed_file_continues")
		if control["connection_id"] == matched["connection_id"] || control["operation_id"] != matched["operation_id"] {
			t.Fatal("P/F association lost")
		}
		requireEvent(t, records, "transfer_completed")
	}
}
func TestBrowseFailureStages(t *testing.T) {
	for _, body := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_header", true: "interrupted_body"}[body], func(t *testing.T) {
			logger := protocolLogger(t, func(records []map[string]any) {
				stage := requireEvent(t, records, "frame_read_failed")["stage"]
				want := "frame_length"
				if body {
					want = "frame_body"
					requireEvent(t, records, "frame_header_received")
				}
				if stage != want {
					t.Fatalf("stage=%v want=%s", stage, want)
				}
				requireEvent(t, records, "browse_read_failed")
			})
			c := NewClient(ClientConfig{Logger: logger})
			left, right := net.Pipe()
			defer left.Close()
			go func() {
				defer right.Close()
				ReadFrame(right)
				if body {
					binary.Write(right, binary.LittleEndian, uint32(12))
					binary.Write(right, binary.LittleEndian, uint32(PeerSharedList))
					right.Write([]byte{1, 2})
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := c.Browse(ctx, left, ""); err == nil {
				t.Fatal("interrupted response accepted")
			}
		})
	}
}
func TestStallSummaryRecoveryAndLimits(t *testing.T) {
	logger := protocolLogger(t, func(records []map[string]any) {
		stalls, recoveries := 0, 0
		for _, r := range records {
			if r["msg"] == "transfer_stalled" {
				stalls++
			}
			if r["msg"] == "transfer_recovered" {
				recoveries++
			}
		}
		if stalls != 1 || recoveries != 1 {
			t.Fatalf("transitions: %d/%d", stalls, recoveries)
		}
		summary := requireEvent(t, records, "transfer_summary")
		if _, ok := summary["last_data_age_ms"]; ok {
			t.Fatal("unknown last data represented as known")
		}
		if summary["committed_bytes"] != float64(3) {
			t.Fatal("resume offset not accounted")
		}
	})
	c := NewClient(ClientConfig{Logger: logger})
	o := c.newObservation(context.Background(), "download", 3, 100)
	defer c.endObservation(o)
	o.begin(logger)
	o.mu.Lock()
	o.started = time.Now().Add(-31 * time.Second)
	o.mu.Unlock()
	c.LogDiagnostics(time.Now())
	c.LogDiagnostics(time.Now())
	o.observe(true, 5, nil)
	o.observe(false, 5, nil)
	if o.committed != 3 || !o.stallWarned {
		t.Fatal("write incorrectly counted as committed progress")
	}
	observedProgress(o, func(Progress) {
		if o.committed != 3 {
			t.Fatal("committed before callback returned")
		}
	})(Progress{Done: 8})
	c.LogDiagnostics(time.Now())
}
func TestObservedWriteKeepsOriginalError(t *testing.T) {
	logger := protocolLogger(t, func(records []map[string]any) { requireEvent(t, records, "transfer_write_ended") })
	c := NewClient(ClientConfig{Logger: logger})
	o := c.newObservation(context.Background(), "download", 0, 1)
	defer c.endObservation(o)
	file, err := os.CreateTemp(t.TempDir(), "PRIVATE_PATH")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	_, err = observedWriterAt{WriterAt: file, observation: o}.WriteAt([]byte("PRIVATE_CONTENT"), 0)
	if err == nil {
		t.Fatal("write error hidden")
	}
}
func TestDiagnosticSocketCapability(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	wrapped := &diagnosticConn{Conn: conn}
	if _, err := wrapped.SyscallConn(); err != nil {
		t.Fatal(err)
	}
	if got := diagnosticEndpoint(&net.UnixAddr{Name: "PRIVATE_PATH", Net: "unix"}); got != "" {
		t.Fatal("Unix path exposed")
	}
}
