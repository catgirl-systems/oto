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
	s := downloadService(t)
	dest, err := DownloadAsDestination("peer", `Album\original.flac`, "renamed.flac")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: `Album\original.flac`, Size: 8, Destination: dest}}}})
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
		{Folder: "Album", Revision: page.Revision},
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
