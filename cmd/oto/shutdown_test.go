package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/ipc"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestShutdownProcessHelper(t *testing.T) {
	if os.Getenv("OTO_SHUTDOWN_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			if err := run(os.Args[i+1:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(2)
}

// Minimal loopback server/peer: no public network or account. The peer holds the
// resume-offset handshake so the test controls when an allocated upload finishes.
func shutdownNetwork(t *testing.T) (string, <-chan struct{}, func(), <-chan []byte) {
	t.Helper()
	server, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	peer, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	release, reached := make(chan struct{}), make(chan struct{}, 4)
	data := make(chan []byte, 4)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var wg sync.WaitGroup
	var mu sync.Mutex
	var connections []net.Conn
	accept := func(ln net.Listener, handle func(net.Conn)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				mu.Lock()
				connections = append(connections, c)
				mu.Unlock()
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer c.Close()
					_ = c.SetDeadline(time.Now().Add(20 * time.Second))
					handle(c)
				}()
			}
		}()
	}
	writeMessage := func(c net.Conn, m soulseek.Message) {
		b, err := soulseek.EncodeMessage(m)
		if err == nil {
			_, _ = c.Write(b)
		}
	}
	accept(server, func(c net.Conn) {
		for {
			code, _, err := soulseek.ReadFrame(c)
			if err != nil {
				return
			}
			switch code {
			case soulseek.ServerLogin:
				writeMessage(c, soulseek.LoginResponse{Success: true, Message: "ok"})
			case soulseek.ServerGetPeerAddress:
				writeMessage(c, soulseek.PeerAddress{Username: "peer", IP: "127.0.0.1", Port: uint32(peer.Addr().(*net.TCPAddr).Port)})
			}
		}
	})
	accept(peer, func(c net.Conn) {
		_, payload, err := soulseek.ReadInitFrame(c)
		if err != nil {
			return
		}
		d := soulseek.NewDecoder(payload)
		_, _ = d.String()
		kind, _ := d.String()
		if kind == "P" {
			code, payload, err := soulseek.ReadFrame(c)
			if err != nil || code != soulseek.PeerTransferRequest {
				return
			}
			req, err := soulseek.DecodeTransferRequest(payload)
			if err != nil {
				return
			}
			writeMessage(c, soulseek.TransferResponse{Token: req.Token, Accepted: true})
		} else if kind == "F" {
			var token [4]byte
			if _, err := io.ReadFull(c, token[:]); err != nil {
				return
			}
			reached <- struct{}{}
			<-release
			if _, err := c.Write(make([]byte, 8)); err != nil {
				return
			}
			b, _ := io.ReadAll(c)
			data <- b
		}
	})
	t.Cleanup(func() {
		server.Close()
		peer.Close()
		unblock()
		mu.Lock()
		for _, c := range connections {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return server.Addr().String(), reached, unblock, data
}

func shutdownStatus(t *testing.T, client *ipc.Client, ready func(daemon.Snapshot) bool) daemon.Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		s, err := client.Status(ctx)
		cancel()
		if err == nil && ready(s) {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon status deadline")
	return daemon.Snapshot{}
}

func TestChildShutdownUploads(t *testing.T) {
	for _, mode := range []string{"default", "eof", "signal", "force", "eof-force"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("OTO_SHUTDOWN_HELPER", "1")
			address, reached, release, data := shutdownNetwork(t)
			cfg := config.Default()
			cfg.Soulseek.Username, cfg.Soulseek.Password = "local-test", "local-test"
			port, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			cfg.Soulseek.Server, cfg.Soulseek.ListenAddr = address, port.Addr().String()
			port.Close()
			cfg.Soulseek.NATPMPPortMapping, cfg.Soulseek.UPnPPortMapping = false, false
			cfg.AudioMetadata = false
			cfg.UploadSlots = 1
			cfg.Uploads.WaitForActiveUploadsOnQuit = mode != "default"
			root := t.TempDir()
			cfg.Shares = []config.Share{{Name: "Music", Path: root}}
			cfg.DownloadDir = t.TempDir()
			content := bytes.Repeat([]byte("local fixture\n"), 100)
			for _, name := range []string{"one", "two"} {
				if err := os.WriteFile(filepath.Join(root, name), content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(t.TempDir(), "config.json")
			if err := cfg.Save(path); err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(t.TempDir(), "oto-test")
			if err := os.WriteFile(script, []byte("#!/bin/sh\nexec '"+strings.ReplaceAll(binary, "'", "'\\''")+"' -test.run=^TestShutdownProcessHelper$ -- \"$@\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			old := executable
			executable = func() (string, error) { return script, nil }
			defer func() { executable = old }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			child, keepAlive, err := startChild(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			var done chan struct{}
			defer func() {
				_ = keepAlive.Close()
				_ = child.Process.Kill()
				if done != nil {
					<-done
				} else {
					_ = child.Wait()
				}
			}()
			client := ipc.NewClient(config.SocketPath())
			st := shutdownStatus(t, client, func(s daemon.Snapshot) bool { return s.Status == daemon.StatusConnected && s.PublicPort != 0 })
			p, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", st.PublicPort))
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			var init soulseek.Encoder
			_ = init.String("peer")
			_ = init.String("P")
			init.U32(1)
			if err := soulseek.WriteInitFrame(p, byte(soulseek.PeerInit), init.Payload()); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{`Music\one`, `Music\two`} {
				msg, err := soulseek.EncodeMessage(soulseek.QueueRequest{Filename: name})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := p.Write(msg); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-reached:
			case <-time.After(5 * time.Second):
				t.Fatal("no active upload")
			}
			shutdownStatus(t, client, func(s daemon.Snapshot) bool { return len(s.Transfers) == 2 })
			if mode == "signal" || mode == "force" {
				if err := child.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				shutdownStatus(t, client, func(s daemon.Snapshot) bool { return s.Shutdown != nil && s.Shutdown.Draining })
			}
			// Cancellation of the owning frontend must not SIGKILL its ready child.
			cancel()
			var output bytes.Buffer
			done = make(chan struct{})
			go func() { waitForChild(child, keepAlive, client, &output); close(done) }()
			if mode != "default" {
				st = shutdownStatus(t, client, func(s daemon.Snapshot) bool { return s.Shutdown != nil && s.Shutdown.Draining })
				if st.Shutdown.ActiveUploads != 1 {
					t.Fatal(st.Shutdown)
				}
				if mode == "force" || mode == "eof-force" {
					if mode == "eof-force" {
						// Exercise the frontend supervisor's force-signal forwarding.
						self, _ := os.FindProcess(os.Getpid())
						_ = self.Signal(os.Interrupt)
					} else {
						_ = child.Process.Signal(syscall.SIGTERM)
					}
				} else {
					select {
					case <-done:
						t.Fatal("graceful upload truncated")
					case <-time.After(3300 * time.Millisecond):
					}
					release()
				}
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("child failed to exit")
			}
			if mode == "signal" || mode == "eof" {
				select {
				case got := <-data:
					if !bytes.Equal(got, content) {
						t.Fatal("incomplete upload", len(got))
					}
				case <-time.After(time.Second):
					t.Fatal("no bytes")
				}
				if !strings.Contains(output.String(), "Waiting for 1 active upload(s); Ctrl+C") {
					t.Fatal("missing waiting instructions", output.String())
				}
			}
			// Reopen SQLite after process exit; queued work survives every shutdown mode.
			if mode != "default" {
				log, err := os.ReadFile(filepath.Join(config.DataDir(), "logs", "daemon.log"))
				if err != nil {
					t.Fatal(err)
				}
				finish := "upload_drain_completed"
				if mode == "force" || mode == "eof-force" {
					finish = "upload_drain_forced"
				}
				if !bytes.Contains(log, []byte("upload_drain_started")) || !bytes.Contains(log, []byte(finish)) {
					t.Fatal("missing drain diagnostics", string(log))
				}
				if mode == "eof-force" && !strings.Contains(output.String(), "Forcing shutdown") {
					t.Fatal("force not forwarded", output.String())
				}
			}
			svc, err := daemon.New(cfg, filepath.Join(config.DataDir(), "state.sqlite3"))
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			rows := svc.Transfers()
			if len(rows) != 2 {
				t.Fatalf("lost history: %+v", rows)
			}
			for _, row := range rows {
				want := "interrupted"
				if row.Filename == `Music\one` && (mode == "eof" || mode == "signal") {
					want = "completed"
				}
				if row.State != want {
					t.Fatalf("%s: %+v", mode, row)
				}
			}
		})
	}
}

func TestDaemonShutdownWithoutUploadsAndIPCFailure(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			cfg := config.Default()
			cfg.Soulseek.Username, cfg.Soulseek.Password = "local", "local"
			cfg.Soulseek.ConnectOnStartup = false
			cfg.Uploads.WaitForActiveUploadsOnQuit = true
			svc, err := daemon.New(cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			path := config.SocketPath()
			if failure {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				other, err := net.Listen("unix", path)
				if err != nil {
					t.Fatal(err)
				}
				defer other.Close()
			}
			eof := make(chan struct{})
			done := make(chan error, 1)
			go func() { done <- runDaemon(svc, ipc.NewServer(svc, path), make(chan os.Signal), eof) }()
			if !failure {
				shutdownStatus(t, ipc.NewClient(path), func(s daemon.Snapshot) bool { return s.Shutdown == nil })
				close(eof)
			}
			select {
			case err := <-done:
				if (err != nil) != failure {
					t.Fatalf("failure=%t: %v", failure, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("shutdown blocked without active uploads")
			}
			if failure {
				conn, err := net.Dial("unix", path)
				if err != nil {
					t.Fatal("removed another daemon's socket", err)
				}
				conn.Close()
			}
		})
	}
}
