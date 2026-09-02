package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStartChildStopsWhenContextIsCancelled(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	script := filepath.Join(t.TempDir(), "child")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	old := executable
	executable = func() (string, error) { return script, nil }
	t.Cleanup(func() { executable = old })

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	started := time.Now()
	cmd, keepAlive, err := startChild(ctx, filepath.Join(t.TempDir(), "config.json"))
	if cmd != nil || keepAlive != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("startChild = (%v, %v, %v)", cmd, keepAlive, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancel took %v", elapsed)
	}
}

func TestParseDaemonShareRescanDelay(t *testing.T) {
	options, err := parseDaemonOptions(nil)
	if err != nil || options.shareScanDelay != 5*time.Minute {
		t.Fatalf("default delay: %v %v", options.shareScanDelay, err)
	}
	options, err = parseDaemonOptions([]string{"--share-rescan-delay", "0"})
	if err != nil || options.shareScanDelay != 0 {
		t.Fatalf("disabled delay: %v %v", options.shareScanDelay, err)
	}
	for _, args := range [][]string{{"--share-rescan-delay", "-1s"}, {"--share-rescan-delay", "later"}} {
		if _, err := parseDaemonOptions(args); err == nil {
			t.Fatalf("accepted invalid delay %q", args[1])
		}
	}
}
