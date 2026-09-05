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
)

func TestSoulfindDaemonUploadControls(t *testing.T) {
	address := os.Getenv("OTO_SOULFIND_ADDR")
	if address == "" {
		t.Skip("OTO_SOULFIND_ADDR is unset; local Soulfind integration is opt-in")
	}
	stamp := fmt.Sprintf("%x", time.Now().UnixNano())
	receiver := startIntegrationUploader(t, address, "recv"+stamp, nil, 0)
	root := t.TempDir()
	contents := bytes.Repeat([]byte("upload"), 4096)
	if err := os.WriteFile(filepath.Join(root, "song"), contents, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Soulseek.Server = address
	cfg.Soulseek.Username = "send" + stamp
	cfg.Soulseek.Password = "pw"
	cfg.Soulseek.ListenAddr = closedAddress(t)
	cfg.Soulseek.NATPMPPortMapping, cfg.Soulseek.UPnPPortMapping = false, false
	cfg.Shares = []config.Share{{Name: "Music", Path: root}}
	cfg.UploadSlots = 1
	cfg.Bandwidth.Profiles[0].UploadSpeedLimitKiB = 1
	svc, err := New(cfg, filepath.Join(t.TempDir(), "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForIntegration(t, func() bool { return svc.Snapshot().Status == StatusConnected })
	file, err := os.CreateTemp(t.TempDir(), "received")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- receiver.client.Download(ctx, cfg.Soulseek.Username, `Music\song`, uint64(len(contents)), 0, file, nil)
	}()
	id := uploadID(receiver.username, `Music\song`)
	waitForIntegration(t, func() bool { tr := integrationTransfer(svc, id); return tr.State == "running" && tr.Done > 0 })
	result, err := svc.UploadAction(UploadActionRequest{Action: "cancel", Usernames: []string{receiver.username}})
	if err != nil || result.Changed != 1 {
		t.Fatalf("abort user %+v %v", result, err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("download not aborted")
		}
	case <-ctx.Done():
		t.Fatal("abort did not close file connection")
	}
	result, err = svc.UploadAction(UploadActionRequest{Action: "retry", IDs: []string{id}})
	if err != nil || result.Changed != 1 {
		t.Fatalf("retry %+v %v", result, err)
	}
	// The receiver has cancelled its request; the retry must record its rejection.
	waitForIntegration(t, func() bool { return integrationTransfer(svc, id).State == "failed" })
	result, err = svc.UploadAction(UploadActionRequest{Action: "clear", States: []string{"failed"}})
	if err != nil || result.Changed != 1 || len(svc.Transfers()) != 0 {
		t.Fatalf("clear failed %+v %v", result, err)
	}
}
