package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestChildStartupCaptureBounded(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	os.MkdirAll(config.DataDir(), 0700)
	legacy := filepath.Join(config.DataDir(), "daemon.log")
	os.WriteFile(legacy, []byte("untouched"), 0600)
	script := filepath.Join(t.TempDir(), "startup-child")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec 1>&2\nprintf '%040000d' 0\nprintf '\\n{\"level\":\"ERROR\",\"msg\":\"startup_fixture_failed\"}\\n'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	old := executable
	executable = func() (string, error) { return script, nil }
	defer func() { executable = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	child, input, err := startChild(ctx, "unused")
	if err == nil || child != nil || input != nil || !strings.Contains(err.Error(), "startup_fixture_failed") || len(err.Error()) > 33<<10 {
		t.Fatalf("startup capture: %v", err)
	}
	if b, _ := os.ReadFile(legacy); string(b) != "untouched" {
		t.Fatal("legacy log was changed")
	}
}
func TestTailWriterRetainsOnlyBoundedSuffix(t *testing.T) {
	var w tailWriter
	w.Write(bytes.Repeat([]byte{'a'}, 50000))
	w.Write(bytes.Repeat([]byte{'b'}, 12000))
	if len(w.buf) != 32<<10 || cap(w.buf) > 32<<10 || !bytes.HasSuffix(w.buf, bytes.Repeat([]byte{'b'}, 12000)) {
		t.Fatal("tail buffer not bounded")
	}
}
func TestSecondDaemonCannotRecoverLogs(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "offline-test", "unused-test-password"
	cfg.Soulseek.ConnectOnStartup = false
	cfg.DownloadDir = t.TempDir()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	first, err := daemon.New(cfg, filepath.Join(config.DataDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	dir := filepath.Join(config.DataDir(), "logs")
	os.MkdirAll(dir, 0700)
	raw := filepath.Join(dir, "daemon-000001.log")
	os.WriteFile(raw, []byte("owned by first daemon"), 0600)
	if err := daemonCommand([]string{"--config", path}); err == nil {
		t.Fatal("second daemon acquired state")
	}
	if b, _ := os.ReadFile(raw); string(b) != "owned by first daemon" {
		t.Fatal("second daemon altered recovery segment")
	}
	if _, err := os.Stat(raw + ".gz"); !os.IsNotExist(err) {
		t.Fatal("second daemon compressed logs")
	}
}
