package daemon

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestDownloadAsValidationAndFilters(t *testing.T) {
	s := downloadService(t)
	for _, name := range []string{"", " ", ".", "..", "../file", `dir\file`, "/file", "bad\x00name", "bad\nname", "bad\x7fname", string([]byte{255})} {
		if _, err := DownloadAsDestination("peer", `Album\song.flac`, name); err == nil {
			t.Fatalf("accepted filename %q", name)
		}
	}
	dest, err := DownloadAsDestination("Alice Smith", `Music\Album\song.flac`, "日本語 remix.flac")
	if err != nil || dest != "Alice_Smith/Music/Album/日本語 remix.flac" {
		t.Fatalf("destination = %q, %v", dest, err)
	}
	for _, dest := range []string{"../escape", "/escape", `C:\escape`, "peer/../escape", "peer/", "peer/.", "peer/bad\nname", "bad\nparent/file"} {
		_, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "valid.flac", Size: 8}, {Filename: "other.flac", Size: 8, Destination: dest}}}})
		if err == nil || len(s.Downloads()) != 0 {
			t.Fatalf("invalid destination committed a batch: %q, %v", dest, err)
		}
	}
	s.cfg.Downloads.FiltersEnabled = true
	s.cfg.Downloads.FilterPatterns = []string{"*.exe"}
	dest, err = DownloadAsDestination("peer", `Album\app.exe`, "song.flac")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: `Album\app.exe`, Size: 8, Destination: dest}}}})
	if err != nil || len(rows) != 1 || rows[0].State != "filtered" || rows[0].Filename != `Album\app.exe` || rows[0].Destination != dest {
		t.Fatalf("rename bypassed remote-name filter: %+v, %v", rows, err)
	}
}

func TestDownloadAsRestartResumeAndCollision(t *testing.T) {
	for _, mode := range []string{"file", "folder"} {
		t.Run(mode, func(t *testing.T) {
			s := downloadService(t)
			dest, err := DownloadAsDestination("peer", `Album\original.flac`, "renamed.flac")
			if err != nil {
				t.Fatal(err)
			}
			var rows []Download
			if mode == "folder" {
				dest = "peer/renamed/original.flac"
				rows, err = s.QueueFolder(context.Background(), FolderDownloadRequest{Username: "peer", Folder: "Album", Destination: "peer/renamed", Files: []DownloadItem{{Filename: `Album\original.flac`, Size: 8}}})
			} else {
				rows, err = s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: `Album\original.flac`, Size: 8, Destination: dest}}}})
			}
			if err != nil {
				t.Fatal(err)
			}
			id := rows[0].ID
			part := putPartial(t, id, "data")
			s.finishDownload(id, "failed", 4, io.EOF)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = New(s.cfg, s.journalPath)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			got := s.Downloads()[0]
			if got.Filename != `Album\original.flac` || got.Destination != dest || got.Offset != 4 || got.State != "retrying" || incompletePath(id) != part {
				t.Fatalf("restart changed source/destination/partial: %+v", got)
			}
			target := filepath.Join(s.cfg.DownloadDir, filepath.FromSlash(dest))
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("existing"), 0600); err != nil {
				t.Fatal(err)
			}
			// Supply the remaining bytes locally, then exercise the real resume/finalize path.
			f, err := os.OpenFile(part, os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := f.WriteString("tail")
			closeErr := f.Close()
			if writeErr != nil || closeErr != nil {
				t.Fatalf("partial append: %v, %v", writeErr, closeErr)
			}
			s.ctx, s.cancel = context.WithCancel(context.Background())
			if err := s.TransferAction(id, "resume"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool { return s.Downloads()[0].State == "completed" })
			got = s.Downloads()[0]
			if got.Filename != `Album\original.flac` || got.Destination == dest || !strings.Contains(got.Destination, "renamed") {
				t.Fatalf("completion lost rename/collision handling: %+v", got)
			}
			if data, err := os.ReadFile(filepath.Join(s.cfg.DownloadDir, filepath.FromSlash(got.Destination))); err != nil || string(data) != "datatail" {
				t.Fatalf("completed bytes: %q, %v", data, err)
			}
			if data, err := os.ReadFile(target); err != nil || string(data) != "existing" {
				t.Fatalf("overwrote collision: %q, %v", data, err)
			}
			if _, err := os.Stat(part); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("partial not retired: %v", err)
			}
		})
	}
}

func TestBrowseDownloadAsSelectionAndRevision(t *testing.T) {
	s := remoteShareService(t)
	s.client = &soulseek.Client{}
	calls := 0
	s.fullBrowseDirectories = func(context.Context, *soulseek.Client, string, func(uint64, uint64)) ([]soulseek.ShareDirectory, error) {
		calls++
		return []soulseek.ShareDirectory{{Name: "Album", Files: []soulseek.ShareEntry{{Name: "a.flac", Size: 8, Private: true}, {Name: "b.flac", Size: 8}}}}, nil
	}
	ctx := context.Background()
	page, err := s.OpenBrowse(ctx, "peer", "Album", "")
	if err != nil {
		t.Fatal(err)
	}
	file, other, folder := page.Entries[0].ID, page.Entries[1].ID, page.Ancestors[0].ID
	for _, req := range []BrowseDownloadRequest{
		{Selection: map[int]bool{file: true}, Revision: page.Revision + 1},
		{Selection: map[int]bool{folder: true}, Revision: page.Revision},
		{Selection: map[int]bool{file: false}, Revision: page.Revision},
		{Selection: map[int]bool{file: true, other: true}, Revision: page.Revision},
		{Folder: "Album", Selection: map[int]bool{file: true}, Revision: page.Revision},
		{Selection: map[int]bool{file: true}, Recursive: true, Revision: page.Revision},
	} {
		req.Username, req.Destination = "peer", "peer/Album/new.flac"
		if _, err := s.QueueBrowse(ctx, req); err == nil || len(s.Downloads()) != 0 {
			t.Fatalf("invalid download-as selection queued: %+v, %v", req, err)
		}
	}
	req := BrowseDownloadRequest{Username: "peer", Revision: page.Revision, Selection: map[int]bool{file: true}, Destination: "peer/Album/new.flac"}
	result, err := s.QueueBrowse(ctx, req)
	if err != nil || result.Queued != 1 || calls != 1 {
		t.Fatalf("renamed browse queue: %+v, %v, fetches %d", result, err, calls)
	}
	got := s.Downloads()[0]
	if got.Filename != `Album\a.flac` || got.Destination != req.Destination || got.Size != 8 {
		t.Fatalf("changed remote file: %+v", got)
	}
	if result, err = s.QueueBrowse(ctx, req); err != nil || result.Queued != 0 {
		t.Fatalf("changed existing browse deduplication: %+v, %v", result, err)
	}
}

func TestFolderDownloadAsMapping(t *testing.T) {
	entries := []soulseek.ShareEntry{{Name: `Music\Album\cover.jpg`, Size: 3}, {Name: `Music\Album\Disc\song.flac`, Size: 8}}
	for _, recursive := range []bool{false, true} {
		s := downloadService(t)
		req := FolderDownloadRequest{Username: "peer", Folder: "music/album", Destination: "peer/Music/新しい名前", Recursive: recursive, Files: []DownloadItem{{Filename: entries[0].Name, Size: 3}, {Filename: entries[1].Name, Size: 8}}}
		items, err := folderDownloadItems(req, entries)
		want := 1
		if recursive {
			want = 2
		}
		if err != nil || len(items) != want || items[0].Destination != req.Destination+"/cover.jpg" {
			t.Fatalf("folder mapping: %+v %v", items, err)
		}
		if recursive && (items[1].Destination != req.Destination+"/Disc/song.flac" || items[1].Filename != "Music/Album/Disc/song.flac") {
			t.Fatal("nested path or remote identity changed")
		}
		queued, err := s.QueueFolder(context.Background(), req)
		if err != nil || len(queued) != want || queued[0].Destination != items[want-1].Destination { // QueueFolder sorts remote filenames.
			t.Fatalf("folder queue: %+v %v", queued, err)
		}
		if again, err := s.QueueFolder(context.Background(), req); err != nil || len(again) != 0 {
			t.Fatal("folder deduplication changed")
		}
	}
	// Case-folding can change UTF-8 byte lengths; preserve whole path segments.
	items := []DownloadItem{{Filename: "ſ/Album/Disc/song.flac"}}
	if err := setFolderDestinations(items, "S/album", "peer/Renamed"); err != nil || items[0].Destination != "peer/Renamed/Disc/song.flac" {
		t.Fatalf("Unicode mapping: %+v %v", items, err)
	}
	s := downloadService(t)
	for _, destination := range []string{"../escape", "peer/a/../escape", "/absolute", "peer/", "peer/.", "peer/bad\nname"} {
		req := FolderDownloadRequest{Username: "peer", Folder: "Music/Album", Destination: destination, Files: []DownloadItem{{Filename: entries[0].Name, Size: 3}}}
		if _, err := s.QueueFolder(context.Background(), req); err == nil || len(s.Downloads()) != 0 {
			t.Fatalf("invalid destination queued: %q", destination)
		}
	}
	items = []DownloadItem{{Filename: "Music/AlbumExtra/song.flac"}}
	if err := setFolderDestinations(items, "Music/Album", "peer/Renamed"); err == nil {
		t.Fatal("prefix sibling accepted")
	}
}

func TestPagedFolderDownloadAs(t *testing.T) {
	s := remoteShareService(t)
	s.client = &soulseek.Client{}
	s.fullBrowseDirectories = func(context.Context, *soulseek.Client, string, func(uint64, uint64)) ([]soulseek.ShareDirectory, error) {
		return []soulseek.ShareDirectory{
			{Name: "Music/Album", Files: []soulseek.ShareEntry{{Name: "cover.jpg", Size: 3}}},
			{Name: "Music/Album/Disc", Files: []soulseek.ShareEntry{{Name: "song.flac", Size: 8, Private: true}, {Name: "app.exe", Size: 2}}},
		}, nil
	}
	ctx := context.Background()
	page, err := s.OpenBrowse(ctx, "peer", "", "") // Descendant pages are never loaded by the frontend.
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Downloads.FiltersEnabled, s.cfg.Downloads.FilterPatterns = true, []string{"*.exe"}
	for _, recursive := range []bool{false, true} {
		req := BrowseDownloadRequest{Username: "peer", Revision: page.Revision + 1, Folder: "Music/Album", Recursive: recursive, Destination: "peer/Music/Renamed", DownloadDir: t.TempDir()}
		before := len(s.Downloads())
		if _, err := s.QueueBrowse(ctx, req); err == nil || len(s.Downloads()) != before {
			t.Fatal("stale rename queued")
		}
		req.Revision = page.Revision
		out, err := s.QueueBrowse(ctx, req)
		want := 1
		if recursive {
			want = 3
		}
		if err != nil || out.Queued != want {
			t.Fatalf("paged rename: %+v %v", out, err)
		}
		for _, row := range s.Downloads()[before:] {
			remote := strings.ReplaceAll(row.Filename, "\\", "/")
			if row.Destination != "peer/Music/Renamed/"+strings.TrimPrefix(remote, "Music/Album/") {
				t.Fatalf("lost subtree: %+v", row)
			}
			if strings.HasSuffix(remote, ".exe") && row.State != "filtered" {
				t.Fatal("rename bypassed filter")
			}
		}
		if out, err = s.QueueBrowse(ctx, req); err != nil || out.Queued != 0 {
			t.Fatal("paged deduplication changed")
		}
	}
}
