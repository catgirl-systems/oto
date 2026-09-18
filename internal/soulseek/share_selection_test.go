package soulseek

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

func TestSelectUploadFilesCapturesExactIndexedFolder(t *testing.T) {
	s := &ShareIndex{files: []ShareFile{{Root: "Music", Directory: true}, {Root: "Music", Path: "album", Directory: true}, {Root: "Music", Path: "album-old", Directory: true}, {Root: "Music", Path: "album-old/not-selected"}, {Root: "Music", Path: "album/disc", Directory: true}, {Root: "Music", Path: "album/disc/猫.flac"}, {Root: "Music", Path: "album/song"}, {Root: "Other", Directory: true}, {Root: "Other", Path: "album/song"}}}
	sortShareFiles(s.files)
	ctx := context.Background()
	want := []string{"Music/album/disc/猫.flac", "Music/album/song"}
	out, err := s.SelectUploadFiles(ctx, nil, `Music\album`)
	failIf(t, err != nil || !reflect.DeepEqual(out, want), out, err)
	selected, err := s.SelectUploadFiles(ctx, []string{`Music\album\song`, "Music/album/song"}, "")
	if err != nil || !reflect.DeepEqual(selected, []string{"Music/album/song"}) {
		t.Fatal(selected, err)
	}
	for _, req := range []struct {
		files  []string
		folder string
	}{{nil, ""}, {[]string{"Music/album/song"}, "Music"}, {[]string{"Music/album/unindexed"}, ""}, {[]string{"music/album/song"}, ""}, {[]string{"Music/album"}, ""}, {nil, "Music/album/song"}, {nil, "../Music"}} {
		if _, err := s.SelectUploadFiles(ctx, req.files, req.folder); err == nil {
			t.Fatal("invalid selection", req)
		}
	}
	s.files = append(s.files, ShareFile{Root: "Music", Path: "album/new"})
	sortShareFiles(s.files)
	failIf(t, !reflect.DeepEqual(out, want), "captured selection changed")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.SelectUploadFiles(cancelled, nil, "Music"); err == nil {
		t.Fatal("cancel ignored")
	}
}
func TestSelectUploadFilesRejectsWholeOversizedFolder(t *testing.T) {
	s := &ShareIndex{files: []ShareFile{{Root: "Music", Directory: true}}}
	for i := range MaxUploadSelectionFiles + 1 {
		s.files = append(s.files, ShareFile{Root: "Music", Path: fmt.Sprintf("file-%05d", i)})
	}
	if out, err := s.SelectUploadFiles(context.Background(), nil, "Music"); err == nil || out != nil {
		t.Fatal("selection silently truncated", len(out), err)
	}
	s.files = s.files[:len(s.files)-1]
	if out, err := s.SelectUploadFiles(context.Background(), nil, "Music"); err != nil || len(out) != MaxUploadSelectionFiles {
		t.Fatal(len(out), err)
	}
}
