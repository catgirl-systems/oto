package ipc

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catgirl-systems/slsk-tui/internal/config"
	"github.com/catgirl-systems/slsk-tui/internal/daemon"
)

func TestStatusMethodsBodyAndSocketMode(t *testing.T) {
	c := config.Default()
	c.Soulseek.Username, c.Soulseek.Password = "u", "p"
	c.DownloadDir = t.TempDir()
	svc, err := daemon.NewWithJournal(c, filepath.Join(t.TempDir(), "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	path := filepath.Join(t.TempDir(), "run", "slsk.sock")
	srv := NewServer(svc, path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	var snap daemon.Snapshot
	cl := NewClient(path)
	for i := 0; i < 50; i++ {
		if err := cl.Do(context.Background(), "GET", "/v1/state", nil, &snap); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if snap.Config.Soulseek.Username != "u" {
		t.Fatalf("snapshot: %+v", snap)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("socket mode: %v %v", st, err)
	}
	resp, err := cl.http.Do(mustRequest("POST", "http://slsk-tui.local/v1/state", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 405 {
		t.Fatalf("method status %d", resp.StatusCode)
	}
	resp.Body.Close()
	big := strings.Repeat("x", int(MaxBodySize)+1)
	resp, err = cl.http.Do(mustRequest("POST", "http://slsk-tui.local/v1/downloads", strings.NewReader(big)))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Fatalf("body status %d", resp.StatusCode)
	}
	resp.Body.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}
func mustRequest(method, path string, body io.Reader) *http.Request {
	r, _ := http.NewRequest(method, path, body)
	return r
}
func TestStaleSocketIsRemovedAfterFailedDial(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "slsk.sock")
	if err := os.WriteFile(p, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	c := config.Default()
	c.Soulseek.Username, c.Soulseek.Password = "u", "p"
	c.DownloadDir = t.TempDir()
	svc, _ := daemon.NewWithJournal(c, filepath.Join(d, "j"))
	defer svc.Close()
	srv := NewServer(svc, p)
	ln, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	os.Remove(p)
}
