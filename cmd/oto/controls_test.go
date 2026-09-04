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
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old; r.Close() }()
	err = run(args)
	w.Close()
	data, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatal(readErr)
	}
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
	if err != nil {
		t.Fatal(err)
	}
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
		if time.Now().After(deadline) {
			t.Fatal("socket unavailable")
		}
		time.Sleep(time.Millisecond)
	}
	if output, err := commandOutput(t, "transfers", "--json"); err != nil || output != "[]\n" {
		t.Fatalf("empty transfers %q %v", output, err)
	}
	ds, err := svc.QueueDownloads([]daemon.DownloadRequest{{Username: "peer", Files: []daemon.DownloadItem{{Filename: "Album/song.flac", Size: 9}}}})
	if err != nil {
		t.Fatal(err)
	}
	id := ds[0].ID
	if _, err := commandOutput(t, "pause", id); err != nil {
		t.Fatal(err)
	}
	if svc.Downloads()[0].State != "paused" {
		t.Fatal("pause not routed")
	}
	if output, err := commandOutput(t, "transfers"); err != nil || !strings.Contains(output, id) || !strings.Contains(output, `state="paused"`) {
		t.Fatalf("text transfers %q %v", output, err)
	}
	output, err := commandOutput(t, "transfers", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var transfers []daemon.Transfer
	if err := json.Unmarshal([]byte(output), &transfers); err != nil || len(transfers) != 1 || transfers[0].ID != id {
		t.Fatalf("JSON %s %v", output, err)
	}
	if _, err := commandOutput(t, "resume", id); err != nil {
		t.Fatal(err)
	}
	if svc.Downloads()[0].State != "queued" {
		t.Fatal("offline resume not queued")
	}
	for _, id := range []string{"d-missing", "upload:peer:file"} {
		if _, err := commandOutput(t, "pause", id); err == nil {
			t.Fatalf("unsupported ID accepted %s", id)
		}
	}
	if _, err := commandOutput(t, "rescan"); err != nil {
		t.Fatal(err)
	}
	snap := svc.Snapshot()
	if snap.ShareScan == nil || snap.ShareScan.State != "completed" || snap.ShareIndexRevision != 1 {
		t.Fatalf("rescan didn't wait: %+v", snap)
	}
	if _, err := commandOutput(t, "status", "--json"); err != nil {
		t.Fatal(err)
	}
}
