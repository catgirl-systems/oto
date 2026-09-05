package ipc

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"

	"github.com/catgirl-systems/oto/internal/config"
	"path/filepath"
	"testing"
	"time"
)

func TestRescanRequestTimeoutIsolation(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "socket")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/shares/rescan" {
			<-r.Context().Done()
			return
		}
		entered <- struct{}{}
		select {
		case <-release:
			w.Write([]byte("[]"))
		case <-r.Context().Done():
		}
	})}
	defer server.Close()
	go server.Serve(listener)
	client := NewClient(socket)
	client.http.Timeout = 10 * time.Millisecond
	if _, err := client.Status(context.Background()); err == nil {
		t.Fatal("ordinary timeout was disabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.Rescan(ctx); done <- err }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("rescan did not reach handler: %v", err)
	case <-ctx.Done():
		t.Fatal("rescan did not reach handler")
	}
	select {
	case err := <-done:
		t.Fatalf("rescan used ordinary timeout: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if client.http.Timeout != 10*time.Millisecond {
		t.Fatal("shared client timeout was mutated")
	}
	cancel()
	if _, err := client.Rescan(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("rescan context ignored: %v", err)
	}
}

func TestSettingsRequestTimeoutIsolation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/config" {
			<-r.Context().Done()
			return
		}
		time.Sleep(40 * time.Millisecond)
		w.Write([]byte("{}"))
	}))
	defer server.Close()
	client := NewClient("")
	client.http.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}
	client.http.Timeout = 10 * time.Millisecond
	if _, err := client.UpdateConfig(context.Background(), config.Default()); err != nil {
		t.Fatal(err)
	}
	if client.http.Timeout != 10*time.Millisecond {
		t.Fatal("shared timeout changed")
	}
	if _, err := client.Status(context.Background()); err == nil {
		t.Fatal("interactive timeout lost")
	}
}
