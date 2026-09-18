package soulseek

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

func TestDownloadOfferAdmissionAndNormalResumedReceiver(t *testing.T) {
	offers := make(chan *DownloadOffer, 1)
	c := NewClient(ClientConfig{DownloadOffered: func(o *DownloadOffer) error { offers <- o; return nil }})
	defer c.Close()
	control, remote := net.Pipe()
	defer control.Close()
	defer remote.Close()
	_ = remote.SetDeadline(time.Now().Add(5 * time.Second))
	served := make(chan struct{})
	go func() { c.serveMessagePeer(control, PeerInitMessage{Username: "sender", Type: "P"}); close(served) }()
	defer func() { remote.Close(); <-served }()
	must(t, writeMessage(remote, TransferRequest{Direction: 1, Token: 123, Filename: `Music\song`, Size: 3}))
	offer := <-offers
	failIf(t, offer.Username() != "sender" || offer.Filename() != "Music/song" || offer.Size() != 3, "offer metadata")
	file, err := os.CreateTemp(t.TempDir(), "receive")
	must(t, err)
	defer file.Close()
	if _, err := file.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	other := NewClient(ClientConfig{})
	defer other.Close()
	if err := other.ReceiveOfferedFile(context.Background(), offer, 1, file, nil, nil); !errors.Is(err, ErrMalformed) {
		t.Fatal("cross-client offer accepted", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.ReceiveOfferedFile(ctx, offer, 1, file, nil, nil) }()
	code, body, err := ReadFrame(remote)
	failIf(t, err != nil || code != PeerTransferResponse, code, err)
	response, err := DecodeTransferResponse(body)
	failIf(t, err != nil || !response.Accepted || response.Token != 123, response, err)
	if err := c.ReceiveOfferedFile(ctx, offer, 1, file, nil, nil); !errors.Is(err, ErrMalformed) {
		t.Fatal("offer reused", err)
	}
	incoming, sender := net.Pipe()
	defer incoming.Close()
	defer sender.Close()
	_ = sender.SetDeadline(time.Now().Add(5 * time.Second))
	fileDone := make(chan struct{})
	go func() { c.serveFile(incoming); close(fileDone) }()
	var token [4]byte
	binary.LittleEndian.PutUint32(token[:], 123)
	if _, err := sender.Write(token[:]); err != nil {
		t.Fatal(err)
	}
	var offset [8]byte
	if _, err := io.ReadFull(sender, offset[:]); err != nil || binary.LittleEndian.Uint64(offset[:]) != 1 {
		t.Fatal("resume offset", err)
	}
	if _, err := sender.Write([]byte("bc")); err != nil {
		t.Fatal(err)
	}
	must(t, <-done)
	<-fileDone
	data, err := os.ReadFile(file.Name())
	failIf(t, err != nil || string(data) != "abc", string(data), err)
	c.mu.Lock()
	defer c.mu.Unlock()
	failIf(t, len(c.requested) != 0 || len(c.downloads) != 0, "pending receive leaked")
}

func TestDownloadOfferDefaultsOffAndRejectsAdmissionFailure(t *testing.T) {
	for _, admit := range []func(*DownloadOffer) error{nil, func(*DownloadOffer) error { return errors.New("storage unavailable") }} {
		c := NewClient(ClientConfig{DownloadOffered: admit})
		control, remote := net.Pipe()
		_ = remote.SetDeadline(time.Now().Add(time.Second))
		done := make(chan struct{})
		go func() {
			c.offerDownload(control, "sender", "Music/song", TransferRequest{Direction: 1, Token: 7, Filename: `Music\song`, Size: 3})
			close(done)
		}()
		code, body, err := ReadFrame(remote)
		failIf(t, err != nil || code != PeerTransferResponse, code, err)
		response, err := DecodeTransferResponse(body)
		failIf(t, err != nil || response.Accepted || response.Reason != "Cancelled", response, err)
		<-done
		control.Close()
		remote.Close()
		c.Close()
	}
}
