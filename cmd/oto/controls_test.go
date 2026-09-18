package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/ipc"
)

func commandOutput(t *testing.T, args ...string) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	must(t, err)
	os.Stdout = w
	defer func() { os.Stdout = old; r.Close() }()
	err = run(args)
	w.Close()
	data, readErr := io.ReadAll(r)
	failIf(t, readErr != nil, readErr)
	return string(data), err
}

func TestCLIControls(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, args := range [][]string{{"typo"}, {"pause"}, {"pause", "d-1", "d-2"}, {"resume", "--bad"}, {"transfers", "unexpected"}, {"rescan", "unexpected"}, {"status", "unexpected"}} {
		if _, err := commandOutput(t, args...); err == nil {
			t.Fatalf("accepted invalid arguments %v", args)
		}
	}
	if _, err := commandOutput(t, "transfers"); err == nil {
		t.Fatal("missing daemon should fail")
	}
	if _, err := os.Stat(config.SocketPath()); !os.IsNotExist(err) {
		t.Fatal("CLI started a daemon")
	}
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "test", "password"
	cfg.Soulseek.ConnectOnStartup = false
	cfg.DownloadDir = t.TempDir()
	cfg.Shares = []config.Share{{Name: "Music", Path: t.TempDir()}}
	svc, err := daemon.New(cfg, filepath.Join(t.TempDir(), "journal"))
	must(t, err)
	server := ipc.NewServer(svc, config.SocketPath())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() { svc.Close(); cancel(); <-done }()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := ipc.NewClient(config.SocketPath()).Status(context.Background()); err == nil {
			break
		}
		failIf(t, time.Now().After(deadline), "socket unavailable")
		time.Sleep(time.Millisecond)
	}
	if output, err := commandOutput(t, "transfers", "--json"); err != nil || output != "[]\n" {
		t.Fatalf("empty transfers %q %v", output, err)
	}
	if output, err := commandOutput(t, "rescan", "--cancel"); err != nil || !strings.Contains(output, "No share scan") {
		t.Fatalf("idle cancel: %s %v", output, err)
	}
	ds, err := svc.QueueDownloads([]daemon.DownloadRequest{{Username: "peer", Files: []daemon.DownloadItem{{Filename: "Album/song.flac", Size: 9}}}})
	must(t, err)
	id := ds[0].ID
	if _, err := commandOutput(t, "pause", id); err != nil {
		t.Fatal(err)
	}
	failIf(t, svc.Downloads()[0].State != "paused", "pause not routed")
	if output, err := commandOutput(t, "transfers"); err != nil || !strings.Contains(output, id) || !strings.Contains(output, `state="paused"`) {
		t.Fatalf("text transfers %q %v", output, err)
	}
	output, err := commandOutput(t, "transfers", "--json")
	must(t, err)
	var transfers []daemon.Transfer
	if err := json.Unmarshal([]byte(output), &transfers); err != nil || len(transfers) != 1 || transfers[0].ID != id {
		t.Fatalf("JSON %s %v", output, err)
	}
	for _, field := range []string{`"elapsed_ms":null`, `"speed_bps":0`, `"eta_seconds":null`} {
		failIfFmt(t, !strings.Contains(output, field), "missing timing %s: %s", field, output)
	}
	if _, err := commandOutput(t, "resume", id); err != nil {
		t.Fatal(err)
	}
	failIf(t, svc.Downloads()[0].State != "queued", "offline resume not queued")
	for _, id := range []string{"d-missing", "upload:peer:file"} {
		if _, err := commandOutput(t, "pause", id); err == nil {
			t.Fatalf("unsupported ID accepted %s", id)
		}
	}
	if _, err := commandOutput(t, "rescan"); err != nil {
		t.Fatal(err)
	}
	snap := svc.Snapshot()
	failIfFmt(t, snap.ShareScan == nil || snap.ShareScan.State != "completed" || snap.ShareIndexRevision != 1, "rescan didn't wait: %+v", snap)
	if _, err := commandOutput(t, "status", "--json"); err != nil {
		t.Fatal(err)
	}
}
