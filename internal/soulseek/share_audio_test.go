package soulseek

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRealAudioMetadataCacheAndPublication(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("real audio fixtures require ffmpeg")
	}
	if _, err = exec.LookPath("ffprobe"); err != nil {
		t.Skip("real audio fixtures require ffprobe")
	}
	root := t.TempDir()
	for _, extension := range []string{"mp3", "flac", "wav", "m4a", "ogg", "opus"} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		out, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "1.1", filepath.Join(root, "song."+extension)).CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("%s fixture: %v %s", extension, err, out)
		}
	}
	scan := func(previous *ShareIndex) (*ShareIndex, AudioScan) {
		t.Helper()
		index := NewShareIndex()
		must(t, index.AddRoot("Music", root))
		must(t, index.ScanContext(context.Background()))
		status, err := index.ExtractAudio(context.Background(), previous, nil)
		must(t, err)
		return index, status
	}
	first, status := scan(nil)
	failIfFmt(t, status.Extracted != 6 || status.Failed != 0, "extraction: %+v", status)
	c := NewClient(ClientConfig{Share: first})
	defer c.Close()
	browse, err := first.Browse("Music")
	must(t, err)
	folder, err := first.Subtree("Music")
	must(t, err)
	for _, entries := range [][]ShareEntry{browse, folder, c.shareEntries()} {
		for _, entry := range entries {
			if entry.Directory {
				continue
			}
			failIfFmt(t, entry.Duration == 0 || entry.SampleRate == 0 || entry.VBRKnown, "published metadata: %+v", entry)
		}
	}
	for _, result := range searchToResults(first.Search("song", 100), nil) {
		failIfFmt(t, result.Duration == 0 || result.SampleRate == 0 || result.VBRKnown, "search metadata: %+v", result)
	}
	second, status := scan(first)
	failIfFmt(t, status.Cached != 6 || status.Extracted != 0, "cache: %+v", status)
	now := time.Now().Add(time.Second)
	if err = os.Chtimes(filepath.Join(root, "song.flac"), now, now); err != nil {
		t.Fatal(err)
	}
	_, status = scan(second)
	failIfFmt(t, status.Cached != 5 || status.Extracted != 1, "invalidation: %+v", status)
	if err = os.WriteFile(filepath.Join(root, "broken.mp3"), []byte("not audio"), 0600); err != nil {
		t.Fatal(err)
	}
	third, status := scan(second)
	failIfFmt(t, status.Failed != 1 || len(third.Search("broken", 10)) != 1, "corrupt audio disappeared: %+v", status)
}
