package soulseek

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"
)

func TestDownloadAuthorizationRechecksFileConnectionBeforeOffset(t *testing.T) {
	c := NewClient(ClientConfig{})
	defer c.Close()
	file, err := os.CreateTemp(t.TempDir(), "receive")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	denied := errors.New("consent revoked")
	pending := &pendingDownload{username: "sender", filename: "Music/song", size: 3, ctx: context.Background(), writer: file, done: make(chan error, 1), authorize: func(netip.Addr) error { return denied }}
	c.downloads[7] = pending
	incoming, sender := net.Pipe()
	defer sender.Close()
	_ = sender.SetDeadline(time.Now().Add(time.Second))
	done := make(chan struct{})
	go func() { defer close(done); defer incoming.Close(); c.serveFile(incoming) }()
	var token [4]byte
	binary.LittleEndian.PutUint32(token[:], 7)
	if _, err := sender.Write(token[:]); err != nil {
		t.Fatal(err)
	}
	var offset [8]byte
	if _, err := io.ReadFull(sender, offset[:]); !errors.Is(err, io.EOF) {
		t.Fatal("offset sent without renewed consent", err)
	}
	if err := <-pending.done; !errors.Is(err, denied) {
		t.Fatal("wrong denial", err)
	}
	<-done
	if stat, err := file.Stat(); err != nil || stat.Size() != 0 {
		t.Fatal("writer used without consent", err)
	}
}
