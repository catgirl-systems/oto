package ipc

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func startIPCSoulfindPeer(t *testing.T, address, username, filename string, contents []byte) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, filename), contents, 0600); err != nil {
		t.Fatal(err)
	}
	shares := soulseek.NewShareIndex()
	if err := shares.AddRoot("Music", root); err != nil {
		t.Fatal(err)
	}
	if err := shares.ScanContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	client := soulseek.NewClient(soulseek.ClientConfig{Address: address, Username: username, Password: "pw", ListenAddr: "0.0.0.0:0", Share: shares})
	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := client.Connect(ctx)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("connect to Soulfind: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	if err := client.Login(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(runCtx) }()
	t.Cleanup(func() {
		stop()
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("peer run loop did not stop")
		}
	})
}

func freeListenAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func TestSoulfindIPCSearchAndDownload(t *testing.T) {
	address := os.Getenv("OTO_SOULFIND_ADDR")
	if address == "" {
		t.Skip("OTO_SOULFIND_ADDR is unset")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stamp := fmt.Sprintf("%x", time.Now().UnixNano())
	filename := "vertical-" + stamp + ".flac"
	contents := bytes.Repeat([]byte("ipc to daemon to Soulfind\n"), 256)
	peerUser := "p" + stamp
	startIPCSoulfindPeer(t, address, peerUser, filename, contents)

	downloadRoot := t.TempDir()
	cfg := config.Default()
	cfg.Soulseek.Server = address
	cfg.Soulseek.Username, cfg.Soulseek.Password = "i"+stamp, "pw"
	cfg.Soulseek.ListenAddr = freeListenAddress(t)
	cfg.Soulseek.NATPMPPortMapping, cfg.Soulseek.UPnPPortMapping = false, false
	cfg.DownloadDir = downloadRoot
	service, err := daemon.New(cfg, filepath.Join(t.TempDir(), "downloads.json"))
	if err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(t.TempDir(), "oto.sock")
	server := NewServer(service, socketPath)
	runCtx, stop := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(runCtx) }()
	if err := service.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop()
		_ = server.Close()
		_ = service.Close()
		select {
		case <-serverDone:
		case <-time.After(5 * time.Second):
			t.Error("IPC server did not stop")
		}
	})

	client := NewClient(socketPath)
	deadline := time.Now().Add(15 * time.Second)
	for {
		status, err := client.Status(context.Background())
		if err == nil && status.Status == daemon.StatusConnected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not connect through IPC: %+v %v", status, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	page, err := client.Search(ctx, filename, "")
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	var result daemon.SearchResult
	for _, candidate := range page.Results {
		if candidate.Username == peerUser && candidate.Path == "Music\\"+filename {
			result = candidate
			break
		}
	}
	if result.Username == "" || result.Size != uint64(len(contents)) {
		t.Fatalf("search result: %+v", page.Results)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	queued, err := client.QueueDownloads(ctx, []daemon.DownloadRequest{{Username: result.Username, Files: []daemon.DownloadItem{{Filename: result.Path, Size: result.Size}}}})
	cancel()
	if err != nil || len(queued) != 1 {
		t.Fatalf("queue download: %+v %v", queued, err)
	}
	deadline = time.Now().Add(15 * time.Second)
	for {
		transfers, err := client.Transfers(context.Background())
		if err == nil {
			for _, transfer := range transfers {
				if transfer.ID == queued[0].ID && transfer.State == "completed" {
					path := filepath.Join(downloadRoot, result.Username, "Music", filename)
					got, readErr := os.ReadFile(path)
					if readErr != nil || !bytes.Equal(got, contents) {
						t.Fatalf("downloaded file: bytes=%d err=%v", len(got), readErr)
					}
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("download did not complete: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
