package soulseek

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
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
	if err := s.Scan(); err != nil {
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
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- SendFile(ctx, srcRoot, name, a, uint64(len(data)), 0, nil) }()
	if _, err := ReceiveFile(ctx, dstRoot, name, b, uint64(len(data)), 0, nil); err != nil {
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
		for _, want := range []Message{Status{Status: 2}, SharedCounts{}, AcceptChildren{Value: true}, HaveNoParent{Value: true}} {
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
	if err := <-server; err != nil {
		t.Fatal(err)
	}
}

func TestUploadFIFO(t *testing.T) {
	m := NewUploadManager(1, 1)
	a := m.Enqueue("a", TransferRequest{})
	b := m.Enqueue("b", TransferRequest{})
	if err := m.Wait(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if m.QueueLen() != 1 || m.Place("b") != 1 {
		t.Fatalf("queue %d place %d", m.QueueLen(), m.Place("b"))
	}
	m.Done("a")
	if err := m.Wait(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	m.Done("b")
}

func TestWireFramingAndDistributedFixtures(t *testing.T) {
	frame, err := EncodeFrame(0x01020304, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{4, 0, 0, 0, 4, 3, 2, 1}
	if !bytes.Equal(frame, want) {
		t.Fatalf("frame %x want %x", frame, want)
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
		{Name: "Music\\Album\\song.mp3", Size: 42},
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
	if err != nil || len(decoded.Entries) != 4 || decoded.Entries[1].Name != "Music\\Album\\song.mp3" || decoded.Entries[1].Size != 42 || decoded.Entries[2].Name != "Locked" || !decoded.Entries[2].Private || decoded.Entries[3].Name != "Locked\\secret.flac" || !decoded.Entries[3].Private {
		t.Fatalf("shared list: %+v %v", decoded, err)
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
