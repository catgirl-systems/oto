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
	must(t, os.WriteFile(filepath.Join(root, filename), contents, 0600))
	shares := soulseek.NewShareIndex()
	must(t, shares.AddRoot("Music", root))
	must(t, shares.ScanContext(context.Background()))
	client := soulseek.NewClient(soulseek.ClientConfig{Address: address, Username: username, Password: "pw", ListenAddr: "0.0.0.0:0", Share: shares})
	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := client.Connect(ctx)
		cancel()
		if err == nil {
			break
		}
		failIfFmt(t, time.Now().After(deadline), "connect to Soulfind: %v", err)
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
	must(t, err)
	address := listener.Addr().String()
	must(t, listener.Close())
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
	service, err := daemon.New(cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
	must(t, err)
	socketPath := filepath.Join(t.TempDir(), "oto.sock")
	server := NewServer(service, socketPath)
	runCtx, stop := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(runCtx) }()
	must(t, service.Start(runCtx))
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
		failIfFmt(t, time.Now().After(deadline), "daemon did not connect through IPC: %+v %v", status, err)
		time.Sleep(20 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	page, err := client.Search(ctx, filename, "")
	cancel()
	must(t, err)
	var result daemon.SearchResult
	for _, candidate := range page.Results {
		if candidate.Username == peerUser && candidate.Path == "Music\\"+filename {
			result = candidate
			break
		}
	}
	failIfFmt(t, result.Username == "" || result.Size != uint64(len(contents)), "search result: %+v", page.Results)

	ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	queued, err := client.QueueDownloads(ctx, []daemon.DownloadRequest{{Username: result.Username, Files: []daemon.DownloadItem{{Filename: result.Path, Size: result.Size}}}})
	cancel()
	failIfFmt(t, err != nil || len(queued) != 1, "queue download: %+v %v", queued, err)
	deadline = time.Now().Add(15 * time.Second)
	for {
		transfers, err := client.Transfers(context.Background())
		if err == nil {
			for _, transfer := range transfers {
				if transfer.ID == queued[0].ID && transfer.State == "completed" {
					path := filepath.Join(downloadRoot, result.Username, "Music", filename)
					got, readErr := os.ReadFile(path)
					failIfFmt(t, readErr != nil || !bytes.Equal(got, contents), "downloaded file: bytes=%d err=%v", len(got), readErr)
					return
				}
			}
		}
		failIfFmt(t, time.Now().After(deadline), "download did not complete: %v", err)
		time.Sleep(20 * time.Millisecond)
	}
}
