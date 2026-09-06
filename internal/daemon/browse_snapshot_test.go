package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestBrowseSnapshotPages(t *testing.T) {
	groups := []soulseek.ShareDirectory{{Name: "Music"}, {Name: `Music\Empty`}, {Name: `Music\Secret`, Private: true, Files: []soulseek.ShareEntry{{Name: "hidden.flac", Size: 7, Private: true, SampleRate: 96000}}}}
	for i := 0; i < 405; i++ {
		groups[0].Files = append(groups[0].Files, soulseek.ShareEntry{Name: fmt.Sprintf("song-%03d.flac", i), Size: 1})
	}
	x, err := newBrowseSnapshot(groups, browseLimits(testConfig(t)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	root, err := x.page(ctx, BrowsePageRequest{})
	if err != nil || len(root.Entries) != 1 || root.TotalEntries != 409 || root.Entries[0].FileCount != 406 {
		t.Fatalf("root: %+v %v", root, err)
	}
	seen := map[int]bool{}
	cursor := 0
	for {
		page, err := x.page(ctx, BrowsePageRequest{Folder: "Music", Cursor: cursor})
		if err != nil || page.Total != 407 || len(page.Entries) > BrowsePageSize || len(page.Ancestors) != 1 {
			t.Fatalf("page: %+v %v", page, err)
		}
		for _, entry := range page.Entries {
			if seen[entry.ID] {
				t.Fatal("duplicate page ID")
			}
			seen[entry.ID] = true
		}
		if page.NextCursor == 0 {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 407 {
		t.Fatalf("missing entries: %d", len(seen))
	}
	found, err := x.page(ctx, BrowsePageRequest{Query: "MUSIC/SECRET/HIDDEN"})
	if err != nil || len(found.Entries) != 1 || !found.Entries[0].Private || found.Entries[0].SampleRate != 96000 || len(found.Entries[0].Ancestors) != 2 {
		t.Fatalf("find: %+v %v", found, err)
	}
	folder, err := x.page(ctx, BrowsePageRequest{Folder: `Music\Secret`})
	if err != nil || folder.Entries[0].ID != found.Entries[0].ID {
		t.Fatal("IDs changed between find and folder page")
	}
	if _, err := x.page(ctx, BrowsePageRequest{Cursor: -1}); err == nil {
		t.Fatal("negative cursor")
	}
	if _, err := x.page(ctx, BrowsePageRequest{Cursor: 2}); err == nil {
		t.Fatal("out-of-range cursor")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := x.page(cancelled, BrowsePageRequest{Query: "song"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	large, err := newBrowseSnapshot([]soulseek.ShareDirectory{{Name: "Long", Files: []soulseek.ShareEntry{{Name: strings.Repeat("x", 300000)}, {Name: strings.Repeat("y", 300000)}}}}, browseLimits(testConfig(t)))
	if err != nil {
		t.Fatal(err)
	}
	page, err := large.page(ctx, BrowsePageRequest{Folder: "Long"})
	data, _ := json.Marshal(page)
	if err != nil || len(page.Entries) != 1 || page.NextCursor != 1 || len(data) > browsePageBytes {
		t.Fatalf("byte-bounded page: %d bytes, %v", len(data), err)
	}
	limits := browseLimits(testConfig(t))
	limits.MaxEntries = 1
	if _, err := newBrowseSnapshot(groups, limits); !errors.Is(err, soulseek.ErrTooLarge) {
		t.Fatalf("entry budget: %v", err)
	}
}

func TestBrowseSnapshotSaveQueueAndRevision(t *testing.T) {
	s := remoteShareService(t)
	s.client = &soulseek.Client{}
	calls := 0
	s.fullBrowseDirectories = func(context.Context, *soulseek.Client, string, func(uint64, uint64)) ([]soulseek.ShareDirectory, error) {
		calls++
		return []soulseek.ShareDirectory{{Name: "Music", Files: []soulseek.ShareEntry{{Name: "a.flac", Size: 1}}}, {Name: `Music\More`, Files: []soulseek.ShareEntry{{Name: "b.flac", Size: 2, Private: true, BitDepth: 24}, {Name: "c.flac", Size: 3}}}}, nil
	}
	ctx := context.Background()
	root, err := s.OpenBrowse(ctx, "Peer", "", "")
	if err != nil || root.Cached || root.TotalEntries != 5 {
		t.Fatalf("open: %+v %v", root, err)
	}
	page, err := s.BrowsePage(ctx, BrowsePageRequest{Username: "peer", Revision: root.Revision, Folder: `Music\More`})
	if err != nil || calls != 1 {
		t.Fatalf("page refetched peer: %d %v", calls, err)
	}
	music, more, file := root.Entries[0].ID, page.Ancestors[1].ID, page.Entries[0].ID
	queued, err := s.QueueBrowse(ctx, BrowseDownloadRequest{Username: "peer", Revision: root.Revision, Selection: map[int]bool{music: true, more: false, file: true}})
	if err != nil || queued.Queued != 2 || calls != 1 {
		t.Fatalf("recursive rules: %+v %v", queued, err)
	}
	queued, err = s.QueueBrowse(ctx, BrowseDownloadRequest{Username: "peer", Revision: root.Revision, Folder: "Music", Recursive: true})
	if err != nil || queued.Queued != 1 {
		t.Fatalf("queue unloaded descendants/dedup: %+v %v", queued, err)
	}
	if _, err := s.SaveBrowse("peer", root.Revision); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.client = nil
	s.mu.Unlock()
	cached, err := s.OpenBrowse(ctx, "PEER", `Music\More`, "")
	if err != nil || !cached.Cached || cached.SavedAt.IsZero() || len(cached.Entries) != 2 || !cached.Entries[0].Private || cached.Entries[0].BitDepth != 24 {
		t.Fatalf("saved snapshot: %+v %v", cached, err)
	}
	if _, err := s.BrowsePage(ctx, BrowsePageRequest{Username: "peer", Revision: root.Revision}); !errors.Is(err, ErrBrowseRevision) {
		t.Fatal(err)
	}
	if _, err := s.QueueBrowse(ctx, BrowseDownloadRequest{Username: "peer", Revision: root.Revision, Selection: map[int]bool{music: true}}); !errors.Is(err, ErrBrowseRevision) {
		t.Fatal(err)
	}
	if _, err := s.SaveBrowse("peer", root.Revision); !errors.Is(err, ErrBrowseRevision) {
		t.Fatal(err)
	}
}

func TestBrowseSnapshotRejectsOlderOpenCompletion(t *testing.T) {
	s := remoteShareService(t)
	s.client = &soulseek.Client{}
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	s.fullBrowseDirectories = func(context.Context, *soulseek.Client, string, func(uint64, uint64)) ([]soulseek.ShareDirectory, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return []soulseek.ShareDirectory{{Name: "Old"}}, nil
		}
		return []soulseek.ShareDirectory{{Name: "New"}}, nil
	}
	done := make(chan error, 1)
	go func() { _, err := s.OpenBrowse(context.Background(), "peer", "", ""); done <- err }()
	<-started
	newer, err := s.OpenBrowse(context.Background(), "peer", "", "")
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrBrowseRevision) {
		t.Fatalf("stale open: %v", err)
	}
	page, err := s.BrowsePage(context.Background(), BrowsePageRequest{Username: "peer", Revision: newer.Revision})
	if err != nil || page.Entries[0].Name != "New" {
		t.Fatalf("newer snapshot lost: %+v %v", page, err)
	}
}

func TestBrowseSnapshotRelativeNamesAndNesting(t *testing.T) {
	limits := browseLimits(testConfig(t))
	x, err := newBrowseSnapshot([]soulseek.ShareDirectory{{Name: "Music", Files: []soulseek.ShareEntry{{Name: "disc/song.flac", Size: 42}}}}, limits)
	if err != nil {
		t.Fatal(err)
	}
	page, err := x.page(context.Background(), BrowsePageRequest{Folder: `Music\disc`})
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Name != `Music\disc\song.flac` || x.dirs[x.byPath[`Music\disc`]].files[0].Name != "song.flac" {
		t.Fatalf("relative hierarchy: %+v %v", page, err)
	}
	var groups []soulseek.ShareDirectory
	for i := 1; i <= 257; i++ {
		groups = append(groups, soulseek.ShareDirectory{Name: strings.Repeat("a\\", i)})
	}
	if _, err := newBrowseSnapshot(groups, limits); !errors.Is(err, soulseek.ErrTooLarge) {
		t.Fatalf("existing parents bypassed nesting limit: %v", err)
	}
}
