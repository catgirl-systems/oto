package daemon

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

type integrationUploader struct {
	username string
	client   *soulseek.Client
	uploads  *soulseek.UploadManager
}

func startIntegrationUploader(t *testing.T, address, username string, files map[string][]byte, bytesPerSecond int64) *integrationUploader {
	t.Helper()
	root := t.TempDir()
	for name, contents := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	shares := soulseek.NewShareIndex()
	if err := shares.AddRoot("Music", root); err != nil {
		t.Fatal(err)
	}
	if err := shares.ScanContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	uploads := soulseek.NewUploadManager(1)
	uploads.Configure(soulseek.UploadPolicy{Scheduling: soulseek.UploadScheduleFIFO, BytesPerSecond: bytesPerSecond})
	client := soulseek.NewClient(soulseek.ClientConfig{Address: address, Username: username, Password: "pw", ListenAddr: "0.0.0.0:0", Share: shares, Uploads: uploads})
	connectIntegrationClient(t, client)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("uploader run loop did not stop")
		}
	})
	return &integrationUploader{username: username, client: client, uploads: uploads}
}

func startIntegrationDownloadService(t *testing.T, address, username, downloadRoot, journalPath string, slots int) *Service {
	t.Helper()
	cfg := config.Default()
	cfg.Soulseek.Server = address
	cfg.Soulseek.Username, cfg.Soulseek.Password = username, "pw"
	cfg.Soulseek.ListenAddr = closedAddress(t)
	cfg.Soulseek.NATPMPPortMapping, cfg.Soulseek.UPnPPortMapping = false, false
	cfg.DownloadDir, cfg.DownloadSlots = downloadRoot, slots
	service, err := New(cfg, journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForIntegration(t, func() bool { return service.Snapshot().Status == StatusConnected })
	return service
}

func integrationDownload(service *Service, id string) Download {
	for _, download := range service.Downloads() {
		if download.ID == id {
			return download
		}
	}
	return Download{}
}

func integrationTransfer(service *Service, id string) Transfer {
	for _, transfer := range service.Transfers() {
		if transfer.ID == id {
			return transfer
		}
	}
	return Transfer{}
}

func waitForIntegration(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not satisfied")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSoulfindDaemonDownloadLifecycle(t *testing.T) {
	address := os.Getenv("OTO_SOULFIND_ADDR")
	if address == "" {
		t.Skip("OTO_SOULFIND_ADDR is unset")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stamp := fmt.Sprintf("%x", time.Now().UnixNano())
	firstName, secondName := "lifecycle-"+stamp+".flac", "next-"+stamp+".flac"
	firstContents := bytes.Repeat([]byte("daemon lifecycle\n"), 8192)
	secondContents := []byte("second download")
	firstPeer := startIntegrationUploader(t, address, "a"+stamp, map[string][]byte{firstName: firstContents}, 32<<10)
	secondPeer := startIntegrationUploader(t, address, "b"+stamp, map[string][]byte{secondName: secondContents}, 0)
	downloadRoot, journalPath := t.TempDir(), filepath.Join(t.TempDir(), "state.sqlite3")
	service := startIntegrationDownloadService(t, address, "c"+stamp, downloadRoot, journalPath, 1)
	t.Cleanup(func() { _ = service.Close() })

	original := filepath.Join(downloadRoot, firstPeer.username, "Music", firstName)
	if err := os.MkdirAll(filepath.Dir(original), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := service.QueueDownloads([]DownloadRequest{{Username: firstPeer.username, Files: []DownloadItem{{Filename: "Music/" + firstName, Size: uint64(len(firstContents))}}}})
	if err != nil {
		t.Fatal(err)
	}
	firstID := first[0].ID
	waitForIntegration(t, func() bool {
		transfer := integrationTransfer(service, firstID)
		stat, err := os.Stat(incompletePath(firstID))
		return transfer.State == "running" && transfer.Done > 0 && err == nil && stat.Size() > 0
	})

	rows, err := service.stateDB.Queries().ListDownloads(context.Background())
	if err != nil || len(rows) != 1 || rows[0].ID != firstID {
		t.Fatalf("queued database rows: %+v %v", rows, err)
	}
	second, err := service.QueueDownloads([]DownloadRequest{{Username: secondPeer.username, Files: []DownloadItem{{Filename: "Music/" + secondName, Size: uint64(len(secondContents))}}}})
	if err != nil {
		t.Fatal(err)
	}
	waitForIntegration(t, func() bool {
		service.mu.RLock()
		defer service.mu.RUnlock()
		_, started := service.downloadCancels[second[0].ID]
		return started
	})
	if download := integrationDownload(service, second[0].ID); download.State != "queued" {
		t.Fatalf("second download bypassed slot: %+v", download)
	}
	if _, err := os.Stat(incompletePath(second[0].ID)); !os.IsNotExist(err) {
		t.Fatalf("queued download created part file: %v", err)
	}

	if err := service.TransferAction(firstID, "cancel"); err != nil {
		t.Fatal(err)
	}
	waitForIntegration(t, func() bool { return integrationDownload(service, firstID).State == "cancelled" })
	waitForIntegration(t, func() bool { return integrationDownload(service, second[0].ID).State == "completed" })
	firstPeer.uploads.Configure(soulseek.UploadPolicy{Scheduling: soulseek.UploadScheduleFIFO})
	if err := service.TransferAction(firstID, "retry"); err != nil {
		t.Fatal(err)
	}
	waitForIntegration(t, func() bool { return integrationDownload(service, firstID).State == "completed" })

	if got, err := os.ReadFile(original); err != nil || string(got) != "existing" {
		t.Fatalf("collision source: %q %v", got, err)
	}
	completed := filepath.Join(downloadRoot, firstPeer.username, "Music", "lifecycle-"+stamp+" (1).flac")
	if got, err := os.ReadFile(completed); err != nil || !bytes.Equal(got, firstContents) {
		t.Fatalf("completed collision download: bytes=%d err=%v", len(got), err)
	}
	transfer := integrationTransfer(service, firstID)
	if transfer.State != "completed" || transfer.Done != uint64(len(firstContents)) || transfer.Total != uint64(len(firstContents)) {
		t.Fatalf("completed progress: %+v", transfer)
	}
	if err := service.TransferAction(firstID, "clear"); err != nil {
		t.Fatal(err)
	}
	if download := integrationDownload(service, firstID); download.ID != "" {
		t.Fatalf("cleared download remains: %+v", download)
	}
	if transfer := integrationTransfer(service, firstID); transfer.ID != "" {
		t.Fatalf("cleared transfer remains: %+v", transfer)
	}
	if _, err := os.Stat(incompletePath(firstID)); !os.IsNotExist(err) {
		t.Fatalf("cleared part remains: %v", err)
	}
}

func TestSoulfindDaemonRestartsAndResumesDownload(t *testing.T) {
	address := os.Getenv("OTO_SOULFIND_ADDR")
	if address == "" {
		t.Skip("OTO_SOULFIND_ADDR is unset")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stamp := fmt.Sprintf("%x", time.Now().UnixNano())
	filename := "restart-" + stamp + ".flac"
	contents := bytes.Repeat([]byte("restart resume\n"), 8192)
	peer := startIntegrationUploader(t, address, "r"+stamp, map[string][]byte{filename: contents}, 32<<10)
	downloadRoot, journalPath := t.TempDir(), filepath.Join(t.TempDir(), "state.sqlite3")
	username := "s" + stamp
	service := startIntegrationDownloadService(t, address, username, downloadRoot, journalPath, 2)
	downloads, err := service.QueueDownloads([]DownloadRequest{{Username: peer.username, Files: []DownloadItem{{Filename: "Music/" + filename, Size: uint64(len(contents))}}}})
	if err != nil {
		t.Fatal(err)
	}
	id := downloads[0].ID
	part := incompletePath(id)
	waitForIntegration(t, func() bool {
		stat, err := os.Stat(part)
		return err == nil && stat.Size() > 0
	})
	before, err := os.Stat(part)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if download := integrationDownload(service, id); download.State != "queued" || download.Offset < uint64(before.Size()) {
		t.Fatalf("shutdown journal state: %+v", download)
	}

	peer.uploads.Configure(soulseek.UploadPolicy{Scheduling: soulseek.UploadScheduleFIFO})
	restarted := startIntegrationDownloadService(t, address, username, downloadRoot, journalPath, 2)
	t.Cleanup(func() { _ = restarted.Close() })
	waitForIntegration(t, func() bool { return integrationDownload(restarted, id).State == "completed" })
	completed := filepath.Join(downloadRoot, peer.username, "Music", filename)
	if got, err := os.ReadFile(completed); err != nil || !bytes.Equal(got, contents) {
		t.Fatalf("resumed download: bytes=%d err=%v", len(got), err)
	}
}
