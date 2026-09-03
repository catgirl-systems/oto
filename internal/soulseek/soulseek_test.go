package soulseek

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCodecMalformedAndCompressionLimit(t *testing.T) {
	var e Encoder
	if err := e.String("héllo"); err != nil {
		t.Fatal(err)
	}
	d := NewDecoder(e.Payload())
	got, err := d.String()
	if err != nil || got != "héllo" {
		t.Fatalf("decode=%q %v", got, err)
	}
	if _, err := NewDecoder([]byte{4, 0, 0}).String(); err != ErrTruncated {
		t.Fatalf("short length: %v", err)
	}
	if _, err := NewDecoder([]byte{2, 0, 0, 0, 'x'}).String(); err != ErrTruncated {
		t.Fatalf("short value: %v", err)
	}
	var frame bytes.Buffer
	frame.Write([]byte{0xff, 0xff, 0xff, 0x7f})
	if _, _, err := ReadFrame(&frame); err != ErrTooLarge {
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

func TestShareScanSearchAndContainment(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "music")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Beyoncé.mp3"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".hidden"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(d, "outside"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	s := NewShareIndex()
	if err := s.AddRoot("Songs", root); err != nil {
		t.Fatal(err)
	}
	if err := s.ScanContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if counts := sharedCounts(s); counts != (SharedCounts{Folders: 1, Files: 2}) {
		t.Fatalf("shared counts: %+v", counts)
	}
	got := s.Search("Beyoncé -secret")
	if len(got) != 1 || got[0].Path != "Beyoncé.mp3" {
		t.Fatalf("search: %+v", got)
	}
	if _, err := s.Resolve("Songs/../outside"); err == nil {
		t.Fatal("traversal accepted")
	}
	if _, err := s.Resolve("Songs/link"); err == nil {
		t.Fatal("symlink accepted")
	}
	entries, err := s.Browse("Songs")
	if err != nil || len(entries) != 2 {
		t.Fatalf("browse %v %+v", err, entries)
	}
}

func TestBrowseUsesBoundedSnapshotChildren(t *testing.T) {
	root := t.TempDir()
	album := filepath.Join(root, "Album")
	if err := os.Mkdir(album, 0700); err != nil {
		t.Fatal(err)
	}
	song := filepath.Join(album, "song.flac")
	if err := os.WriteFile(song, []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cover.jpg"), []byte("jpg"), 0600); err != nil {
		t.Fatal(err)
	}
	index := NewShareIndex()
	if err := index.AddRoot("Music", root); err != nil {
		t.Fatal(err)
	}
	if err := index.ScanContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(song); err != nil {
		t.Fatal(err)
	}
	children, err := index.Browse("Music")
	if err != nil || len(children) != 2 || !children[0].Directory || children[0].Name != "Album" {
		t.Fatalf("root snapshot children: %+v %v", children, err)
	}
	nested, err := index.Browse(`Music\Album`)
	if err != nil || len(nested) != 1 || nested[0].Name != "song.flac" || nested[0].Size != 5 {
		t.Fatalf("nested snapshot children: %+v %v", nested, err)
	}
	for _, path := range []string{"../Music", "/Music", "Missing", "Music/cover.jpg"} {
		if _, err := index.Browse(path); err == nil {
			t.Fatalf("invalid browse path accepted: %q", path)
		}
	}
}

func TestProtocolFixture(t *testing.T) {
	m := LoginRequest{Username: "u", Password: "p", Version: ProtocolVersion, MinorVersion: ProtocolMinor, Hash: "up"}
	b, err := EncodeMessage(m)
	if err != nil {
		t.Fatal(err)
	}
	cmd, p, err := ReadFrame(bytes.NewReader(b))
	if err != nil || cmd != ServerLogin {
		t.Fatalf("frame %d %v", cmd, err)
	}
	got, err := DecodeLoginRequest(p)
	if err != nil || got != m {
		t.Fatalf("login %+v %v", got, err)
	}
	r := SearchResponse{Token: 7, Username: "peer", SlotFree: true, Speed: 42, QueueLength: 3, Results: []SearchResult{{Path: "Songs/a.mp3", Extension: "mp3", Size: 3, Bitrate: 320, Duration: 125, VBR: true, SampleRate: 44100, BitDepth: 24, Public: true}, {Path: "Secret/b.flac", Extension: "flac", Size: 4}}}
	b, err = EncodeMessage(r)
	if err != nil {
		t.Fatal(err)
	}
	cmd, p, err = ReadFrame(bytes.NewReader(b))
	if err != nil || cmd != PeerSearch {
		t.Fatal(err)
	}
	rr, err := DecodeSearchResponse(p)
	if err != nil || rr.Token != 7 || len(rr.Results) != 2 {
		t.Fatalf("search %+v %v", rr, err)
	}
	public, private := rr.Results[0], rr.Results[1]
	if !public.Public || public.Bitrate != 320 || public.Duration != 125 || !public.VBR || public.SampleRate != 44100 || public.BitDepth != 24 || !public.SlotFree || public.Speed != 42 || public.QueueLength != 3 {
		t.Fatalf("public search metadata: %+v", public)
	}
	if private.Public || private.Path != "Secret/b.flac" || !private.SlotFree || private.QueueLength != 3 {
		t.Fatalf("private search result: %+v", private)
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
	if err := os.MkdirAll(filepath.Join(srcRoot, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, name), data, 0600); err != nil {
		t.Fatal(err)
	}
	offset := uint64(5)
	if err := os.MkdirAll(filepath.Join(dstRoot, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstRoot, name), data[:offset], 0600); err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- SendFile(ctx, srcRoot, name, a, uint64(len(data)), offset, nil) }()
	if _, err := ReceiveFile(ctx, dstRoot, name, b, uint64(len(data)), offset, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dstRoot, name))
	if err != nil || string(got) != string(data) {
		t.Fatalf("received %q %v", got, err)
	}
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
			_ = x.String("ok")
			x.U32(0)
			_ = x.String("hash")
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
	if err := c.Login(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.SetStatus(UserStatusAway); err != nil {
		t.Fatal(err)
	}
	if err := c.SetStatus(UserStatusOnline); err != nil {
		t.Fatal(err)
	}
	if err := c.SetStatus(0); err == nil {
		t.Fatal("invalid user status accepted")
	}
	if err := <-server; err != nil {
		t.Fatal(err)
	}
}

func TestDecodeLegacyDownloadRequestWithSize(t *testing.T) {
	var payload Encoder
	payload.U32(0)
	payload.U32(7)
	if err := payload.String("Music\\track.flac"); err != nil {
		t.Fatal(err)
	}
	payload.U64(0)
	request, err := DecodeTransferRequest(payload.Payload())
	if err != nil || request.Direction != 0 || request.Token != 7 || request.Filename != "Music\\track.flac" || request.Size != 0 {
		t.Fatalf("legacy transfer request: %+v %v", request, err)
	}
}

func TestUploadFIFO(t *testing.T) {
	m := NewUploadManager(1)
	a := m.Enqueue("a", TransferRequest{})
	b := m.Enqueue("b", TransferRequest{})
	if err := m.Wait(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if len(m.q) != 1 || m.q[0] != b {
		t.Fatalf("queue: %+v", m.q)
	}
	m.Done("a")
	if err := m.Wait(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	m.Done("b")
}

func TestWireFramingAndDistributedFixtures(t *testing.T) {
	var frame bytes.Buffer
	err := WriteFrame(&frame, 0x01020304, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{4, 0, 0, 0, 4, 3, 2, 1}
	if !bytes.Equal(frame.Bytes(), want) {
		t.Fatalf("frame %x want %x", frame.Bytes(), want)
	}
	var addressPayload Encoder
	_ = addressPayload.String("peer")
	addressPayload.U32(0x7f000001)
	addressPayload.U32(50300)
	addressPayload.U32(0)
	addressPayload.U16(0)
	address, err := DecodePeerAddress(addressPayload.Payload())
	if err != nil || address.IP != "127.0.0.1" {
		t.Fatalf("address %+v %v", address, err)
	}
	query := DistributedSearchQuery{Username: "peer", Token: 9, Query: "one -two"}
	payload, err := query.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeDistributedSearch(payload)
	if err != nil || decoded != query {
		t.Fatalf("distributed: %+v %v", decoded, err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	command, payload, err := ReadFrame(bytes.NewReader(wire))
	if err != nil || command != PeerSharedList {
		t.Fatalf("frame: %d %v", command, err)
	}
	decoded, err := DecodeSharedListResponse(payload)
	if err != nil || len(decoded.Entries) != 4 || decoded.Entries[1].Name != "Music\\Album\\song.mp3" || decoded.Entries[1].Size != 42 || decoded.Entries[1].Bitrate != 320 || decoded.Entries[1].Duration != 125 || !decoded.Entries[1].VBR || decoded.Entries[2].Name != "Locked" || !decoded.Entries[2].Private || decoded.Entries[3].Name != "Locked\\secret.flac" || !decoded.Entries[3].Private {
		t.Fatalf("shared list: %+v %v", decoded, err)
	}
}

func TestSharedListAcceptsLargeLibraries(t *testing.T) {
	const fileCount = 66_869 // Regression: real peers can exceed the old 50,000-entry cap.
	entries := make([]ShareEntry, fileCount)
	for i := range entries {
		entries[i].Name = `Music\file.mp3`
	}
	wire, err := EncodeMessage(SharedListResponse{Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	_, payload, err := ReadFrame(bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSharedListResponse(payload)
	if err != nil || len(decoded.Entries) != fileCount+1 {
		t.Fatalf("large shared list: entries=%d err=%v", len(decoded.Entries), err)
	}
}

func TestFolderResponseAndShareSubtree(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"Album/Disc", "Album/Empty"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "Album", "cover.jpg"), []byte("jpg"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Album", "Disc", "song.flac"), []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	index := NewShareIndex()
	if err := index.AddRoot("Music", root); err != nil {
		t.Fatal(err)
	}
	if err := index.ScanContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	children, err := index.Browse(`Music\Album`)
	if err != nil || len(children) != 3 {
		t.Fatalf("immediate browse changed: %+v %v", children, err)
	}
	entries, err := index.Subtree(`Music\Album`)
	if err != nil || len(entries) != 5 {
		t.Fatalf("subtree: %+v %v", entries, err)
	}

	encoded, err := EncodeMessage(FolderResponse{Token: 9, Path: `Music\Album`, Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	command, payload, err := ReadFrame(bytes.NewReader(encoded))
	if err != nil || command != PeerFolderResponse {
		t.Fatalf("folder frame: %d %v", command, err)
	}
	response, err := DecodeFolderResponse(payload)
	if err != nil || response.Token != 9 || response.Path != `Music\Album` || len(response.Entries) != 5 {
		t.Fatalf("folder response: %+v %v", response, err)
	}
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
		token, _ := d.U32()
		_, _ = d.String()
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
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	file := SearchResult{Path: "song.flac", Size: 42}

	var search Encoder
	_ = search.String("peer")
	search.U32(7)
	search.U32(1)
	_ = file.encode(&search)
	search.Bool(true)
	search.U32(100)
	search.U32(0)
	search.U32(0) // Unknown field; peers may omit the empty private-list count.
	result, err := DecodeSearchResponse(compress(&search))
	if err != nil || len(result.Results) != 1 || !result.Results[0].Public {
		t.Fatalf("public-only search response: %+v %v", result, err)
	}

	var shares Encoder
	shares.U32(1)
	_ = shares.String("Music")
	shares.U32(1)
	_ = file.encode(&shares)
	shares.U32(0) // Unknown field; peers may omit the empty private-list count.
	list, err := DecodeSharedListResponse(compress(&shares))
	if err != nil || len(list.Entries) != 2 || list.Entries[1].Private {
		t.Fatalf("public-only shared list: %+v %v", list, err)
	}
}

func TestFileConnectionReceive(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	file, err := os.OpenFile(filepath.Join(t.TempDir(), "part"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	client := NewClient(ClientConfig{})
	pending := &pendingDownload{size: 4, writer: file, done: make(chan error, 1), ctx: context.Background()}
	client.downloads[7] = pending
	go client.serveFile(left)
	var token [4]byte
	binary.LittleEndian.PutUint32(token[:], 7)
	if _, err := right.Write(token[:]); err != nil {
		t.Fatal(err)
	}
	var offset [8]byte
	if _, err := io.ReadFull(right, offset[:]); err != nil || binary.LittleEndian.Uint64(offset[:]) != 0 {
		t.Fatalf("offset %v %v", offset, err)
	}
	if _, err := right.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := <-pending.done; err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := file.ReadAt(got, 0); err != nil || string(got) != "data" {
		t.Fatalf("file %q %v", got, err)
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
	if err != nil || command != ServerGetPeerAddress {
		t.Fatalf("peer lookup request: command=%d err=%v", command, err)
	}
	time.Sleep(20 * time.Millisecond)
	client.route(ServerGetPeerAddress, PeerAddress{Username: "peer", IP: "0.0.0.0"})
	for range callers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	_ = serverConn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, _, err := ReadFrame(serverConn); err == nil {
		t.Fatal("concurrent lookups sent more than one server request")
	}
}
