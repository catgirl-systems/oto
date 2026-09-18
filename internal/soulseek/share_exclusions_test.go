package soulseek

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestShareExclusionsDefaults(t *testing.T) {
	e, err := NewShareExclusions(nil)
	must(t, err)
	cases := []struct {
		path      string
		dir, want bool
	}{
		{"share/.hidden", false, true}, {"share/.hidden/file", false, true}, {"share/.hidden", true, true},
		{"share/@eaDir/file", false, true}, {"share/#recycle/file", true, true}, {"share/#snapshot/x", false, true},
		{"share/desktop.ini", false, true}, {"share/Thumbs.DB", false, true},
		{"share/System Volume Information/x", false, true}, {"share/$recycle.bin/x", true, true},
		{"share/lost+found/x", false, true}, {"share/a.part", false, true}, {"share/a.PART", true, false},
		{"share/a.partial", false, true}, {"share/a.crdownload", false, true}, {"share/a.tmp", false, true},
		{"share/a.temp", false, true}, {"share/a.bak", false, true}, {"share/a~", false, true},
	}
	for _, tc := range cases {
		if got := e.Excluded(tc.path, tc.dir); got != tc.want {
			t.Errorf("Excluded(%q, %v) = %v, want %v", tc.path, tc.dir, got, tc.want)
		}
	}
}

func TestShareExclusionsMatching(t *testing.T) {
	e, err := NewShareExclusions([]string{"foo?bar", "[abc]file", "cache/", "nested/name", "*.log"})
	must(t, err)
	for _, tc := range []struct {
		path      string
		dir, want bool
	}{
		{"Share/foo?bar", false, true}, {"share/FOO?BAR", false, true}, {"share/foobar", false, false},
		{"share/[abc]file", false, true}, {"share/cache/x/y", false, true}, {"share/cache", true, true},
		{"share", true, false}, {"share/nested/name", false, true}, {"share/a/nested/name", false, true},
		{"share/a\\b.LOG", false, true}, {"share/a.log", true, false}, {"other/cache", true, true},
	} {
		if got := e.Excluded(tc.path, tc.dir); got != tc.want {
			t.Errorf("Excluded(%q, %v) = %v, want %v", tc.path, tc.dir, got, tc.want)
		}
	}
	empty, err := NewShareExclusions([]string{})
	failIfFmt(t, err != nil || empty.Excluded("share/cache/x", true), "empty exclusions: %v", err)
}

func TestShareExclusionsRejectsInvalid(t *testing.T) {
	if _, err := NewShareExclusions([]string{"../secret"}); err == nil {
		t.Fatal("accepted traversal")
	}
}

func TestShareExclusionsVirtualBoundariesAndRoots(t *testing.T) {
	e, err := NewShareExclusions([]string{"Music/Temp/*", "Music/*.tmp", "cache/", "a*b/end"})
	must(t, err)
	for _, tc := range []struct {
		name                string
		directory, excluded bool
	}{
		{"Music/Temp", true, true}, {"Music/Temp/deep/song", false, true},
		{"Music/album/x.tmp", false, true}, {"Other/x.tmp", false, false},
		{"Music/cache", false, false}, {"cache/song", false, false}, {"cache", true, false},
		{"Music/xcache/song", false, false}, {"Music/a/dir/b/end", false, true},
	} {
		if e.Excluded(tc.name, tc.directory) != tc.excluded {
			t.Errorf("boundary/root: %+v", tc)
		}
	}
}

func TestShareExclusionsIndexAndExactPaths(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"@eaDir/song", "album/song.tmp", "album/song.flac", ".hidden/song"} {
		local := filepath.Join(root, name)
		must(t, os.MkdirAll(filepath.Dir(local), 0700))
		must(t, os.WriteFile(local, []byte("x"), 0600))
	}
	must(t, os.Symlink(filepath.Join(root, "album"), filepath.Join(root, "link")))
	for _, rules := range [][]string{nil, {}} {
		index, err := NewShareIndexWithExclusions(rules)
		must(t, err)
		must(t, index.AddRoot("Music", root))
		must(t, index.ScanContext(context.Background()))
		for _, virtual := range []string{"Music/@eaDir/song", "Music/album/song.tmp", "Music/.hidden/song", "Music/link/song.flac"} {
			_, err := index.Resolve(virtual)
			want := rules == nil || virtual == "Music/.hidden/song" || virtual == "Music/link/song.flac"
			failIfFmt(t, (err != nil) != want, "resolve %s: %v", virtual, err)
		}
		for _, file := range index.Files() {
			failIfFmt(t, index.Excluded(file.Root+"/"+file.Path, file.Directory), "indexed excluded file: %+v", file)
		}
	}
}

func TestQueuedUploadRevalidatesExclusions(t *testing.T) {
	address, _, received := uploadPeer(t, "normal", 0)
	c, events, local := uploadClient(t, address, []byte("data"))
	blocker := c.cfg.Uploads.Enqueue("blocker", TransferRequest{})
	_, _, err := c.registerUpload("peer", "Music/song", true) // queued behind the occupied slot
	must(t, err)
	index, err := NewShareIndexWithExclusions([]string{"song"})
	must(t, err)
	must(t, index.AddRoot("Music", filepath.Dir(local)))
	must(t, index.ScanContext(context.Background()))
	c.SetShareIndex(index)
	c.cfg.Uploads.Done(blocker)
	failed := uploadEvent(t, events, "failed")
	failIf(t, failed.Done != 0, "excluded upload sent data")
	if got := <-received; len(got) != 0 {
		t.Fatalf("sent excluded bytes: %q", got)
	}
	if _, err := c.QueueUpload("peer", "Music/song"); err == nil {
		t.Fatal("new excluded request accepted")
	}
}

func TestStreamingUploadSurvivesExclusionPublication(t *testing.T) {
	address, _, received := uploadPeer(t, "normal", 0)
	c, events, local := uploadClient(t, address, []byte("data"))
	index, err := NewShareIndexWithExclusions([]string{"song"})
	must(t, err)
	must(t, index.AddRoot("Music", filepath.Dir(local)))
	must(t, index.ScanContext(context.Background()))
	c.cfg.UploadStreamStart = func(TransferEvent) { c.SetShareIndex(index) }
	if _, err := c.QueueUpload("peer", "Music/song"); err != nil {
		t.Fatal(err)
	}
	if done := uploadEvent(t, events, "completed"); done.Done != 4 {
		t.Fatalf("stream interrupted: %+v", done)
	}
	if got := <-received; string(got) != "data" {
		t.Fatalf("stream changed: %q", got)
	}
}
