package soulseek

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

type remoteAddressConn struct {
	net.Conn
	remote net.Addr
}

func (c remoteAddressConn) RemoteAddr() net.Addr { return c.remote }

func TestCodecMalformedAndCompressionLimit(t *testing.T) {
	var e Encoder
	e.String("héllo")
	d := NewDecoder(e.Payload())
	got := d.String()
	failIfFmt(t, d.Err() != nil || got != "héllo", "decode=%q %v", got, d.Err())
	short := NewDecoder([]byte{4, 0, 0})
	_ = short.String()
	if err := short.Done(); err != ErrTruncated {
		t.Fatalf("short length: %v", err)
	}
	shortValue := NewDecoder([]byte{2, 0, 0, 0, 'x'})
	_ = shortValue.String()
	if err := shortValue.Done(); err != ErrTruncated {
		t.Fatalf("short value: %v", err)
	}
	var frame bytes.Buffer
	frame.Write([]byte{0xff, 0xff, 0xff, 0x7f})
	if _, _, err := ReadFrame(&frame); !errors.Is(err, ErrTooLarge) || !strings.Contains(err.Error(), "frame has 2147483647 bytes (limit 67108864)") {
		t.Fatalf("frame limit: %v", err)
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	_, _ = zw.Write(bytes.Repeat([]byte{'x'}, 1024))
	_ = zw.Close()
	old := MaxDecompressedSize
	_ = old // keep the test independent of a mutable package constant
	if out, err := DecompressZlib(z.Bytes()); err != nil || len(out) != 1024 {
		t.Fatalf("zlib %d %v", len(out), err)
	}
	if _, err := DecompressZlib([]byte("not zlib")); err == nil {
		t.Fatal("invalid zlib accepted")
	}
}

func TestStickyCodecKeepsFirstError(t *testing.T) {
	d := NewDecoder([]byte{1, 0, 0})
	if v := d.U8(); v != 1 {
		t.Fatal(v)
	}
	d.U32()      // Truncates here.
	_ = d.String() // No-op after the first error.
	if err := d.Done(); err != ErrTruncated {
		t.Fatalf("sticky first error: %v", err)
	}
	var e Encoder
	e.String("ok")
	e.Bytes(make([]byte, MaxBytesSize+1))
	e.String("still ok")
	if err := e.Err(); err != ErrTooLarge {
		t.Fatalf("sticky encode error: %v", err)
	}
}

func TestFrameTransportErrors(t *testing.T) {
	for _, init := range []bool{false, true} {
		var wire bytes.Buffer
		if init {
			_ = WriteInitFrame(&wire, byte(PeerInit), []byte("abc"))
		} else {
			_ = WriteFrame(&wire, PeerTransferRequest, []byte("abc"))
		}
		data := wire.Bytes()
		timeout := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
		for _, tc := range []struct {
			name string
			data []byte
			tail error
			want error
		}{
			{"complete", data, nil, nil},
			{"closed", nil, nil, io.EOF},
			{"partial_length", data[:2], nil, io.ErrUnexpectedEOF},
			{"missing_body", data[:4], nil, io.ErrUnexpectedEOF},
			{"partial_body", data[:len(data)-1], nil, io.ErrUnexpectedEOF},
			{"header_timeout", nil, timeout, timeout},
			{"body_timeout", data[:len(data)-1], timeout, timeout},
			{"header_closed", nil, net.ErrClosed, net.ErrClosed},
			{"body_closed", data[:len(data)-1], net.ErrClosed, net.ErrClosed},
		} {
			t.Run(fmt.Sprintf("init=%t/%s", init, tc.name), func(t *testing.T) {
				var reader io.Reader = bytes.NewReader(tc.data)
				if tc.tail != nil {
					reader = io.MultiReader(reader, iotest.ErrReader(tc.tail))
				}
				reader = iotest.OneByteReader(reader)
				var err error
				if init {
					_, _, err = ReadInitFrame(reader)
				} else {
					_, _, err = ReadFrameWithProgress(reader, func(uint64, uint64) {})
				}
				failIfFmt(t, !errors.Is(err, tc.want) || errors.Is(err, ErrTruncated), "transport error: got %v, want %v", err, tc.want)
			})
		}
	}
}

func TestReadFrameWithProgress(t *testing.T) {
	var wire bytes.Buffer
	must(t, WriteFrame(&wire, PeerSharedList, []byte("abc")))
	var updates [][2]uint64
	command, payload, err := ReadFrameWithProgress(iotest.OneByteReader(bytes.NewReader(wire.Bytes())), func(received, total uint64) {
		updates = append(updates, [2]uint64{received, total})
	})
	failIfFmt(t, err != nil || command != PeerSharedList || string(payload) != "abc", "frame: command=%d payload=%q err=%v", command, payload, err)
	if len(updates) < 3 || updates[0] != [2]uint64{0, 7} || updates[len(updates)-1] != [2]uint64{7, 7} {
		t.Fatalf("progress updates: %v", updates)
	}
	for i := 1; i < len(updates); i++ {
		failIfFmt(t, updates[i][0] <= updates[i-1][0] || updates[i][1] != 7, "non-monotonic progress: %v", updates)
	}
}

func TestShareScanSearchAndContainment(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "music")
	must(t, os.Mkdir(root, 0700))
	must(t, os.WriteFile(filepath.Join(root, "Beyoncé.mp3"), []byte("x"), 0600))
	must(t, os.WriteFile(filepath.Join(root, "secret"), []byte("x"), 0600))
	must(t, os.WriteFile(filepath.Join(root, ".hidden"), []byte("x"), 0600))
	must(t, os.Symlink(filepath.Join(d, "outside"), filepath.Join(root, "link")))
	s := NewShareIndex()
	must(t, s.AddRoot("Songs", root))
	must(t, s.ScanContext(context.Background()))
	if counts := sharedCounts(s); counts != (SharedCounts{Folders: 1, Files: 2}) {
		t.Fatalf("shared counts: %+v", counts)
	}
	got := s.Search("Beyoncé -secret", 500)
	failIfFmt(t, len(got) != 1 || got[0].Path != "Beyoncé.mp3", "search: %+v", got)
	if _, err := s.Resolve("Songs/../outside"); err == nil {
		t.Fatal("traversal accepted")
	}
	if _, err := s.Resolve("Songs/link"); err == nil {
		t.Fatal("symlink accepted")
	}
	entries, err := s.Browse("Songs")
	failIfFmt(t, err != nil || len(entries) != 2, "browse %v %+v", err, entries)
}

func TestRestoreShareIndexValidatesCachedFiles(t *testing.T) {
	root := t.TempDir()
	roots := []ShareRoot{{Name: "Music", Path: root}}
	index, err := RestoreShareIndex(roots, []ShareFile{
		{Root: "Music", Path: "Album", Directory: true},
		{Root: "Music", Path: "Album/song.flac", Size: 5},
	})
	must(t, err)
	entries, err := index.Browse("Music/Album")
	failIfFmt(t, err != nil || len(entries) != 1 || entries[0].Name != "song.flac" || entries[0].Size != 5, "restored entries: %+v %v", entries, err)

	for name, file := range map[string]ShareFile{
		"unknown root":     {Root: "Other", Path: "song.flac"},
		"traversal":        {Root: "Music", Path: "../song.flac"},
		"absolute":         {Root: "Music", Path: "/song.flac"},
		"Windows absolute": {Root: "Music", Path: "C:/song.flac"},
		"unnormalized":     {Root: "Music", Path: `Album\song.flac`},
		"null byte":        {Root: "Music", Path: "song\x00.flac"},
		"empty file":       {Root: "Music"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := RestoreShareIndex(roots, []ShareFile{file}); err == nil {
				t.Fatal("invalid cached entry accepted")
			}
		})
	}
}

func TestBrowseUsesBoundedSnapshotChildren(t *testing.T) {
	root := t.TempDir()
	album := filepath.Join(root, "Album")
	must(t, os.Mkdir(album, 0700))
	song := filepath.Join(album, "song.flac")
	must(t, os.WriteFile(song, []byte("audio"), 0600))
	must(t, os.WriteFile(filepath.Join(root, "cover.jpg"), []byte("jpg"), 0600))
	index := NewShareIndex()
	must(t, index.AddRoot("Music", root))
	must(t, index.ScanContext(context.Background()))
	must(t, os.Remove(song))
	children, err := index.Browse("Music")
	failIfFmt(t, err != nil || len(children) != 2 || !children[0].Directory || children[0].Name != "Album", "root snapshot children: %+v %v", children, err)
	nested, err := index.Browse(`Music\Album`)
	failIfFmt(t, err != nil || len(nested) != 1 || nested[0].Name != "song.flac" || nested[0].Size != 5, "nested snapshot children: %+v %v", nested, err)
	for _, path := range []string{"../Music", "/Music", "Missing", "Music/cover.jpg"} {
		if _, err := index.Browse(path); err == nil {
			t.Fatalf("invalid browse path accepted: %q", path)
		}
	}
}

func TestProtocolFixture(t *testing.T) {
	m := LoginRequest{Username: "u", Password: "p", Version: ProtocolVersion, MinorVersion: ProtocolMinor, Hash: "up"}
	b, err := EncodeMessage(m)
	must(t, err)
	cmd, p, err := ReadFrame(bytes.NewReader(b))
	failIfFmt(t, err != nil || cmd != ServerLogin, "frame %d %v", cmd, err)
	got, err := DecodeLoginRequest(p)
	failIfFmt(t, err != nil || got != m, "login %+v %v", got, err)
	r := SearchResponse{Token: 7, Username: "peer", SlotFree: true, Speed: 42, QueueLength: 3, Results: []SearchResult{{Path: "Songs/a.mp3", Extension: "mp3", Size: 3, Bitrate: 320, Duration: 125, VBR: true, SampleRate: 44100, BitDepth: 24, Public: true}, {Path: "Secret/b.flac", Extension: "flac", Size: 4}}}
	b, err = EncodeMessage(r)
	must(t, err)
	cmd, p, err = ReadFrame(bytes.NewReader(b))
	failIf(t, err != nil || cmd != PeerSearch, err)
	rr, err := DecodeSearchResponse(p)
	failIfFmt(t, err != nil || rr.Token != 7 || len(rr.Results) != 2, "search %+v %v", rr, err)
	public, private := rr.Results[0], rr.Results[1]
	failIfFmt(t, !public.Public || public.Bitrate != 320 || public.Duration != 125 || !public.VBR || public.SampleRate != 44100 || public.BitDepth != 24 || !public.SlotFree || public.Speed != 42 || public.QueueLength != 3, "public search metadata: %+v", public)
	failIfFmt(t, private.Public || private.Path != "Secret/b.flac" || !private.SlotFree || private.QueueLength != 3, "private search result: %+v", private)
}

func TestSearchResponseCountry(t *testing.T) {
	client := NewClient(ClientConfig{})
	responses := make(chan SearchResponse, 1)
	client.pending[7] = responses

	reader, writer := net.Pipe()
	peer := remoteAddressConn{Conn: reader, remote: &net.TCPAddr{IP: net.ParseIP("1.0.0.1"), Port: 1234}}
	done := make(chan struct{})
	go func() {
		client.serveMessagePeer(peer, PeerInitMessage{Username: "peer", Type: "P"})
		close(done)
	}()

	message, err := EncodeMessage(SearchResponse{Token: 7, Username: "peer", Results: []SearchResult{{Path: "song.flac"}}})
	must(t, err)
	if _, err := writer.Write(message); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	<-done

	response := <-responses
	if got := response.Results[0].CountryCode; got != "AU" {
		t.Fatalf("country code = %q, want AU", got)
	}
	if got := countryCodeForAddress(&net.IPAddr{IP: net.ParseIP("1.0.0.1")}); got != "" {
		t.Fatalf("non-TCP address country = %q", got)
	}
}

func TestTransferPathAndPipe(t *testing.T) {
	if _, err := NormalizePath("../x"); err == nil {
		t.Fatal("traversal")
	}
	if _, err := NormalizePath("/x"); err == nil {
		t.Fatal("absolute")
	}
	if _, err := SafeJoin(t.TempDir(), "a/../../x"); err == nil {
		t.Fatal("join traversal")
	}
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	name := "nested/file.bin"
	data := []byte("hello soulseek")
	must(t, os.MkdirAll(filepath.Join(srcRoot, "nested"), 0700))
	must(t, os.WriteFile(filepath.Join(srcRoot, name), data, 0600))
	offset := uint64(5)
	must(t, os.MkdirAll(filepath.Join(dstRoot, "nested"), 0700))
	must(t, os.WriteFile(filepath.Join(dstRoot, name), data[:offset], 0600))
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- SendFile(ctx, srcRoot, name, a, uint64(len(data)), offset, nil) }()
	_, err := ReceiveFile(ctx, dstRoot, name, b, uint64(len(data)), offset, nil)
	must(t, err)
	must(t, <-done)
	got, err := os.ReadFile(filepath.Join(dstRoot, name))
	failIfFmt(t, err != nil || string(got) != string(data), "received %q %v", got, err)
}

func TestPipeLogin(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	c := NewClientOnConn(ClientConfig{Username: "alice", Password: "pw"}, a)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	server := make(chan error, 1)
	go func() {
		cmd, p, e := ReadFrame(b)
		if e == nil && cmd != ServerLogin {
			e = fmt.Errorf("login command: %d", cmd)
		}
		if e == nil {
			var m LoginRequest
			m, e = DecodeLoginRequest(p)
			if e == nil && (m.Version != 170 || m.MinorVersion != 2718 || m.Hash != "15da1f78ad7d474862865bab1aab4d51") {
				e = fmt.Errorf("login values: %+v", m)
			}
		}
		if e == nil {
			var x Encoder
			x.Bool(true)
			x.String("ok")
			x.U32(0x01020304)
			x.String("hash")
			x.Bool(false)
			e = WriteFrame(b, ServerLogin, x.Payload())
		}
		for _, want := range []Message{Status{Status: uint32(UserStatusOnline)}, SharedCounts{}, AcceptChildren{Value: true}, HaveNoParent{Value: true}, Status{Status: uint32(UserStatusAway)}, Status{Status: uint32(UserStatusOnline)}} {
			if e != nil {
				break
			}
			cmd, payload, err := ReadFrame(b)
			var encoded Encoder
			if encodeErr := want.encode(&encoded); encodeErr != nil {
				e = encodeErr
			} else if err != nil {
				e = err
			} else if cmd != want.command() || !bytes.Equal(payload, encoded.Payload()) {
				e = fmt.Errorf("post-login frame: command=%d payload=%x", cmd, payload)
			}
		}
		server <- e
	}()
	must(t, c.Login(ctx))
	if got := c.PublicIP(); got != "1.2.3.4" {
		t.Fatalf("public IP = %q, want 1.2.3.4", got)
	}
	must(t, c.SetStatus(UserStatusAway))
	must(t, c.SetStatus(UserStatusOnline))
	if err := c.SetStatus(0); err == nil {
		t.Fatal("invalid user status accepted")
	}
	must(t, <-server)
	must(t, c.Close())
	if got := c.PublicIP(); got != "" {
		t.Fatalf("public IP after close = %q, want empty", got)
	}
}

func TestChangePassword(t *testing.T) {
	message := ChangePassword{Password: " new secret "}
	frame, err := EncodeMessage(message)
	must(t, err)
	command, payload, err := ReadFrame(bytes.NewReader(frame))
	decoded, decodeErr := DecodeMessage(command, payload)
	failIfFmt(t, err != nil || decodeErr != nil || command != ServerChangePassword || decoded != message, "password frame: command=%d message=%#v errors=%v/%v", command, decoded, err, decodeErr)

	t.Run("matching acknowledgement ignores stale response", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer clientConn.Close()
		defer serverConn.Close()
		client := NewClientOnConn(ClientConfig{Username: "alice", Password: "old"}, clientConn)
		client.loggedIn, client.done = true, make(chan struct{})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		runDone := make(chan error, 1)
		go func() { runDone <- client.Run(ctx) }()
		serverDone := make(chan error, 1)
		go func() {
			command, payload, err := ReadFrame(serverConn)
			if err == nil && command != ServerChangePassword {
				err = fmt.Errorf("password command: %d", command)
			}
			if err == nil {
				request, decodeErr := DecodeChangePassword(payload)
				err = decodeErr
				if err == nil && request.Password != message.Password {
					err = fmt.Errorf("password payload: %q", request.Password)
				}
			}
			if err == nil {
				stale, _ := EncodeMessage(ChangePassword{Password: "stale"})
				_, err = serverConn.Write(stale)
			}
			if err == nil {
				ack, _ := EncodeMessage(message)
				_, err = serverConn.Write(ack)
			}
			serverDone <- err
		}()
		must(t, client.ChangePassword(ctx, message.Password))
		failIf(t, client.cfg.Password != message.Password, "client credential was not updated")
		must(t, <-serverDone)
		cancel()
		_ = clientConn.Close()
		<-runDone
	})

	t.Run("concurrent request is rejected", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer clientConn.Close()
		defer serverConn.Close()
		client := NewClientOnConn(ClientConfig{}, clientConn)
		client.loggedIn, client.done = true, make(chan struct{})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		go client.Run(ctx)
		requestRead := make(chan error, 1)
		go func() {
			command, _, err := ReadFrame(serverConn)
			if err == nil && command != ServerChangePassword {
				err = fmt.Errorf("password command: %d", command)
			}
			requestRead <- err
		}()
		first := make(chan error, 1)
		go func() { first <- client.ChangePassword(ctx, "first") }()
		must(t, <-requestRead)
		if err := client.ChangePassword(ctx, "second"); err == nil || !strings.Contains(err.Error(), "already in progress") {
			t.Fatalf("concurrent password change: %v", err)
		}
		ack, _ := EncodeMessage(ChangePassword{Password: "first"})
		_, err := serverConn.Write(ack)
		must(t, err)
		must(t, <-first)
	})

	t.Run("cancellation and connection closure", func(t *testing.T) {
		for _, closeConnection := range []bool{false, true} {
			clientConn, serverConn := net.Pipe()
			client := NewClientOnConn(ClientConfig{}, clientConn)
			client.loggedIn, client.done = true, make(chan struct{})
			runCtx, stopRun := context.WithCancel(context.Background())
			go client.Run(runCtx)
			requestRead := make(chan struct{})
			go func() {
				_, _, _ = ReadFrame(serverConn)
				close(requestRead)
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			result := make(chan error, 1)
			go func() { result <- client.ChangePassword(ctx, "new") }()
			<-requestRead
			if closeConnection {
				_ = serverConn.Close()
			}
			err := <-result
			failIfFmt(t, closeConnection && (err == nil || !strings.Contains(err.Error(), "connection closed")), "connection closure: %v", err)
			failIfFmt(t, !closeConnection && !errors.Is(err, context.DeadlineExceeded), "cancellation: %v", err)
			cancel()
			stopRun()
			_ = clientConn.Close()
			_ = serverConn.Close()
		}
	})
}

func TestDecodeLegacyDownloadRequestWithSize(t *testing.T) {
	var payload Encoder
	payload.U32(0)
	payload.U32(7)
	payload.String("Music\\track.flac")
	payload.U64(0)
	request, err := DecodeTransferRequest(payload.Payload())
	failIfFmt(t, err != nil || request.Direction != 0 || request.Token != 7 || request.Filename != "Music\\track.flac" || request.Size != 0, "legacy transfer request: %+v %v", request, err)
}

func TestUploadFIFO(t *testing.T) {
	m := NewUploadManager(1)
	a := m.Enqueue("a", TransferRequest{})
	b := m.Enqueue("b", TransferRequest{})
	must(t, m.Wait(context.Background(), a))
	failIfFmt(t, len(m.q) != 1 || m.q[0] != b, "queue: %+v", m.q)
	m.Done(a)
	must(t, m.Wait(context.Background(), b))
	m.Done(b)
}

func uploadReady(job *UploadJob) bool {
	select {
	case <-job.Ready:
		return true
	default:
		return false
	}
}

func TestUploadFIFOUsesEligibleSlots(t *testing.T) {
	m := NewUploadManager(2)
	first := m.Enqueue("a", TransferRequest{})
	blocked := m.Enqueue("a", TransferRequest{})
	other := m.Enqueue("b", TransferRequest{})
	failIfFmt(t, !uploadReady(first) || uploadReady(blocked) || !uploadReady(other), "eligible FIFO jobs: first=%v blocked=%v other=%v", uploadReady(first), uploadReady(blocked), uploadReady(other))
	m.Done(first)
	failIf(t, !uploadReady(blocked), "blocked user was not promoted when its prior upload finished")
	m.Done(other)
	m.Done(blocked)
}

func TestUploadRoundRobinUsers(t *testing.T) {
	m := NewUploadManager(1)
	m.Configure(UploadPolicy{Scheduling: UploadScheduleRoundRobin})
	a1 := m.Enqueue("a", TransferRequest{})
	a2 := m.Enqueue("a", TransferRequest{})
	b1 := m.Enqueue("b", TransferRequest{})
	b2 := m.Enqueue("b", TransferRequest{})
	c1 := m.Enqueue("c", TransferRequest{})
	for _, job := range []*UploadJob{a1, b1, c1, a2, b2} {
		failIfFmt(t, !uploadReady(job), "round-robin did not promote %q", job.User)
		m.Done(job)
	}
}

func TestUploadRandomChoosesEligibleUser(t *testing.T) {
	for seed := uint64(0); seed < 20; seed++ {
		m := NewUploadManager(1)
		m.Configure(UploadPolicy{Scheduling: UploadScheduleRandom})
		blocker := m.Enqueue("blocker", TransferRequest{})
		m.random = rand.NewPCG(seed, seed+1)
		a := m.Enqueue("a", TransferRequest{Filename: "a"})
		b1 := m.Enqueue("b", TransferRequest{Filename: "first"})
		b2 := m.Enqueue("b", TransferRequest{Filename: "second"})
		random := *m.random
		selected := rand.New(&random).IntN(2) // Two users, not three files.
		m.Done(blocker)
		failIfFmt(t, uploadReady(a) != (selected == 0) || uploadReady(b1) != (selected == 1) || uploadReady(b2), "seed %d: random scheduler did not choose the selected user's oldest file", seed)
		m.Done(a)
		m.Done(b1)
		m.Done(b2)
	}
}

func TestUploadSmallestFirstAndArrivalTie(t *testing.T) {
	m := NewUploadManager(1)
	m.Configure(UploadPolicy{Scheduling: UploadScheduleSmallestFirst})
	blocker := m.Enqueue("blocker", TransferRequest{})
	large := m.Enqueue("large", TransferRequest{Size: 100})
	firstSmall := m.Enqueue("small-a", TransferRequest{Size: 10})
	secondSmall := m.Enqueue("small-b", TransferRequest{Size: 10})
	m.Done(blocker)
	failIf(t, !uploadReady(firstSmall) || uploadReady(large) || uploadReady(secondSmall), "smallest-first order or arrival tie-break was not preserved")
	m.Done(firstSmall)
	failIf(t, !uploadReady(secondSmall), "second equal-sized upload was not next")
	m.Done(secondSmall)
	m.Done(large)
}

func TestUploadLimiterReservationsAndCancellation(t *testing.T) {
	m := NewUploadManager(1)
	one, two := &UploadJob{}, &UploadJob{}
	now := time.Unix(100, 0)
	reserve := func(job *UploadJob, bytes int) time.Duration {
		delay, _ := m.reserve(job, bytes, now)
		return delay
	}
	m.Configure(UploadPolicy{Scheduling: UploadScheduleFIFO, BytesPerSecond: 1024})
	if got := reserve(one, 1024); got != 0 {
		t.Fatalf("first shared reservation = %v", got)
	}
	if got := reserve(two, 1024); got != time.Second {
		t.Fatalf("second shared reservation = %v", got)
	}

	_, changed := m.reserve(one, 1, now)
	m.Configure(UploadPolicy{Scheduling: UploadScheduleFIFO, BytesPerSecond: 1024, PerTransfer: true})
	select {
	case <-changed:
	default:
		t.Fatal("hot reconfiguration did not wake paced writers")
	}
	if got := reserve(one, 1024); got != 0 {
		t.Fatalf("first per-transfer reservation = %v", got)
	}
	if got := reserve(two, 1024); got != 0 {
		t.Fatalf("independent per-transfer reservation = %v", got)
	}
	if got := reserve(one, 1024); got != time.Second {
		t.Fatalf("second reservation for one transfer = %v", got)
	}

	m.Configure(UploadPolicy{Scheduling: UploadScheduleFIFO, BytesPerSecond: 2048})
	if got := reserve(one, 1024); got != 0 {
		t.Fatalf("hot reconfiguration kept pacing debt: %v", got)
	}
	if got := reserve(two, 1024); got != 500*time.Millisecond {
		t.Fatalf("reconfigured shared reservation = %v", got)
	}
	m.Configure(UploadPolicy{Scheduling: UploadScheduleFIFO})
	if got := reserve(one, 1024); got != 0 {
		t.Fatalf("unlimited reservation = %v", got)
	}

	m.Configure(UploadPolicy{Scheduling: UploadScheduleFIFO, BytesPerSecond: 1024})
	var dst bytes.Buffer
	_, err := m.LimitWriter(context.Background(), one, &dst).Write(make([]byte, 1024))
	must(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.LimitWriter(ctx, one, &dst).Write([]byte{1}); !errors.Is(err, ErrTransferCancelled) {
		t.Fatalf("cancelled limiter write = %v", err)
	}
}

func TestWireFramingAndDistributedFixtures(t *testing.T) {
	var frame bytes.Buffer
	err := WriteFrame(&frame, 0x01020304, nil)
	must(t, err)
	want := []byte{4, 0, 0, 0, 4, 3, 2, 1}
	failIfFmt(t, !bytes.Equal(frame.Bytes(), want), "frame %x want %x", frame.Bytes(), want)
	var addressPayload Encoder
	addressPayload.String("peer")
	addressPayload.U32(0x7f000001)
	addressPayload.U32(50300)
	addressPayload.U32(0)
	addressPayload.U16(0)
	address, err := DecodePeerAddress(addressPayload.Payload())
	failIfFmt(t, err != nil || address.IP != "127.0.0.1", "address %+v %v", address, err)
	query := DistributedSearchQuery{Username: "peer", Token: 9, Query: "one -two"}
	payload, err := query.MarshalBinary()
	must(t, err)
	decoded, err := DecodeDistributedSearch(payload)
	failIfFmt(t, err != nil || decoded != query, "distributed: %+v %v", decoded, err)
	payload[0] = 48
	if _, err := DecodeDistributedSearch(payload); err == nil {
		t.Fatal("accepted invalid distributed identifier")
	}
}

func TestSharedListRoundTrip(t *testing.T) {
	message := SharedListResponse{Entries: []ShareEntry{
		{Name: "Music\\Album\\song.mp3", Size: 42, Extension: "mp3", Bitrate: 320, Duration: 125, VBR: true},
		{Name: "Locked\\secret.flac", Size: 84, Private: true},
	}}
	wire, err := EncodeMessage(message)
	must(t, err)
	command, payload, err := ReadFrame(bytes.NewReader(wire))
	failIfFmt(t, err != nil || command != PeerSharedList, "frame: %d %v", command, err)
	decoded, err := DecodeSharedListResponse(payload)
	failIfFmt(t, err != nil || len(decoded.Entries) != 4 || decoded.Entries[1].Name != "Music\\Album\\song.mp3" || decoded.Entries[1].Size != 42 || decoded.Entries[1].Bitrate != 320 || decoded.Entries[1].Duration != 125 || !decoded.Entries[1].VBR || decoded.Entries[2].Name != "Locked" || !decoded.Entries[2].Private || decoded.Entries[3].Name != "Locked\\secret.flac" || !decoded.Entries[3].Private, "shared list: %+v %v", decoded, err)
}

func TestSharedListAcceptsLargeLibraries(t *testing.T) {
	const fileCount = 66_869 // Regression: real peers can exceed the old 50,000-entry cap.
	entries := make([]ShareEntry, fileCount)
	for i := range entries {
		entries[i].Name = `Music\file.mp3`
	}
	wire, err := EncodeMessage(SharedListResponse{Entries: entries})
	must(t, err)
	_, payload, err := ReadFrame(bytes.NewReader(wire))
	must(t, err)
	decoded, err := DecodeSharedListResponse(payload)
	failIfFmt(t, err != nil || len(decoded.Entries) != fileCount+1, "large shared list: entries=%d err=%v", len(decoded.Entries), err)
}

func TestFolderResponseAndShareSubtree(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"Album/Disc", "Album/Empty"} {
		must(t, os.MkdirAll(filepath.Join(root, dir), 0700))
	}
	must(t, os.WriteFile(filepath.Join(root, "Album", "cover.jpg"), []byte("jpg"), 0600))
	must(t, os.WriteFile(filepath.Join(root, "Album", "Disc", "song.flac"), []byte("audio"), 0600))
	index := NewShareIndex()
	must(t, index.AddRoot("Music", root))
	must(t, index.ScanContext(context.Background()))
	children, err := index.Browse(`Music\Album`)
	failIfFmt(t, err != nil || len(children) != 3, "immediate browse changed: %+v %v", children, err)
	entries, err := index.Subtree(`Music\Album`)
	failIfFmt(t, err != nil || len(entries) != 5, "subtree: %+v %v", entries, err)

	encoded, err := EncodeMessage(FolderResponse{Token: 9, Path: `Music\Album`, Entries: entries})
	must(t, err)
	command, payload, err := ReadFrame(bytes.NewReader(encoded))
	failIfFmt(t, err != nil || command != PeerFolderResponse, "folder frame: %d %v", command, err)
	response, err := DecodeFolderResponse(payload)
	failIfFmt(t, err != nil || response.Token != 9 || response.Path != `Music\Album` || len(response.Entries) != 5, "folder response: %+v %v", response, err)
	got := make(map[string]ShareEntry, len(response.Entries))
	for _, entry := range response.Entries {
		got[entry.Name] = entry
	}
	for _, name := range []string{`Music\Album`, `Music\Album\cover.jpg`, `Music\Album\Disc`, `Music\Album\Disc\song.flac`, `Music\Album\Empty`} {
		if _, ok := got[name]; !ok {
			t.Fatalf("folder response missing %q: %+v", name, response.Entries)
		}
	}
}

func TestBrowseRejectsMismatchedFolderResponse(t *testing.T) {
	clientConn, peerConn := net.Pipe()
	defer clientConn.Close()
	client := NewClientOnConn(ClientConfig{Username: "u"}, clientConn)
	go func() {
		defer peerConn.Close()
		command, payload, err := ReadFrame(peerConn)
		if err != nil || command != PeerFolderContents {
			return
		}
		d := NewDecoder(payload)
		token := d.U32()
		_ = d.String()
		_ = writeMessage(peerConn, FolderResponse{Token: token, Path: "Other"})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := client.Browse(ctx, clientConn, "Music"); !errors.Is(err, ErrMalformed) {
		t.Fatalf("mismatched response accepted: %v", err)
	}
}

func TestFullBrowseLimitHonorsCancellation(t *testing.T) {
	client := NewClient(ClientConfig{})
	client.browseSlot <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Browse(ctx, nil, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting full browse: %v", err)
	}
	<-client.browseSlot
}

func TestOptionalPrivateLists(t *testing.T) {
	compress := func(raw *Encoder) []byte {
		t.Helper()
		payload, err := CompressZlib(raw.Payload())
		must(t, err)
		return payload
	}
	file := SearchResult{Path: "song.flac", Size: 42}

	var search Encoder
	search.String("peer")
	search.U32(7)
	search.U32(1)
	_ = file.encode(&search)
	search.Bool(true)
	search.U32(100)
	search.U32(0)
	search.U32(0) // Unknown field; peers may omit the empty private-list count.
	result, err := DecodeSearchResponse(compress(&search))
	failIfFmt(t, err != nil || len(result.Results) != 1 || !result.Results[0].Public, "public-only search response: %+v %v", result, err)

	var shares Encoder
	shares.U32(1)
	shares.String("Music")
	shares.U32(1)
	_ = file.encode(&shares)
	shares.U32(0) // Unknown field; peers may omit the empty private-list count.
	list, err := DecodeSharedListResponse(compress(&shares))
	failIfFmt(t, err != nil || len(list.Entries) != 2 || list.Entries[1].Private, "public-only shared list: %+v %v", list, err)
}

func TestBrowseLimitDiagnostics(t *testing.T) {
	for _, kind := range []string{"share list", "private share list", "folder response"} {
		for _, directories := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/directories=%t", kind, directories), func(t *testing.T) {
				var raw Encoder
				switch kind {
				case "private share list":
					raw.U32(0) // Public directories.
					raw.U32(0) // Unknown field.
				case "folder response":
					raw.U32(7) // Token.
					raw.String("Music")
				}
				if directories {
					raw.U32(maxShareEntries + 1)
				} else {
					raw.U32(1)
					raw.String("Music")
					raw.U32(maxShareEntries) // Files plus the directory exceed the limit.
				}
				payload, err := CompressZlib(raw.Payload())
				must(t, err)
				if kind == "folder response" {
					_, err = DecodeFolderResponse(payload)
				} else {
					_, err = DecodeSharedListResponse(payload)
				}
				failIfFmt(t, !errors.Is(err, ErrTooLarge), "expected size limit: %v", err)
				for _, detail := range []string{fmt.Sprint(maxShareEntries + 1), fmt.Sprintf("limit %d", maxShareEntries), "directories"} {
					failIfFmt(t, !strings.Contains(err.Error(), detail), "missing %q in error: %v", detail, err)
				}
			})
		}
	}
}

func TestFileConnectionCancellation(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	file, err := os.OpenFile(filepath.Join(t.TempDir(), "part"), os.O_CREATE|os.O_RDWR, 0600)
	must(t, err)
	defer file.Close()
	ctx, cancel := context.WithCancel(context.Background())
	pending := &pendingDownload{size: 32 << 10, writer: file, done: make(chan error, 1), ctx: ctx}
	client := NewClient(ClientConfig{})
	client.downloads[7] = pending
	go client.serveFile(left)

	var token [4]byte
	binary.LittleEndian.PutUint32(token[:], 7)
	if _, err := right.Write(token[:]); err != nil {
		t.Fatal(err)
	}
	var offset [8]byte
	if _, err := io.ReadFull(right, offset[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := right.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-pending.done:
		failIf(t, err == nil, "cancelled file connection completed")
	case <-time.After(time.Second):
		t.Fatal("cancel did not interrupt file connection")
	}
}

func TestConcurrentPeerAddressLookupsShareRequest(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	client := NewClientOnConn(ClientConfig{}, clientConn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	const callers = 10
	start := make(chan struct{})
	results := make(chan error, callers)
	for range callers {
		go func() {
			<-start
			address, err := client.lookupPeerAddress(ctx, "peer")
			if err == nil && (address.IP != "0.0.0.0" || address.Username != "peer") {
				err = fmt.Errorf("unexpected address: %+v", address)
			}
			results <- err
		}()
	}
	close(start)
	command, _, err := ReadFrame(serverConn)
	failIfFmt(t, err != nil || command != ServerGetPeerAddress, "peer lookup request: command=%d err=%v", command, err)
	time.Sleep(20 * time.Millisecond)
	client.route(ServerGetPeerAddress, PeerAddress{Username: "peer", IP: "0.0.0.0"})
	for range callers {
		must(t, <-results)
	}
	_ = serverConn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, _, err := ReadFrame(serverConn); err == nil {
		t.Fatal("concurrent lookups sent more than one server request")
	}
}
