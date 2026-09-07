package soulseek

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestUploadDrainCutoff(t *testing.T) {
	m := NewUploadManager(2)
	a := m.Enqueue("a", TransferRequest{})
	b := m.Enqueue("b", TransferRequest{})
	q := m.Enqueue("c", TransferRequest{})
	done := m.Drain()
	if done != m.Drain() {
		t.Fatal("drain is not idempotent")
	}
	if draining, active := m.DrainStatus(); !draining || active != 2 {
		t.Fatal(draining, active)
	}
	if _, err := m.TryEnqueue("new", TransferRequest{}); !errors.Is(err, ErrUploadsDraining) {
		t.Fatal(err)
	}
	if m.EnqueueRestored("restored", TransferRequest{}) != nil {
		t.Fatal("restored admission reopened drain")
	}
	m.Configure(UploadPolicy{Scheduling: UploadScheduleRandom})
	m.Done(a)
	select {
	case <-done:
		t.Fatal("ignored second active upload")
	default:
	}
	m.Done(b)
	select {
	case <-done:
	default:
		t.Fatal("did not finish")
	}
	select {
	case <-q.Ready:
		t.Fatal("promoted queued work")
	default:
	}
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Wait(expired, q); !errors.Is(err, ErrUploadsDraining) {
		t.Fatal("frozen queue expired", err)
	}
	m.Done(q)
	m.Done(b)
	empty := NewUploadManager(1)
	select {
	case <-empty.Drain():
	default:
		t.Fatal("empty drain blocked")
	}
}

func TestUploadDrainRequeueAndCompletion(t *testing.T) {
	for _, mode := range []string{"normal", "reject"} {
		t.Run(mode, func(t *testing.T) {
			addr, _, received := uploadPeer(t, mode, 0)
			c, events, _ := uploadClient(t, addr, bytes.Repeat([]byte("x"), 2048))
			// Hold the active worker before any network work, including its callback.
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			var once sync.Once
			c.cfg.UploadUpdate = func(e TransferEvent) {
				if e.State == "running" {
					once.Do(func() { close(entered); <-release })
				}
				events <- e
			}
			if _, err := c.QueueUpload("peer", `Music\song`); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("upload never started")
			}
			if _, err := c.QueueUpload("queued", `Music\song`); err != nil {
				t.Fatal(err)
			}
			done := c.DrainUploads()
			var wg sync.WaitGroup
			for range 16 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, _, err := c.registerUpload("peer", `Music\song`, true); !errors.Is(err, ErrUploadsDraining) {
						t.Error("peer requeue", err)
					}
				}()
			}
			wg.Wait()
			if _, err := c.RestoreUpload("peer", `Music\song`, ""); !errors.Is(err, ErrUploadsDraining) {
				t.Fatal(err)
			}
			if uploadDenial(ErrUploadsDraining) != "Shutting down" {
				t.Fatal("misleading denial")
			}
			unblock()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("drain stuck")
			}
			state := "completed"
			if mode == "reject" {
				state = "failed"
			}
			uploadEvent(t, events, state)
			if mode == "normal" {
				select {
				case got := <-received:
					if len(got) != 2048 {
						t.Fatal(len(got))
					}
				case <-time.After(time.Second):
					t.Fatal("missing bytes")
				}
			}
			c.mu.Lock()
			queued := c.uploads[downloadKey("queued", `Music\song`)]
			c.mu.Unlock()
			if queued == nil {
				t.Fatal("queued attempt lost")
			}
			queued.mu.Lock()
			state = queued.state
			queued.mu.Unlock()
			if state != "queued" {
				t.Fatal("queued work started", state)
			}
		})
	}
}
