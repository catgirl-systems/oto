package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
)

func TestBandwidthSaveDuringConnect(t *testing.T) {
	server, observations := startMappingServer(t, false, nil)
	s, ctx := prepareConnectOnce(t, server, true, true)
	s.SetConfigPath(filepath.Join(t.TempDir(), "config.json"))
	entered, release := make(chan struct{}), make(chan struct{})
	s.portMapOpen = func(ctx context.Context, _ uint16, _, _ bool, _ func(uint16)) (portMapping, error) {
		close(entered)
		select {
		case <-release:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	done := make(chan error, 1)
	go func() { done <- s.connectOnce(ctx) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("connect did not reach mapping")
	}
	next := s.cfg
	next.Bandwidth = config.Bandwidth{ActiveProfile: "During login", Profiles: []config.BandwidthProfile{{Name: "During login", UploadSpeedLimitKiB: 7, DownloadSpeedLimitKiB: 11}}}
	next.Uploads.SlotBandwidthKiB = 64
	must(t, s.UpdateConfig(next))
	close(release)
	select {
	case err := <-done:
		must(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("connect did not finish")
	}
	awaitMappingObservation(t, observations)
	failIf(t, s.client.DownloadLimit() != 11*1024 || s.client.UploadPolicy().BytesPerSecond != 7*1024 || s.client.UploadPolicy().SlotBandwidthBytesPerSecond != 64*1024, "published client used stale bandwidth config")
	loaded, err := config.Load(s.configPath)
	must(t, err)
	// The next daemon/client lifecycle derives the same two rates from disk.
	restarted, err := New(loaded, filepath.Join(t.TempDir(), "state.sqlite3"))
	must(t, err)
	defer restarted.Close()
	failIf(t, downloadLimit(restarted.cfg) != 11*1024 || newUploadManager(restarted.cfg).Policy().BytesPerSecond != 7*1024 || newUploadManager(restarted.cfg).Policy().SlotBandwidthBytesPerSecond != 64*1024, "restart lost bandwidth settings")
}
