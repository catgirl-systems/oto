package soulseek

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"
)

func TestSetListenPortRebindsAndAdvertises(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewClientOnConn(ClientConfig{ListenAddr: "127.0.0.1:0"}, clientConn)
	t.Cleanup(func() {
		_ = client.Close()
		_ = serverConn.Close()
	})
	if err := client.startListener(); err != nil {
		t.Fatal(err)
	}

	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()

	frame := make(chan struct {
		command uint32
		payload []byte
		err     error
	}, 1)
	go func() {
		command, payload, err := ReadFrame(serverConn)
		frame <- struct {
			command uint32
			payload []byte
			err     error
		}{command, payload, err}
	}()
	if err := client.SetListenPort(uint16(port)); err != nil {
		t.Fatal(err)
	}
	message := <-frame
	if message.err != nil || message.command != ServerSetListenPort || len(message.payload) != 4 || binary.LittleEndian.Uint32(message.payload) != uint32(port) {
		t.Fatalf("listen port frame: command=%d payload=%v err=%v", message.command, message.payload, message.err)
	}

	client.mu.Lock()
	got := client.listener.Addr().(*net.TCPAddr).Port
	client.mu.Unlock()
	if got != port {
		t.Fatalf("listener port = %d, want %d", got, port)
	}
}

func TestAdvertisedPortDoesNotRebindAndUpdatesAfterLogin(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewClientOnConn(ClientConfig{Username: "u", Password: "p", ListenAddr: "127.0.0.1:0"}, clientConn)
	t.Cleanup(func() {
		_ = client.Close()
		_ = serverConn.Close()
	})
	if err := client.startListener(); err != nil {
		t.Fatal(err)
	}
	boundPort := client.ListenPort()
	if got := client.PublicPort(); got != boundPort {
		t.Fatalf("public port = %d, want listener port %d", got, boundPort)
	}
	if err := client.SetAdvertisedPort(61000); err != nil {
		t.Fatal(err)
	}
	if got := client.PublicPort(); got != 61000 {
		t.Fatalf("public port = %d, want advertised port 61000", got)
	}

	serverDone := make(chan error, 1)
	go func() {
		command, _, err := ReadFrame(serverConn)
		if err == nil && command != ServerLogin {
			err = errors.New("expected login request")
		}
		if err == nil {
			var frame []byte
			frame, err = EncodeMessage(LoginResponse{Success: true, Message: "ok"})
			if err == nil {
				_, err = serverConn.Write(frame)
			}
		}
		for i := 0; err == nil && i < 5; i++ {
			var payload []byte
			command, payload, err = ReadFrame(serverConn)
			if i == 0 && (command != ServerSetListenPort || len(payload) != 4 || binary.LittleEndian.Uint32(payload) != 61000) {
				err = errors.New("mapped port was not advertised during login")
			}
		}
		if err == nil {
			var payload []byte
			command, payload, err = ReadFrame(serverConn)
			if err == nil && (command != ServerSetListenPort || len(payload) != 4 || binary.LittleEndian.Uint32(payload) != 61001) {
				err = errors.New("renewed port was not advertised")
			}
		}
		serverDone <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Login(ctx); err != nil {
		t.Fatal(err)
	}
	if got := client.ListenPort(); got != boundPort {
		t.Fatalf("listener rebound from %d to %d", boundPort, got)
	}
	if err := client.SetAdvertisedPort(61001); err != nil {
		t.Fatal(err)
	}
	if got := client.PublicPort(); got != 61001 {
		t.Fatalf("public port = %d, want renewed advertised port 61001", got)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}
