package soulseek

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"net"
	"testing"
)

func groupedPayload(t *testing.T, raw []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	z := zlib.NewWriter(&out)
	if _, err := z.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func groupedRaw(t *testing.T, private bool) []byte {
	t.Helper()
	var raw Encoder
	raw.U32(1)
	_ = raw.String("Music\\Album")
	raw.U32(1)
	if err := (SearchResult{Path: "disc/song.mp3", Size: 42, Extension: "mp3", Bitrate: 320}).encode(&raw); err != nil {
		t.Fatal(err)
	}
	raw.U32(0) // legacy unknown field
	if private {
		raw.U32(1)
		_ = raw.String("Locked")
		raw.U32(1)
		if err := (SearchResult{Path: "secret.flac", Size: 84, Extension: "flac"}).encode(&raw); err != nil {
			t.Fatal(err)
		}
	}
	return raw.Payload()
}

func TestDecodeSharedListDirectoriesPreservesRelativeNames(t *testing.T) {
	got, err := decodeSharedListDirectories(groupedPayload(t, groupedRaw(t, true)), BrowseLimits{})
	if err != nil || len(got) != 2 || got[0].Name != "Music\\Album" || got[0].Private || len(got[0].Files) != 1 || got[0].Files[0].Name != "disc\\song.mp3" || got[0].Files[0].Private || got[0].Files[0].Bitrate != 320 || !got[1].Private || got[1].Files[0].Name != "secret.flac" || !got[1].Files[0].Private {
		t.Fatalf("grouped list: %+v, %v", got, err)
	}
}

func TestDecodeSharedListDirectoriesBoundaries(t *testing.T) {
	raw := groupedRaw(t, false)
	payload := groupedPayload(t, raw)
	limits := BrowseLimits{MaxEntries: 2, MaxCompressedSize: len(payload), MaxDecompressedSize: len(raw)}
	if _, err := decodeSharedListDirectories(payload, limits); err != nil {
		t.Fatalf("exact limits: %v", err)
	}
	if _, err := decodeSharedListDirectories(payload, BrowseLimits{MaxCompressedSize: len(payload) - 1}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("compressed limit: %v", err)
	}
	if _, err := decodeSharedListDirectories(payload, BrowseLimits{MaxDecompressedSize: len(raw) - 1}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("decompressed limit: %v", err)
	}
	trailing := append(append([]byte(nil), raw...), 0, 0, 0, 0, 0)
	if _, err := decodeSharedListDirectories(groupedPayload(t, trailing), BrowseLimits{}); !errors.Is(err, ErrMalformed) {
		t.Fatalf("decompressed trailing bytes: %v", err)
	}
	badChecksum := append([]byte(nil), payload...)
	badChecksum[len(badChecksum)-1] ^= 1
	if _, err := decodeSharedListDirectories(badChecksum, BrowseLimits{}); !errors.Is(err, ErrMalformed) {
		t.Fatalf("checksum: %v", err)
	}
	if _, err := decodeSharedListDirectories(payload[:len(payload)-1], BrowseLimits{}); !errors.Is(err, ErrTruncated) {
		t.Fatalf("compressed truncation: %v", err)
	}
	compressedTrailing := append(append([]byte(nil), payload...), 0)
	if _, err := decodeSharedListDirectories(compressedTrailing, BrowseLimits{}); !errors.Is(err, ErrMalformed) {
		t.Fatalf("compressed trailing bytes: %v", err)
	}
}

func TestDecodeSharedListDirectoriesAllowsMissingOptionalFields(t *testing.T) {
	var raw Encoder
	raw.U32(1)
	_ = raw.String("Music")
	raw.U32(0)
	if _, err := decodeSharedListDirectories(groupedPayload(t, raw.Payload()), BrowseLimits{}); err != nil {
		t.Fatalf("missing optional fields: %v", err)
	}
}

func TestDecodeSharedListDirectoriesFieldLimits(t *testing.T) {
	var tooLong Encoder
	tooLong.U32(1)
	tooLong.U32(MaxStringSize + 1)
	if _, err := decodeSharedListDirectories(groupedPayload(t, tooLong.Payload()), BrowseLimits{}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("string limit: %v", err)
	}

	var tooManyAttributes Encoder
	tooManyAttributes.U32(1)
	_ = tooManyAttributes.String("Music")
	tooManyAttributes.U32(1)
	tooManyAttributes.U8(1)
	_ = tooManyAttributes.String("song.mp3")
	tooManyAttributes.U64(1)
	_ = tooManyAttributes.String("mp3")
	tooManyAttributes.U32(65)
	if _, err := decodeSharedListDirectories(groupedPayload(t, tooManyAttributes.Payload()), BrowseLimits{}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("attribute limit: %v", err)
	}
}

func TestBrowseSharedDirectoriesPipe(t *testing.T) {
	clientConn, peerConn := net.Pipe()
	defer clientConn.Close()
	defer peerConn.Close()
	client := NewClientOnConn(ClientConfig{}, clientConn)
	go func() {
		command, _, err := ReadFrame(peerConn)
		if err == nil && command == PeerGetSharedList {
			_ = writeMessage(peerConn, SharedListResponse{Entries: []ShareEntry{{Name: "Music\\song.mp3", Size: 7}}})
		}
	}()
	got, err := client.browseSharedDirectories(context.Background(), clientConn, nil, client.BrowseLimits())
	if err != nil || len(got) != 1 || len(got[0].Files) != 1 || got[0].Files[0].Name != "song.mp3" {
		t.Fatalf("pipe browse: %+v, %v", got, err)
	}
}

func BenchmarkDecodeSharedListDirectories(b *testing.B) {
	var raw Encoder
	raw.U32(1000)
	for i := 0; i < 1000; i++ {
		_ = raw.String("Music\\Album")
		raw.U32(100)
		for j := 0; j < 100; j++ {
			_ = (SearchResult{Path: "song.mp3", Size: uint64(j), Extension: "mp3"}).encode(&raw)
		}
	}
	raw.U32(0)
	payload, err := CompressZlib(raw.Payload())
	if err != nil {
		b.Fatal(err)
	}
	for name, decode := range map[string]func() error{
		"grouped": func() error { _, err := decodeSharedListDirectories(payload, BrowseLimits{}); return err },
		"flat":    func() error { _, err := DecodeSharedListResponse(payload); return err },
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if err := decode(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
