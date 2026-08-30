package soulseek

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const soulfindWatchUserCommand uint32 = 5

type soulfindWatch struct {
	username       string
	exists         bool
	status         uint32
	files, folders uint32
}

func TestSoulfindHandshakeCriticalValues(t *testing.T) {
	addr := os.Getenv("OTO_SOULFIND_ADDR")
	if addr == "" {
		t.Skip("OTO_SOULFIND_ADDR is unset")
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "critical.flac"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	shares := NewShareIndex()
	if err := shares.AddRoot("Music", root); err != nil {
		t.Fatal(err)
	}
	if err := shares.Scan(); err != nil {
		t.Fatal(err)
	}

	stamp := time.Now().UnixNano()
	targetUser, observerUser := fmt.Sprintf("t%d", stamp), fmt.Sprintf("o%d", stamp)
	target := NewClient(ClientConfig{Address: addr, Username: targetUser, Password: "pw", ListenAddr: "127.0.0.1:0", Share: shares})
	t.Cleanup(func() { _ = target.Close() })
	connectSoulfind(t, target)

	listener, ok := target.ListenerAddr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address: %v", target.ListenerAddr())
	}

	observer := NewClient(ClientConfig{Address: addr, Username: observerUser, Password: "pw", ListenAddr: "127.0.0.1:0"})
	t.Cleanup(func() { _ = observer.Close() })
	connectSoulfind(t, observer)

	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := observer.send(PeerAddressRequest{Username: targetUser}); err != nil {
			t.Fatal(err)
		}
		payload := readSoulfindCommand(t, observer.Conn(), ServerGetPeerAddress)
		peer, err := DecodePeerAddress(payload)
		if err != nil {
			t.Fatal(err)
		}
		if peer.Username == targetUser && peer.Port == uint32(listener.Port) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("peer address: %+v, want user=%s port=%d", peer, targetUser, listener.Port)
		}
		time.Sleep(50 * time.Millisecond)
	}

	deadline = time.Now().Add(5 * time.Second)
	for {
		watch := requestSoulfindWatch(t, observer, targetUser)
		if watch.username == targetUser && watch.exists && watch.status == 2 && watch.files == 1 && watch.folders == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("watch user: %+v, want user=%s exists=true status=2 files=1 folders=1", watch, targetUser)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestSoulfindPeerFeatures(t *testing.T) {
	addr := os.Getenv("OTO_SOULFIND_ADDR")
	if addr == "" {
		t.Skip("OTO_SOULFIND_ADDR is unset")
	}

	stamp := time.Now().UnixNano()
	filename := fmt.Sprintf("feature-%d.flac", stamp)
	contents := []byte("Soulfind integration transfer")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, filename), contents, 0600); err != nil {
		t.Fatal(err)
	}
	shares := NewShareIndex()
	if err := shares.AddRoot("Music", root); err != nil {
		t.Fatal(err)
	}
	if err := shares.Scan(); err != nil {
		t.Fatal(err)
	}

	targetUser, observerUser := fmt.Sprintf("p%d", stamp), fmt.Sprintf("q%d", stamp)
	target := NewClient(ClientConfig{Address: addr, Username: targetUser, Password: "pw", ListenAddr: "0.0.0.0:0", Share: shares})
	observer := NewClient(ClientConfig{Address: addr, Username: observerUser, Password: "pw", ListenAddr: "0.0.0.0:0"})
	t.Cleanup(func() { _ = target.Close() })
	t.Cleanup(func() { _ = observer.Close() })
	connectSoulfind(t, target)
	connectSoulfind(t, observer)
	runSoulfind(target)
	observerRun := runSoulfind(observer)

	t.Run("wrong password", func(t *testing.T) {
		username := fmt.Sprintf("w%d", stamp)
		registered := NewClient(ClientConfig{Address: addr, Username: username, Password: "pw", ListenAddr: "127.0.0.1:0"})
		t.Cleanup(func() { _ = registered.Close() })
		connectSoulfind(t, registered)
		if err := registered.Close(); err != nil {
			t.Fatal(err)
		}

		client := NewClient(ClientConfig{Address: addr, Username: username, Password: "wrong", ListenAddr: "127.0.0.1:0"})
		t.Cleanup(func() { _ = client.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.Connect(ctx); err != nil {
			t.Fatal(err)
		}
		if err := client.Login(ctx); err == nil || !strings.Contains(err.Error(), "INVALIDPASS") {
			t.Fatalf("wrong password login: %v", err)
		}
	})

	t.Run("search", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		results, err := observer.Search(ctx, filename)
		if err != nil {
			t.Fatal(err)
		}
		want := "Music\\" + filename
		for _, result := range results {
			if result.Path == want && result.Size == uint64(len(contents)) {
				return
			}
		}
		t.Fatalf("search results: %+v, want %s", results, want)
	})

	browse := func(t *testing.T) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		entries, err := observer.BrowseUser(ctx, targetUser, "Music")
		if err != nil {
			t.Fatal(err)
		}
		want := "Music\\" + filename
		if len(entries) != 2 || entries[0].Name != "Music" || !entries[0].Directory || entries[1].Name != want || entries[1].Size != uint64(len(contents)) || entries[1].Directory {
			t.Fatalf("browse entries: %+v", entries)
		}
	}
	t.Run("browse", browse)

	t.Run("transfer", func(t *testing.T) {
		destination, err := os.CreateTemp(t.TempDir(), "download-")
		if err != nil {
			t.Fatal(err)
		}
		defer destination.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := observer.Download(ctx, targetUser, "Music\\"+filename, uint64(len(contents)), 0, destination, nil); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(contents))
		if _, err := destination.ReadAt(got, 0); err != nil {
			t.Fatal(err)
		}
		if string(got) != string(contents) {
			t.Fatalf("downloaded %q, want %q", got, contents)
		}
	})

	t.Run("reconnect", func(t *testing.T) {
		if err := observer.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-observerRun:
		case <-time.After(5 * time.Second):
			t.Fatal("old client run loop did not stop")
		}
		connectSoulfind(t, observer)
		observerRun = runSoulfind(observer)
		browse(t)
	})
}

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
	target := startSoulfindClient(t, addr, "u"+stamp, map[string][]byte{firstName: firstContents, secondName: secondContents}, NewUploadManager(1, 1))
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

func TestSoulfindIndirectPeerConnection(t *testing.T) {
	addr := soulfindAddress(t)
	stamp := fmt.Sprintf("%x", time.Now().UnixNano())
	filename := "indirect-" + stamp + ".flac"
	target := startSoulfindClient(t, addr, "i"+stamp, map[string][]byte{filename: []byte("indirect")}, nil)
	observer := startSoulfindClient(t, addr, "j"+stamp, nil, nil)

	target.mu.Lock()
	listener := target.listener
	target.mu.Unlock()
	if listener == nil {
		t.Fatal("target listener is not running")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	entries, err := observer.BrowseUser(ctx, target.cfg.Username, "Music")
	if err != nil {
		t.Fatal(err)
	}
	want := "Music\\" + filename
	if len(entries) != 2 || entries[1].Name != want {
		t.Fatalf("indirect browse: %+v", entries)
	}
}

func TestSoulfindReconnectDuringSearch(t *testing.T) {
	addr := soulfindAddress(t)
	stamp := fmt.Sprintf("%x", time.Now().UnixNano())
	filename := "reconnect-" + stamp + ".flac"
	target := startSoulfindClient(t, addr, "c"+stamp, map[string][]byte{filename: []byte("reconnect")}, nil)
	observer := NewClient(ClientConfig{Address: addr, Username: "d" + stamp, Password: "pw", ListenAddr: "0.0.0.0:0"})
	t.Cleanup(func() { _ = observer.Close() })
	connectSoulfind(t, observer)
	runDone := runSoulfind(observer)

	searchCtx, cancelSearch := context.WithCancel(context.Background())
	searchDone := make(chan error, 1)
	go func() {
		_, err := observer.Search(searchCtx, filename)
		searchDone <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		observer.mu.Lock()
		active := len(observer.pending) > 0
		observer.mu.Unlock()
		if active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("search did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := observer.Conn().Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("run loop did not observe connection loss")
	}
	cancelSearch()
	if err := <-searchDone; err == nil {
		t.Fatal("active search survived connection loss")
	}
	_ = observer.Close()
	connectSoulfind(t, observer)
	runSoulfind(observer)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entries, err := observer.BrowseUser(ctx, target.cfg.Username, "Music")
	if err != nil || len(entries) != 2 || entries[1].Name != "Music\\"+filename {
		t.Fatalf("browse after reconnect: %+v %v", entries, err)
	}
}

func TestSoulfindConcurrentSearchTokens(t *testing.T) {
	addr := soulfindAddress(t)
	stamp := fmt.Sprintf("%x", time.Now().UnixNano())
	common := "token" + stamp
	firstName, secondName := common+"-a.flac", common+"-b.flac"
	startSoulfindClient(t, addr, "a"+stamp, map[string][]byte{firstName: []byte("a")}, nil)
	startSoulfindClient(t, addr, "b"+stamp, map[string][]byte{secondName: []byte("bb")}, nil)
	observer := startSoulfindClient(t, addr, "e"+stamp, nil, nil)

	type searchResult struct {
		query   string
		results []SearchResult
		err     error
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	results := make(chan searchResult, 2)
	for _, query := range []string{common, firstName} {
		go func(query string) {
			found, err := observer.Search(ctx, query)
			results <- searchResult{query: query, results: found, err: err}
		}(query)
	}
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		paths := make(map[string]bool)
		for _, found := range result.results {
			paths[found.Path] = true
		}
		if result.query == common {
			if !paths["Music\\"+firstName] || !paths["Music\\"+secondName] {
				t.Fatalf("multi-responder search: %+v", result.results)
			}
		} else if !paths["Music\\"+firstName] || paths["Music\\"+secondName] {
			t.Fatalf("token-isolated search: %+v", result.results)
		}
	}
}

func soulfindAddress(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("OTO_SOULFIND_ADDR")
	if addr == "" {
		t.Skip("OTO_SOULFIND_ADDR is unset")
	}
	return addr
}

func startSoulfindClient(t *testing.T, addr, username string, files map[string][]byte, uploads *UploadManager) *Client {
	t.Helper()
	shares := NewShareIndex()
	if len(files) > 0 {
		root := t.TempDir()
		for name, contents := range files {
			if err := os.WriteFile(filepath.Join(root, name), contents, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := shares.AddRoot("Music", root); err != nil {
			t.Fatal(err)
		}
		if err := shares.Scan(); err != nil {
			t.Fatal(err)
		}
	}
	client := NewClient(ClientConfig{Address: addr, Username: username, Password: "pw", ListenAddr: "0.0.0.0:0", Share: shares, Uploads: uploads})
	t.Cleanup(func() { _ = client.Close() })
	connectSoulfind(t, client)
	runSoulfind(client)
	return client
}

func runSoulfind(client *Client) <-chan error {
	done := make(chan error, 1)
	go func() { done <- client.Run(context.Background()) }()
	return done
}
func connectSoulfind(t *testing.T, client *Client) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := client.Connect(ctx)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("connect to Soulfind: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Login(ctx); err != nil {
		t.Fatalf("login to Soulfind: %v", err)
	}
}

func readSoulfindCommand(t *testing.T, conn net.Conn, wanted uint32) []byte {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		command, payload, err := ReadFrame(conn)
		if err != nil {
			t.Fatalf("read Soulfind command %d: %v", wanted, err)
		}
		if command == wanted {
			return payload
		}
	}
}

func requestSoulfindWatch(t *testing.T, client *Client, username string) soulfindWatch {
	t.Helper()
	var request Encoder
	if err := request.String(username); err != nil {
		t.Fatal(err)
	}
	if err := client.send(RawMessage{Command: soulfindWatchUserCommand, Payload: request.Payload()}); err != nil {
		t.Fatal(err)
	}

	d := NewDecoder(readSoulfindCommand(t, client.Conn(), soulfindWatchUserCommand))
	watch := soulfindWatch{username: mustSoulfindValue(t, d.String), exists: mustSoulfindValue(t, d.Bool)}
	if !watch.exists {
		if err := d.Done(); err != nil {
			t.Fatal(err)
		}
		return watch
	}
	watch.status = mustSoulfindValue(t, d.U32)
	_ = mustSoulfindValue(t, d.U32) // upload speed
	_ = mustSoulfindValue(t, d.U32) // obsolete upload count
	_ = mustSoulfindValue(t, d.U32) // obsolete unknown value
	watch.files = mustSoulfindValue(t, d.U32)
	watch.folders = mustSoulfindValue(t, d.U32)
	if watch.status > 0 {
		_ = mustSoulfindValue(t, d.String) // obsolete country code
	}
	if err := d.Done(); err != nil {
		t.Fatal(err)
	}
	return watch
}

func mustSoulfindValue[T any](t *testing.T, read func() (T, error)) T {
	t.Helper()
	value, err := read()
	if err != nil {
		t.Fatal(err)
	}
	return value
}
