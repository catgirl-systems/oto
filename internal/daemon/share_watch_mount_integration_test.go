package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestBindMountedShareWatcher(t *testing.T) {
	root := os.Getenv("OTO_SHARE_WATCH_ROOT")
	if root == "" {
		t.Skip("OTO_SHARE_WATCH_ROOT is unset")
	}
	var scans atomic.Int32
	builder := func(ctx context.Context, shares []config.Share) (*soulseek.ShareIndex, error) {
		scans.Add(1)
		return buildShareIndex(ctx, shares)
	}
	service := watchingService(t, []config.Share{{Name: "Music", Path: root}}, 50*time.Millisecond, builder)
	waitFor(t, func() bool { return scans.Load() >= 1 })
	if err := os.WriteFile(filepath.Join(root, ".oto-watch-ready"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return hasLocalFile(service, "Music", "host-created.flac", 19) })
}
