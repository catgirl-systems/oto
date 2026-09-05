package soulseek

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"testing/synctest"
	"time"
)

func TestDownloadBucketAccounting(t *testing.T) {
	var l downloadLimiter
	l.configure(1024)
	now := l.last
	n, delay, changed := l.take(32768, now)
	if n != 1024 || delay != 0 {
		t.Fatalf("initial credit: %d %v", n, delay)
	}
	if n, delay, _ := l.take(1024, now); n != 0 || delay != time.Second {
		t.Fatalf("shared credit: %d %v", n, delay)
	}
	l.refund(512, changed)
	if n, delay, _ := l.take(1024, now); n != 0 || delay != 500*time.Millisecond {
		t.Fatalf("short read refund: %d %v", n, delay)
	}
	l.configure(1024)
	if l.changed != changed || l.credit != 512 {
		t.Fatal("unchanged limit reset credit")
	}
	if n, _, _ := l.take(1024, now.Add(time.Hour)); n != 1024 {
		t.Fatal("idle refill missing")
	}
	if n, _, _ := l.take(1024, now.Add(time.Hour)); n != 0 {
		t.Fatal("idle credit exceeded one chunk")
	}
	l.configure(2048)
	select {
	case <-changed:
	default:
		t.Fatal("rate change did not wake waiters")
	}
	_, _, _ = l.take(1024, l.last)
	l.refund(1024, changed)
	if l.credit != 0 {
		t.Fatal("old generation refunded new bucket")
	}
	l.configure(0)
	if n, delay, _ := l.take(32768, l.last); n != 32768 || delay != 0 {
		t.Fatal("unlimited not immediate")
	}
}

func TestDownloadLimiterWakeAndRefund(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l downloadLimiter
		l.configure(1024)
		reader := downloadReader{context.Background(), &l, bytes.NewReader([]byte("abc"))}
		if n, err := reader.Read(make([]byte, 32768)); n != 3 || err != nil || l.credit != 1021 {
			t.Fatalf("short read: %d %v credit %v", n, err, l.credit)
		}
		if n, err := reader.Read(make([]byte, 32768)); n != 0 || err != io.EOF {
			t.Fatalf("EOF: %d %v", n, err)
		}
		// EOF refunded its entire grant. Consume it before testing blocked waits.
		_, _, _ = l.take(1024, time.Now())
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, _, err := l.acquire(ctx, 1024); done <- err }()
		synctest.Wait()
		cancel()
		synctest.Wait()
		if err := <-done; !errors.Is(err, ErrTransferCancelled) {
			t.Fatalf("cancel: %v", err)
		}
		for _, rate := range []int64{2048, 1024, 0} {
			go func() { _, _, err := l.acquire(context.Background(), 1024); done <- err }()
			synctest.Wait()
			select {
			case err := <-done:
				t.Fatalf("waiter escaped before change: %v", err)
			default:
			}
			l.configure(rate)
			synctest.Wait()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
	})
}

type downloadMemory []byte

func (b downloadMemory) WriteAt(p []byte, offset int64) (int, error) {
	return copy(b[int(offset):], p), nil
}

func TestSharedDownloadLimitReceivePath(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := NewClient(ClientConfig{DownloadLimitBytesPerSecond: 1024})
		defer c.Close()
		progress := make(chan Progress, 8)
		peers := make(chan error, 2)
		pending := make([]*pendingDownload, 2)
		targets := make([]downloadMemory, 2)
		for i := range pending {
			targets[i] = make(downloadMemory, 2055)
			copy(targets[i], "resumed")
			pending[i] = &pendingDownload{ctx: context.Background(), size: 2055, offset: 7, writer: targets[i], done: make(chan error, 1), progress: func(p Progress) { progress <- p }}
			c.downloads[uint32(i+1)] = pending[i]
		}
		for i := range pending {
			left, right := net.Pipe()
			go func() { defer left.Close(); c.serveFile(left) }()
			go func(token uint32) {
				defer right.Close()
				var init [4]byte
				binary.LittleEndian.PutUint32(init[:], token)
				_, err := right.Write(init[:])
				if err == nil {
					var offset [8]byte
					_, err = io.ReadFull(right, offset[:])
					if err == nil && binary.LittleEndian.Uint64(offset[:]) != 7 {
						err = errors.New("wrong resume offset")
					}
				}
				if err == nil {
					_, err = right.Write(bytes.Repeat([]byte("x"), 2048))
				}
				peers <- err
			}(uint32(i + 1))
		}
		synctest.Wait()
		if len(progress) != 1 {
			t.Fatalf("expected one shared initial chunk, got %d", len(progress))
		}
		p := <-progress
		if p.Done != 1031 {
			t.Fatalf("low rate did not report short read immediately: %+v", p)
		}
		for step := 1; step <= 3; step++ {
			time.Sleep(time.Second)
			synctest.Wait()
			if len(progress) != 1 {
				t.Fatalf("second %d: progress count %d", step, len(progress))
			}
			<-progress
		}
		for i, p := range pending {
			if err := <-p.done; err != nil {
				t.Fatal(err)
			}
			if err := <-peers; err != nil {
				t.Fatal(err)
			}
			if string(targets[i][:7]) != "resumed" || !bytes.Equal(targets[i][7:], bytes.Repeat([]byte("x"), 2048)) {
				t.Fatal("resume corrupted payload")
			}
		}
	})
}

func TestThrottledDownloadCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l downloadLimiter
		l.configure(1024)
		ctx, cancel := context.WithCancel(context.Background())
		var dst bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- copyAtMost(ctx, &dst, downloadReader{ctx, &l, bytes.NewReader(make([]byte, 65536))}, 65536, 0, nil)
		}()
		synctest.Wait()
		if dst.Len() != 1024 {
			t.Fatalf("partial bytes: %d", dst.Len())
		}
		cancel()
		synctest.Wait()
		if err := <-done; !errors.Is(err, ErrTransferCancelled) {
			t.Fatalf("cancel: %v", err)
		}
		if dst.Len() != 1024 {
			t.Fatal("cancellation read more bytes")
		}
	})
}
