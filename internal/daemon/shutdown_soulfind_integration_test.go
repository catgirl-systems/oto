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
)

// Real Soulfind discovery and oto services, not a mocked server or peer.
func TestSoulfindDaemonUploadShutdown(t *testing.T) {
	address := os.Getenv("OTO_SOULFIND_ADDR")
	if address == "" {
		t.Skip("OTO_SOULFIND_ADDR is unset")
	}
	for _, mode := range []string{"default", "finish", "force"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			stamp := fmt.Sprintf("%x", time.Now().UnixNano())
			cfg := config.Default()
			cfg.Soulseek.Server = address
			cfg.Soulseek.Username, cfg.Soulseek.Password = "up"+stamp, "pw"
			_, port, _ := net.SplitHostPort(closedAddress(t))
			cfg.Soulseek.ListenAddr = net.JoinHostPort("0.0.0.0", port)
			cfg.Soulseek.NATPMPPortMapping, cfg.Soulseek.UPnPPortMapping = false, false
			cfg.AudioMetadata = false
			cfg.UploadSlots = 1
			cfg.Uploads.WaitForActiveUploadsOnQuit = mode != "default"
			cfg.Bandwidth.Profiles[0].UploadSpeedLimitKiB = 32
			cfg.DownloadDir = t.TempDir()
			root := t.TempDir()
			cfg.Shares = []config.Share{{Name: "Music", Path: root}}
			contents := bytes.Repeat([]byte("Soulfind shutdown\n"), 16384)
			for _, name := range []string{"active.bin", "queued.bin"} {
				if err := os.WriteFile(filepath.Join(root, name), contents, 0600); err != nil {
					t.Fatal(err)
				}
			}
			journal := filepath.Join(t.TempDir(), "state.sqlite3")
			uploader, err := New(cfg, journal)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = uploader.Close() })
			if err := uploader.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			waitForIntegration(t, func() bool { return uploader.Snapshot().Status == StatusConnected })
			receiverConfig := cfg
			receiverConfig.Soulseek.Username = "down" + stamp
			_, port, _ = net.SplitHostPort(closedAddress(t))
			receiverConfig.Soulseek.ListenAddr = net.JoinHostPort("0.0.0.0", port)
			receiverConfig.Shares = nil
			receiverConfig.DownloadDir, receiverConfig.DownloadSlots = t.TempDir(), 2
			downloader, err := New(receiverConfig, filepath.Join(t.TempDir(), "state.sqlite3"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = downloader.Close() })
			if err := downloader.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			waitForIntegration(t, func() bool { return downloader.Snapshot().Status == StatusConnected })
			t.Cleanup(func() {
				if t.Failed() {
					t.Logf("uploader status=%s uploads=%+v; downloader status=%s downloads=%+v transfers=%+v", uploader.Snapshot().Status, uploader.Transfers(), downloader.Snapshot().Status, downloader.Downloads(), downloader.Transfers())
				}
			})
			downloads, err := downloader.QueueDownloads([]DownloadRequest{{Username: cfg.Soulseek.Username, Files: []DownloadItem{{Filename: `Music\active.bin`, Size: uint64(len(contents))}}}})
			if err != nil {
				t.Fatal(err)
			}
			activeID := downloads[0].ID
			waitForIntegration(t, func() bool {
				tr := integrationTransfer(downloader, activeID)
				return tr.State == "running" && tr.Done > 0 && tr.Done < tr.Total
			})
			// A second peer is necessary: oto serializes downloads from one user.
			queuedPeer := startIntegrationUploader(t, address, "queue"+stamp, nil, 0)
			queuedFile, err := os.CreateTemp(t.TempDir(), "queued")
			if err != nil {
				t.Fatal(err)
			}
			transferCtx, cancelTransfer := context.WithCancel(context.Background())
			queuedDone := make(chan error, 1)
			go func() {
				queuedDone <- queuedPeer.client.Download(transferCtx, cfg.Soulseek.Username, `Music\queued.bin`, uint64(len(contents)), 0, queuedFile, nil)
			}()
			t.Cleanup(func() {
				cancelTransfer()
				select {
				case <-queuedDone:
				case <-time.After(5 * time.Second):
					t.Error("queued download did not stop")
				}
				queuedFile.Close()
			})
			waitForIntegration(t, func() bool {
				for _, tr := range uploader.Transfers() {
					if tr.Filename == `Music\queued.bin` && tr.State == "queued" {
						return true
					}
				}
				return false
			})
			ctx, force := context.WithCancel(context.Background())
			defer force()
			done := make(chan struct{})
			go func() { uploader.WaitForUploads(ctx); close(done) }()
			if mode != "default" {
				waitForIntegration(t, func() bool {
					st := uploader.Snapshot().Shutdown
					return st != nil && st.Draining && st.ActiveUploads == 1
				})
				if mode == "force" {
					force()
				} else {
					select {
					case <-done:
						t.Fatal("shutdown interrupted the real transfer")
					case <-time.After(3300 * time.Millisecond):
					}
				}
			}
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Fatal("upload drain did not finish")
			}
			if mode == "finish" {
				waitForIntegration(t, func() bool { return integrationDownload(downloader, activeID).State == "completed" })
				download := integrationDownload(downloader, activeID)
				got, err := os.ReadFile(filepath.Join(download.DownloadDir, download.Destination))
				if err != nil || !bytes.Equal(got, contents) {
					t.Fatalf("completed download differs: %d bytes, %v", len(got), err)
				}
			}
			if info, err := queuedFile.Stat(); err != nil || info.Size() != 0 {
				t.Fatalf("queued upload wrote data during drain: %v %v", info, err)
			}
			_ = uploader.Close()
			_ = downloader.Close()
			_ = queuedPeer.client.Close()
			reopened, err := New(cfg, journal)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if len(reopened.journal.Uploads) != 2 {
				t.Fatalf("lost upload history: %+v", reopened.journal.Uploads)
			}
			for _, upload := range reopened.journal.Uploads {
				complete := mode == "finish" && upload.Filename == `Music\active.bin`
				if complete && (upload.State != "completed" || upload.Recoverable) || !complete && (upload.State != "interrupted" || !upload.Recoverable) {
					t.Fatalf("incorrect persisted upload: %+v", upload)
				}
			}
		})
	}
}
