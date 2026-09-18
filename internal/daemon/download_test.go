package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
)

func TestFinalizePartRenamesCollision(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "u", "p"
	cfg.DownloadDir = t.TempDir()
	service, err := New(cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
	must(t, err)
	must(t, os.MkdirAll(filepath.Join(cfg.DownloadDir, "peer"), 0700))
	must(t, os.WriteFile(filepath.Join(cfg.DownloadDir, "peer", "a.txt"), []byte("old"), 0600))
	part := filepath.Join(t.TempDir(), "d-1.part")
	must(t, os.WriteFile(part, []byte("new"), 0600))
	target, err := service.finalizePart(cfg.DownloadDir, part, "peer/a.txt", "d-1")
	must(t, err)
	failIfFmt(t, !strings.HasSuffix(target, "a (1).txt"), "collision target %s", target)
	got, err := os.ReadFile(target)
	failIfFmt(t, err != nil || string(got) != "new", "final content %q %v", got, err)
}

func TestSafeSegment(t *testing.T) {
	if got := safeSegment("../../peer/name"); strings.ContainsAny(got, "/\\") || got == "." || got == ".." {
		t.Fatalf("unsafe segment %q", got)
	}
}
