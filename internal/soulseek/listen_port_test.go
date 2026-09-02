package soulseek

import (
	"encoding/binary"
	"net"
	"testing"
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
