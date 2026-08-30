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
