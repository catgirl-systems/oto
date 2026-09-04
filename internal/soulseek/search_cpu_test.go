package soulseek

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestSearchPathsFollowSnapshots(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"Straße.flac", "STRASSE live.flac", "猫.flac"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	index := NewShareIndex()
	if err := index.AddRoot("MÜSIC", root); err != nil {
		t.Fatal(err)
	}
	if err := index.ScanContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	files := index.Files()
	slices.Reverse(files) // Restoring must sort filenames and search paths together.
	restored, err := RestoreShareIndex(index.Roots(), files)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for _, snapshot := range []*ShareIndex{index, restored} {
		for range 4 {
			workers.Go(func() {
				for _, tc := range []struct {
					query string
					limit int
					path  string
				}{
					{"müsic STRASSE -live", 300, "Straße.flac"},
					{"strasse", 1, "STRASSE live.flac"},
					{"猫", 300, "猫.flac"},
					{"absent", 300, ""},
					{"strasse", 0, ""},
				} {
					got := snapshot.Search(tc.query, tc.limit)
					if tc.path == "" && len(got) == 0 {
						continue
					}
					if len(got) != 1 || got[0].Path != tc.path {
						t.Errorf("search %q: %+v", tc.query, got)
					}
				}
			})
		}
	}
	workers.Wait()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := index.ScanContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan: %v", err)
	}
	if got := index.Search("strasse -live", 300); len(got) != 1 {
		t.Fatal("cancelled scan replaced search paths")
	}
	if err := os.Rename(filepath.Join(root, "Straße.flac"), filepath.Join(root, "Neu.flac")); err != nil {
		t.Fatal(err)
	}
	if err := index.ScanContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(index.Search("strasse -live", 300)) != 0 || len(index.Search("neu", 300)) != 1 || len(restored.Search("strasse -live", 300)) != 1 {
		t.Fatal("rescan did not replace only its own search paths")
	}
}

func TestSearchAllocationsDoNotScaleWithShareCount(t *testing.T) {
	files := make([]ShareFile, 1000)
	for i := range files {
		files[i] = ShareFile{Root: "Music", Path: fmt.Sprintf("Beyoncé/Track %04d.flac", i)}
	}
	index, err := RestoreShareIndex([]ShareRoot{{Name: "Music", Path: t.TempDir()}}, files)
	if err != nil {
		t.Fatal(err)
	}
	if allocs := testing.AllocsPerRun(10, func() { index.Search("absent", 300) }); allocs > 20 {
		t.Fatalf("full scan allocated %.0f objects; filenames should already be folded", allocs)
	}
}

func TestIncomingSearchAdmissionAndRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		index := NewShareIndex()
		if err := index.setFiles(t.Context(), []ShareFile{{Root: "Music", Path: "song.flac"}}); err != nil {
			t.Fatal(err)
		}
		client := NewClient(ClientConfig{Share: index})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		client.ctx = ctx
		// Keep matching responses waiting for a peer address, without network I/O.
		client.addresses["peer"] = &peerAddressLookup{done: make(chan struct{})}
		messages, err := client.distributed.AddChild("child")
		if err != nil {
			t.Fatal(err)
		}
		payload, err := (DistributedSearchQuery{Username: "peer", Token: 7, Query: "song"}).MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		for round := range 2 {
			for range cap(client.searchSlots) * 2 {
				client.route(ServerFileSearch, IncomingSearch{Username: "peer", Query: "song"})
				client.handleDistributedSearch(payload)
				select {
				case message := <-messages:
					if message.Command != DistributedSearchCommand || !bytes.Equal(message.Payload, payload) {
						t.Fatal("distributed forwarding changed")
					}
				default:
					t.Fatal("busy client stopped forwarding searches")
				}
			}
			synctest.Wait()
			if got := len(client.searchSlots); got != 2 {
				t.Fatalf("active responses = %d, want 2", got)
			}
			if round == 0 {
				time.Sleep(10 * time.Second)
			} else {
				cancel()
			}
			synctest.Wait()
			if len(client.searchSlots) != 0 {
				t.Fatal("timeout/cancellation did not release search slots")
			}
		}
		for _, query := range []string{"absent", "song"} {
			client.respondSearch(IncomingSearch{Query: query}) // No match, then invalid peer.
			synctest.Wait()
			if len(client.searchSlots) != 0 {
				t.Fatal("early return did not release search slot")
			}
		}
	})
}

func BenchmarkShareIndexSearch(b *testing.B) {
	files := make([]ShareFile, 10000)
	for i := range files {
		files[i] = ShareFile{Root: "Music", Path: fmt.Sprintf("Beyoncé/Album %04d/Track %02d.flac", i/10, i%10)}
	}
	index, err := RestoreShareIndex([]ShareRoot{{Name: "Music", Path: b.TempDir()}}, files)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if got := index.Search("absent", 300); len(got) != 0 {
			b.Fatal(got)
		}
	}
}
