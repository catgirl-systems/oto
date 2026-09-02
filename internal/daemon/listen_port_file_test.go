package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type listenPortUpdate struct {
	port      uint16
	available bool
}

func TestWatchListenPortFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forwarded-port")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := make(chan listenPortUpdate, 16)
	go watchListenPortFile(ctx, path, time.Hour, func(port uint16, available bool) {
		updates <- listenPortUpdate{port: port, available: available}
	})
	waitForListenPort(t, updates, listenPortUpdate{})

	if err := os.WriteFile(path, []byte("43001\n"), 0600); err != nil {
		t.Fatal(err)
	}
	waitForListenPort(t, updates, listenPortUpdate{port: 43001, available: true})

	replacement := path + ".new"
	if err := os.WriteFile(replacement, []byte("43002\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	waitForListenPort(t, updates, listenPortUpdate{port: 43002, available: true})

	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitForListenPort(t, updates, listenPortUpdate{})
}

func TestListenPortFileFallsBackToConfiguredReconciliation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "later", "forwarded-port")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := make(chan listenPortUpdate, 16)
	go watchListenPortFile(ctx, path, 20*time.Millisecond, func(port uint16, available bool) {
		updates <- listenPortUpdate{port: port, available: available}
	})
	waitForListenPort(t, updates, listenPortUpdate{})
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("44001"), 0600); err != nil {
		t.Fatal(err)
	}
	waitForListenPort(t, updates, listenPortUpdate{port: 44001, available: true})
}

func TestReadListenPortFileRejectsMalformedPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "port")
	for _, value := range []string{"later", "80", "65536", "40000\n40001"} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readListenPortFile(path); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func waitForListenPort(t *testing.T, updates <-chan listenPortUpdate, want listenPortUpdate) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case got := <-updates:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("did not receive listen port update %+v", want)
		}
	}
}
