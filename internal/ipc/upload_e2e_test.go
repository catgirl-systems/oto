package ipc

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

// Exercise the real daemon session, queue protocol and Unix API without an
// external server. The loopback peer rejects its first offer, then receives.
func TestUploadControlsEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var sockets []net.Conn
	listen := func() net.Listener {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		return ln
	}
	server, peer := listen(), listen()
	t.Cleanup(func() {
		cancel()
		_ = server.Close()
		_ = peer.Close()
		mu.Lock()
		for _, p := range sockets {
			_ = p.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	accept := func(ln net.Listener, handle func(net.Conn)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				p, err := ln.Accept()
				if err != nil {
					return
				}
				mu.Lock()
				sockets = append(sockets, p)
				mu.Unlock()
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer p.Close()
					_ = p.SetDeadline(time.Now().Add(15 * time.Second))
					handle(p)
				}()
			}
		}()
	}
	send := func(p net.Conn, m soulseek.Message) {
		b, err := soulseek.EncodeMessage(m)
		if err == nil {
			_, _ = p.Write(b)
		}
	}
	ports := make(chan uint32, 8)
	accept(server, func(p net.Conn) {
		for {
			cmd, b, err := soulseek.ReadFrame(p)
			if err != nil {
				return
			}
			switch cmd {
			case soulseek.ServerLogin:
				send(p, soulseek.LoginResponse{Success: true, IP: 0x7f000001})
			case soulseek.ServerSetListenPort:
				d := soulseek.NewDecoder(b)
				port, _ := d.U32()
				ports <- port
			case soulseek.ServerGetPeerAddress:
				d := soulseek.NewDecoder(b)
				user, _ := d.String()
				send(p, soulseek.PeerAddress{Username: user, IP: "127.0.0.1", Port: uint32(peer.Addr().(*net.TCPAddr).Port)})
			}
		}
	})
	var offered atomic.Int32
	accept(peer, func(p net.Conn) {
		_, b, err := soulseek.ReadInitFrame(p)
		if err != nil {
			return
		}
		d := soulseek.NewDecoder(b)
		_, _ = d.String()
		kind, _ := d.String()
		if kind == "P" {
			cmd, b, err := soulseek.ReadFrame(p)
			if err != nil || cmd != soulseek.PeerTransferRequest {
				return
			}
			req, err := soulseek.DecodeTransferRequest(b)
			if err != nil {
				return
			}
			send(p, soulseek.TransferResponse{Token: req.Token, Accepted: offered.Add(1) > 1, Reason: "test rejection"})
		} else if kind == "F" {
			var token [4]byte
			if _, err := io.ReadFull(p, token[:]); err != nil {
				return
			}
			var offset [8]byte
			binary.LittleEndian.PutUint64(offset[:], 7)
			if _, err := p.Write(offset[:]); err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, p)
		}
	})
	root := t.TempDir()
	contents := bytes.Repeat([]byte("data"), 4096)
	for _, name := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(root, name), contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.Soulseek.Server = server.Addr().String()
	cfg.Soulseek.Username = "uploader"
	cfg.Soulseek.Password = "test"
	reserved := listen()
	cfg.Soulseek.ListenAddr = reserved.Addr().String()
	_ = reserved.Close()
	cfg.Soulseek.NATPMPPortMapping = false
	cfg.Soulseek.UPnPPortMapping = false
	cfg.Shares = []config.Share{{Name: "Music", Path: root}}
	cfg.UploadSlots = 1
	cfg.Uploads.Profiles[0].SpeedLimitKiB = 1
	svc, err := daemon.New(cfg, filepath.Join(t.TempDir(), "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	var port uint32
	select {
	case port = <-ports:
	case <-ctx.Done():
		t.Fatal("no advertised port")
	}
	path := filepath.Join(t.TempDir(), "api.sock")
	api := NewServer(svc, path)
	if _, err := api.Listen(); err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	api.http = &http.Server{Handler: api.handler()}
	go api.http.Serve(api.listener)
	client := NewClient(path)
	wait := func(check func([]daemon.Transfer) bool) {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for {
			rows, err := client.Transfers(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if check(rows) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("state timeout: %+v", rows)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	state := func(id, want string) {
		wait(func(rows []daemon.Transfer) bool {
			for _, r := range rows {
				if r.ID == id {
					return r.State == want
				}
			}
			return false
		})
	}
	incoming, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer incoming.Close()
	_ = incoming.SetDeadline(time.Now().Add(12 * time.Second))
	var init soulseek.Encoder
	_ = init.String("receiver")
	_ = init.String("P")
	init.U32(0)
	if err := soulseek.WriteInitFrame(incoming, byte(soulseek.PeerInit), init.Payload()); err != nil {
		t.Fatal(err)
	}
	queue := func(name string) {
		send(incoming, soulseek.QueueRequest{Filename: `Music\` + name})
		if _, _, err := soulseek.ReadFrame(incoming); err != nil {
			t.Fatal(err)
		}
	}
	action := func(req daemon.UploadActionRequest, changed int) {
		t.Helper()
		result, err := client.UploadAction(ctx, req)
		if err != nil || result.Changed != changed || len(result.Errors) != 0 {
			t.Fatalf("action %+v: %+v %v", req, result, err)
		}
	}
	one, two := `upload:receiver:Music\one`, `upload:receiver:Music\two`
	queue("one")
	state(one, "failed")
	action(daemon.UploadActionRequest{Action: "retry", IDs: []string{one}}, 1)
	state(one, "running")
	queue("two")
	state(two, "queued")
	action(daemon.UploadActionRequest{Action: "cancel", IDs: []string{one}}, 1)
	state(one, "cancelled")
	state(two, "running")
	action(daemon.UploadActionRequest{Action: "cancel", Usernames: []string{"receiver"}}, 1)
	state(two, "cancelled")
	action(daemon.UploadActionRequest{Action: "retry", IDs: []string{one}}, 1)
	state(one, "running")
	queue("two")
	state(two, "queued")
	action(daemon.UploadActionRequest{Action: "clear", States: []string{"queued"}}, 1)
	action(daemon.UploadActionRequest{Action: "clear", All: true}, 1)
	wait(func(rows []daemon.Transfer) bool { return len(rows) == 0 })
	for _, name := range []string{"one", "two"} {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || !bytes.Equal(got, contents) {
			t.Fatal("shared file modified")
		}
	}
}
