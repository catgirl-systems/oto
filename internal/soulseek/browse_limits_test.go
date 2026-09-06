package soulseek

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestBrowseBeyondOldEntryLimit(t *testing.T) {
	client := NewClient(ClientConfig{})
	if got := client.BrowseLimits(); got != (BrowseLimits{2_000_000, 64 << 20, 256 << 20}) {
		t.Fatalf("default browse limits: %+v", got)
	}
	var raw, file Encoder
	raw.U32(1)
	_ = raw.String("Music")
	raw.U32(500_000)
	if err := (SearchResult{Path: "song.flac", Size: 42}).encode(&file); err != nil {
		t.Fatal(err)
	}
	raw.Raw(bytes.Repeat(file.Payload(), 500_000))
	raw.U32(0)
	payload, err := CompressZlib(raw.Payload())
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeSharedListResponse(payload)
	if err != nil || len(got.Entries) != 500_001 {
		t.Fatalf("large share: entries=%d err=%v", len(got.Entries), err)
	}
}

func TestBrowseConfiguredLimits(t *testing.T) {
	for _, folder := range []bool{false, true} {
		for _, limit := range []string{"entries", "compressed", "decompressed", "exact"} {
			t.Run(fmt.Sprintf("folder=%t/%s", folder, limit), func(t *testing.T) {
				left, right := net.Pipe()
				defer left.Close()
				defer right.Close()
				client := NewClient(ClientConfig{})
				path := ""
				if folder {
					path = "Music"
				}
				go func() {
					defer right.Close()
					_, request, err := ReadFrame(right)
					if err != nil {
						return
					}
					entries := []ShareEntry{{Name: `Music\song.flac`, Size: 42}}
					var message Message = SharedListResponse{Entries: entries}
					if folder {
						token, _ := NewDecoder(request).U32()
						message = FolderResponse{Token: token, Path: path, Entries: entries}
					}
					_ = writeMessage(right, message)
				}()
				// Derive exact byte bounds from the same valid response (token size is fixed).
				entries := []ShareEntry{{Name: `Music\song.flac`, Size: 42}}
				var message Message = SharedListResponse{Entries: entries}
				if folder {
					message = FolderResponse{Token: 1, Path: path, Entries: entries}
				}
				var encoded Encoder
				if err := message.encode(&encoded); err != nil {
					t.Fatal(err)
				}
				raw, err := DecompressZlib(encoded.Payload())
				if err != nil {
					t.Fatal(err)
				}
				limits := BrowseLimits{2, len(encoded.Payload()) + 4, len(raw)}
				want := ""
				switch limit {
				case "entries":
					limits.MaxEntries--
					want = "files/directories"
				case "compressed":
					limits.MaxCompressedSize--
					want = "frame has"
				case "decompressed":
					limits.MaxDecompressedSize--
					want = "decompressed payload exceeds"
				}
				client.ConfigureBrowseLimits(limits)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				got, err := client.Browse(ctx, left, path)
				if want == "" {
					if err != nil || len(got) != 2 {
						t.Fatalf("exact limits: entries=%d err=%v", len(got), err)
					}
				} else if !errors.Is(err, ErrTooLarge) || !strings.Contains(err.Error(), want) {
					t.Fatalf("expected %q size limit: %v", want, err)
				}
				if got := NewClient(ClientConfig{}).BrowseLimits(); got.MaxEntries != 2_000_000 {
					t.Fatalf("limits leaked to another client: %+v", got)
				}
			})
		}
	}
}

func TestBrowseLimitIncludesPrivateEntries(t *testing.T) {
	var encoded Encoder
	err := (SharedListResponse{Entries: []ShareEntry{
		{Name: `Music\public.flac`}, {Name: `Private\private.flac`, Private: true},
	}}).encode(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	limits := BrowseLimits{MaxEntries: 3}.withDefaults()
	if _, err := decodeSharedListResponse(encoded.Payload(), limits); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("public + private entries must share one budget: %v", err)
	}
	limits.MaxEntries = 4
	got, err := decodeSharedListResponse(encoded.Payload(), limits)
	if err != nil || len(got.Entries) != 4 || !got.Entries[3].Private {
		t.Fatalf("exact public + private budget: entries=%d err=%v", len(got.Entries), err)
	}
}
