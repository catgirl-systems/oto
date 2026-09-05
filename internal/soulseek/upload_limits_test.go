package soulseek

import (
	"errors"
	"sync"
	"testing"
)

func TestOutstandingUploadLimits(t *testing.T) {
	m := NewUploadManager(2)
	m.Configure(UploadPolicy{MaxQueuedFilesPerUser: 2, MaxQueuedBytesPerUser: 10})
	a, err := m.TryEnqueue("a", TransferRequest{Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.TryEnqueue("a", TransferRequest{Size: 6})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.TryEnqueue("a", TransferRequest{}); !errors.Is(err, ErrTooManyUploadFiles) {
		t.Fatal(err)
	}
	if _, err = m.TryEnqueue("b", TransferRequest{Size: 11}); !errors.Is(err, ErrTooManyUploadBytes) {
		t.Fatal(err)
	}
	m.Done(a)
	m.Done(a)
	if _, err = m.TryEnqueue("a", TransferRequest{Size: 5}); !errors.Is(err, ErrTooManyUploadBytes) {
		t.Fatal(err)
	}
	m.Done(b)
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.TryEnqueue("a", TransferRequest{Size: 5}); err == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if accepted != 2 {
		t.Fatalf("accepted %d", accepted)
	}
	m.Configure(UploadPolicy{MaxQueuedFilesPerUser: 1, MaxQueuedBytesPerUser: 1})
	if _, err = m.TryEnqueue("a", TransferRequest{}); !errors.Is(err, ErrTooManyUploadFiles) {
		t.Fatal(err)
	}
	restored := m.EnqueueRestored("a", TransferRequest{Size: 20})
	if restored == nil {
		t.Fatal("restore rejected")
	}
	m.Done(restored)
}
