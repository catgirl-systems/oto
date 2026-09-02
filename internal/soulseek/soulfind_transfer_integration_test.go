package soulseek

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSoulfindResumeTransfer(t *testing.T) {
	addr := soulfindAddress(t)
	stamp := fmt.Sprintf("%x", time.Now().UnixNano())
	filename := "resume-" + stamp + ".flac"
	contents := bytes.Repeat([]byte("resume over Soulfind\n"), 1024)
	target := startSoulfindClient(t, addr, "r"+stamp, map[string][]byte{filename: contents}, nil)
	observer := startSoulfindClient(t, addr, "s"+stamp, nil, nil)

	destination, err := os.CreateTemp(t.TempDir(), "download-")
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	offset := uint64(len(contents) / 3)
	if _, err := destination.WriteAt(contents[:offset], 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := observer.Download(ctx, target.cfg.Username, "Music\\"+filename, uint64(len(contents)), offset, destination, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination.Name())
	if err != nil || !bytes.Equal(got, contents) {
		t.Fatalf("resumed download: bytes=%d err=%v", len(got), err)
	}
}

func TestSoulfindRejectsUnsharedFile(t *testing.T) {
	addr := soulfindAddress(t)
	stamp := fmt.Sprintf("%x", time.Now().UnixNano())
	target := startSoulfindClient(t, addr, "m"+stamp, nil, nil)
	observer := startSoulfindClient(t, addr, "n"+stamp, nil, nil)
	destination, err := os.CreateTemp(t.TempDir(), "download-")
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = observer.Download(ctx, target.cfg.Username, "Music\\missing.flac", 1, 0, destination, nil)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "not shared") {
		t.Fatalf("unshared download error = %v", err)
	}
}

type blockingWriterAt struct {
	file             *os.File
	entered, release chan struct{}
	once             sync.Once
}

func (w *blockingWriterAt) WriteAt(p []byte, offset int64) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return w.file.WriteAt(p, offset)
}

func TestSoulfindUploadQueue(t *testing.T) {
	addr := soulfindAddress(t)
	stamp := fmt.Sprintf("%x", time.Now().UnixNano())
	firstName, secondName := "queue-a-"+stamp+".flac", "queue-b-"+stamp+".flac"
	firstContents := bytes.Repeat([]byte("a"), 4<<20)
	secondContents := []byte("second queued transfer")
	target := startSoulfindClient(t, addr, "u"+stamp, map[string][]byte{firstName: firstContents, secondName: secondContents}, NewUploadManager(1))
	observer := startSoulfindClient(t, addr, "v"+stamp, nil, nil)

	firstFile, err := os.CreateTemp(t.TempDir(), "first-")
	if err != nil {
		t.Fatal(err)
	}
	defer firstFile.Close()
	blocked := &blockingWriterAt{file: firstFile, entered: make(chan struct{}), release: make(chan struct{})}
	released := false
	defer func() {
		if !released {
			close(blocked.release)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- observer.Download(ctx, target.cfg.Username, "Music\\"+firstName, uint64(len(firstContents)), 0, blocked, nil)
	}()
	select {
	case <-blocked.entered:
	case <-ctx.Done():
		t.Fatal("first upload did not start")
	}

	secondFile, err := os.CreateTemp(t.TempDir(), "second-")
	if err != nil {
		t.Fatal(err)
	}
	defer secondFile.Close()
	queued := make(chan Progress, 1)
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- observer.Download(ctx, target.cfg.Username, "Music\\"+secondName, uint64(len(secondContents)), 0, secondFile, func(progress Progress) {
			if progress.State == "queued" {
				select {
				case queued <- progress:
				default:
				}
			}
		})
	}()
	select {
	case progress := <-queued:
		if progress.Queue != 1 {
			t.Fatalf("queue place = %d", progress.Queue)
		}
	case <-ctx.Done():
		t.Fatal("second upload was not queued")
	}
	select {
	case err := <-secondDone:
		t.Fatalf("queued upload completed before slot opened: %v", err)
	default:
	}
	close(blocked.release)
	released = true
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(secondFile.Name())
	if err != nil || !bytes.Equal(got, secondContents) {
		t.Fatalf("queued download: %q %v", got, err)
	}
}
