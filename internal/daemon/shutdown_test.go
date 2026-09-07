package daemon

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

// net.Pipe makes a blocked advertisement deterministic instead of filling TCP buffers.
type shutdownWriteConn struct {
	net.Conn
	entered chan struct{}
	once    sync.Once
}

func (c *shutdownWriteConn) Write(b []byte) (int, error) {
	c.once.Do(func() { close(c.entered) })
	return c.Conn.Write(b)
}

func TestShutdownForceDuringBlockedPortAdvertisement(t *testing.T) {
	s, manager, _, _ := uploadService(t)
	_ = s.client.Close()
	local, remote := net.Pipe()
	defer remote.Close()
	conn := &shutdownWriteConn{Conn: local, entered: make(chan struct{})}
	s.client = soulseek.NewClientOnConn(soulseek.ClientConfig{ListenAddr: "127.0.0.1:0", Uploads: manager}, conn)
	s.cfg.Uploads.WaitForActiveUploadsOnQuit = true
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.listenPortFile = "configured-port-file"
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(reserved.Addr().(*net.TCPAddr).Port)
	reserved.Close()
	changed := make(chan struct{})
	go func() { s.applyListenPort(port, true); close(changed) }()
	select {
	case <-conn.entered:
	case <-time.After(time.Second):
		t.Fatal("write never started")
	}
	ctx, force := context.WithCancel(context.Background())
	defer force()
	done := make(chan struct{})
	go func() { s.WaitForUploads(ctx); close(done) }()
	force()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("port write blocked forced shutdown")
	}
	<-changed
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownOptInHotConfigAndRecovery(t *testing.T) {
	for _, mode := range []string{"default", "finish", "force"} {
		t.Run(mode, func(t *testing.T) {
			s, manager, active, _ := uploadService(t)
			previous := s.cfg
			cfg := previous
			s.SetConfigPath(filepath.Join(t.TempDir(), "config.json"))
			cfg.Uploads.WaitForActiveUploadsOnQuit = mode != "default"
			client := s.client
			if err := s.UpdateConfig(cfg); err != nil {
				t.Fatal(err)
			}
			if s.client != client || !hotConfigUpdate(previous, cfg) {
				t.Fatal("setting reconnects")
			}
			disk, err := config.Load(s.configPath)
			if err != nil || disk.Uploads.WaitForActiveUploadsOnQuit != cfg.Uploads.WaitForActiveUploadsOnQuit {
				t.Fatal("setting not saved", err)
			}
			if s.Snapshot().Shutdown != nil {
				t.Fatal("premature shutdown status")
			}
			if _, err := client.QueueUpload("peer", `Music\one`); err != nil {
				t.Fatal(err)
			}
			// An upload queued behind the active slot must stay recoverable.
			eventID := uploadRow(t, s, "peer", `Music\one`).ID
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { s.WaitForUploads(ctx); close(done) }()
			if mode != "default" {
				deadline := time.Now().Add(time.Second)
				for {
					if st := s.Snapshot().Shutdown; st != nil && st.Draining {
						if st.ActiveUploads != 1 {
							t.Fatal(st)
						}
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("no drain")
					}
					time.Sleep(time.Millisecond)
				}
				select {
				case <-done:
					t.Fatal("did not wait")
				default:
				}
				if err := s.UpdateConfig(cfg); !errors.Is(err, ErrClosed) {
					t.Fatal("config allowed during shutdown", err)
				}
				if err := s.SetPresence(PresenceOffline); !errors.Is(err, ErrClosed) {
					t.Fatal("disconnect allowed", err)
				}
				if _, err := s.UploadAction(UploadActionRequest{Action: "retry", IDs: []string{eventID}}); !errors.Is(err, ErrClosed) {
					t.Fatal("retry allowed", err)
				}
				if mode == "force" {
					cancel()
				} else {
					manager.Done(active)
				}
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("shutdown did not return")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			restored, err := New(cfg, s.journalPath)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			if len(restored.journal.Uploads) != 1 || !restored.journal.Uploads[0].Recoverable {
				t.Fatal("restart lost queue")
			}
		})
	}
}
