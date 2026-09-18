package soulseek

import (
	"context"
	"net"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type syscallConner interface {
	SyscallConn() (syscall.RawConn, error)
}

func socketDevice(t *testing.T, socket syscallConner) string {
	t.Helper()
	raw, err := socket.SyscallConn()
	must(t, err)
	var device string
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		device, socketErr = unix.GetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE)
	}); err != nil {
		t.Fatal(err)
	}
	failIf(t, socketErr != nil, socketErr)
	return device
}

func TestNetworkInterfaceBindsEverySoulseekSocket(t *testing.T) {
	server, err := net.Listen("tcp4", "127.0.0.1:0")
	must(t, err)
	defer server.Close()
	accepted := make(chan net.Conn, 4)
	go func() {
		for {
			conn, err := server.Accept()
			if err != nil {
				return
			}
			accepted <- conn
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client := NewClient(ClientConfig{Address: server.Addr().String(), ListenAddr: "127.0.0.1:0", Username: "u", NetworkInterface: "lo"})
	defer client.Close()
	must(t, client.Connect(ctx))
	serverConn := <-accepted
	defer serverConn.Close()
	if got := socketDevice(t, client.conn.(syscallConner)); got != "lo" {
		t.Fatalf("server socket device = %q", got)
	}
	if got := socketDevice(t, client.listener.(*net.TCPListener)); got != "lo" {
		t.Fatalf("listener device = %q", got)
	}

	peer, err := client.connectAddress(ctx, server.Addr().String(), "P")
	must(t, err)
	defer peer.Close()
	peerServerConn := <-accepted
	defer peerServerConn.Close()
	if got := socketDevice(t, peer.(syscallConner)); got != "lo" {
		t.Fatalf("peer socket device = %q", got)
	}

	reserved, err := net.Listen("tcp4", "127.0.0.1:0")
	must(t, err)
	port := reserved.Addr().(*net.TCPAddr).Port
	reserved.Close()
	must(t, client.SetListenPort(uint16(port)))
	if got := socketDevice(t, client.listener.(*net.TCPListener)); got != "lo" {
		t.Fatalf("replacement listener device = %q", got)
	}

	const missing = "oto-missing0"
	bad := NewClient(ClientConfig{Address: server.Addr().String(), ListenAddr: "127.0.0.1:0", NetworkInterface: missing})
	defer bad.Close()
	if err := bad.Connect(ctx); err == nil {
		t.Fatal("missing interface did not block server connection")
	}
	if err := bad.startListener(); err == nil {
		t.Fatal("missing interface did not block listener")
	}
}
