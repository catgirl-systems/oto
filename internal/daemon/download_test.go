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
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.DownloadDir, "peer"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.DownloadDir, "peer", "a.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(t.TempDir(), "d-1.part")
	if err := os.WriteFile(part, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	target, err := service.finalizePart(cfg.DownloadDir, part, "peer/a.txt", "d-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(target, "a (1).txt") {
		t.Fatalf("collision target %s", target)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "new" {
		t.Fatalf("final content %q %v", got, err)
	}
}

func TestSafeSegment(t *testing.T) {
	if got := safeSegment("../../peer/name"); strings.ContainsAny(got, "/\\") || got == "." || got == ".." {
		t.Fatalf("unsafe segment %q", got)
	}
}

func TestDownloadPeerSlotsSerializeSameUser(t *testing.T) {
	service := &Service{downloadPeers: make(map[string]chan struct{})}
	first := service.downloadPeerSlot("peer")
	if first != service.downloadPeerSlot("peer") || first == service.downloadPeerSlot("other") {
		t.Fatal("download peer slots were not isolated by username")
	}
	first <- struct{}{}
	select {
	case first <- struct{}{}:
		t.Fatal("same-user download slot allowed a concurrent transfer")
	default:
	}
	<-first
}
