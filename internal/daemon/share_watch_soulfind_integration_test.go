package daemon

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
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestSoulfindShareWatcherPublishesWithoutReconnect(t *testing.T) {
	address := os.Getenv("OTO_SOULFIND_ADDR")
	if address == "" {
		t.Skip("OTO_SOULFIND_ADDR is unset")
	}

	stamp := fmt.Sprintf("%x", time.Now().UnixNano())
	targetUser, observerUser := "h"+stamp, "k"+stamp
	root := t.TempDir()
	cfg := config.Default()
	cfg.Soulseek.Server = address
	cfg.Soulseek.Username, cfg.Soulseek.Password = targetUser, "pw"
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	initialPort := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	cfg.Soulseek.ListenAddr = "0.0.0.0:50300"
	portFile := filepath.Join(t.TempDir(), "forwarded-port")
	if err := os.WriteFile(portFile, []byte(fmt.Sprint(initialPort)), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.DownloadDir = t.TempDir()
	cfg.Shares = []config.Share{{Name: "Music", Path: root}}

	service, err := New(cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetShareRescanDelay(50 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := service.SetListenPortFile(portFile, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	serviceCtx, stopService := context.WithCancel(context.Background())
	_ = service.Start(serviceCtx)
	t.Cleanup(func() {
		stopService()
		_ = service.Close()
	})
	waitFor(t, func() bool { return service.Snapshot().Status == StatusConnected })
	service.mu.RLock()
	connectedClient := service.client
	service.mu.RUnlock()

	observer := soulseek.NewClient(soulseek.ClientConfig{Address: address, Username: observerUser, Password: "pw", ListenAddr: "0.0.0.0:0"})
	t.Cleanup(func() { _ = observer.Close() })
	connectIntegrationClient(t, observer)
	observerCtx, stopObserver := context.WithCancel(context.Background())
	t.Cleanup(stopObserver)
	observerDone := make(chan error, 1)
	go func() { observerDone <- observer.Run(observerCtx) }()

	replacementListener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	replacementPort := replacementListener.Addr().(*net.TCPAddr).Port
	_ = replacementListener.Close()
	if err := os.WriteFile(portFile, []byte(fmt.Sprint(replacementPort)), 0600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		service.mu.RLock()
		defer service.mu.RUnlock()
		return service.listenPort == uint16(replacementPort)
	})
	service.mu.RLock()
	reboundWithoutReconnect := service.client == connectedClient
	service.mu.RUnlock()
	if !reboundWithoutReconnect {
		t.Fatal("listen port update reconnected the Soulseek client")
	}

	contents := []byte("published after fsnotify reindex")
	directory := filepath.Join(root, "Dynamic")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	filename := "watch-" + stamp + ".flac"
	if err := os.WriteFile(filepath.Join(directory, filename), contents, 0600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return hasLocalFile(service, "Music/Dynamic", filename, int64(len(contents))) })
	service.mu.RLock()
	stillConnected := service.client == connectedClient
	service.mu.RUnlock()
	if !stillConnected {
		t.Fatal("share reindex reconnected the Soulseek client")
	}
	// Share counts and distributed-search routing use separate server connections.
	time.Sleep(250 * time.Millisecond)

	remotePath := "Music\\Dynamic\\" + filename
	browseCtx, cancelBrowse := context.WithTimeout(context.Background(), 10*time.Second)
	entries, err := observer.BrowseUser(browseCtx, targetUser, "Music/Dynamic")
	cancelBrowse()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.Name == remotePath && entry.Size == uint64(len(contents)) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("updated file absent from remote browse: %+v", entries)
	}

	searchCtx, cancelSearch := context.WithTimeout(context.Background(), 8*time.Second)
	results, err := observer.Search(searchCtx, filename)
	cancelSearch()
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, result := range results {
		if result.Path == remotePath && result.Size == uint64(len(contents)) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("updated file absent from remote search: %+v", results)
	}

	destination, err := os.CreateTemp(t.TempDir(), "watch-download-")
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	downloadCtx, cancelDownload := context.WithTimeout(context.Background(), 15*time.Second)
	err = observer.Download(downloadCtx, targetUser, remotePath, uint64(len(contents)), 0, destination, nil)
	cancelDownload()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination.Name())
	if err != nil || !bytes.Equal(got, contents) {
		t.Fatalf("downloaded reindexed file: %q %v", got, err)
	}

	stopObserver()
	_ = observer.Close()
	select {
	case <-observerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("observer run loop did not stop")
	}
}

func connectIntegrationClient(t *testing.T, client *soulseek.Client) {
	t.Helper()
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
	defer cancel()
	if err := client.Login(ctx); err != nil {
		t.Fatalf("login to Soulfind: %v", err)
	}
}
