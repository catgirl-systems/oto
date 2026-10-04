package soulseek

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		must(t, os.MkdirAll(filepath.Dir(p), 0o755))
		must(t, os.WriteFile(p, []byte(body), 0o600))
	}
}

func scanFresh(t *testing.T, root string, rules []string) *ShareIndex {
	t.Helper()
	index, err := NewShareIndexWithExclusions(rules)
	must(t, err)
	must(t, index.AddRoot("Music", root))
	must(t, index.ScanContext(context.Background()))
	return index
}

func sameFiles(t *testing.T, step string, got, want *ShareIndex) {
	t.Helper()
	g, w := got.Files(), want.Files()
	if len(g) != len(w) {
		t.Fatalf("%s: %d entries, want %d\ngot  %v\nwant %v", step, len(g), len(w), g, w)
	}
	for i := range g {
		if g[i].Root != w[i].Root || g[i].Path != w[i].Path || g[i].Size != w[i].Size || g[i].Directory != w[i].Directory {
			t.Fatalf("%s: entry %d is %+v, want %+v", step, i, g[i], w[i])
		}
	}
}

func TestRescanPathsMatchesFullScan(t *testing.T) {
	root := t.TempDir()
	rules := []string{"*.part"}
	tree := map[string]string{}
	for a := range 4 {
		for b := range 5 {
			tree[fmt.Sprintf("Artist %d/Album %d/%02d.flac", a, b, b)] = "x"
		}
	}
	writeTree(t, root, tree)
	resolved, err := filepath.EvalSymlinks(root)
	must(t, err)
	current := scanFresh(t, root, rules)

	steps := []struct {
		name    string
		mutate  func()
		changed []string
	}{
		{"new file deep", func() { writeTree(t, root, map[string]string{"Artist 1/Album 2/new.flac": "new"}) }, []string{"Artist 1/Album 2/new.flac"}},
		{"file grows", func() { writeTree(t, root, map[string]string{"Artist 0/Album 0/00.flac": "longer"}) }, []string{"Artist 0/Album 0/00.flac"}},
		{"album removed", func() { must(t, os.RemoveAll(filepath.Join(root, "Artist 2", "Album 3"))) }, []string{"Artist 2/Album 3"}},
		{"album renamed", func() {
			must(t, os.Rename(filepath.Join(root, "Artist 3", "Album 1"), filepath.Join(root, "Artist 3", "Album 9")))
		}, []string{"Artist 3/Album 1", "Artist 3/Album 9"}},
		{"new artist subtree", func() {
			writeTree(t, root, map[string]string{"Artist 7/One/a.flac": "a", "Artist 7/Two/b.flac": "b"})
		}, []string{"Artist 7"}},
		{"hidden and excluded", func() {
			writeTree(t, root, map[string]string{"Artist 0/.cache/x": "x", "Artist 0/Album 1/song.part": "p", ".hidden/y": "y"})
		}, []string{"Artist 0/.cache", "Artist 0/Album 1/song.part", ".hidden"}},
		{"root level file", func() { writeTree(t, root, map[string]string{"loose.flac": "l"}) }, []string{"loose.flac"}},
	}
	for _, step := range steps {
		step.mutate()
		changed := make([]string, len(step.changed))
		for i, rel := range step.changed {
			changed[i] = filepath.Join(resolved, filepath.FromSlash(rel))
		}
		next, err := NewShareIndexWithExclusions(rules)
		must(t, err)
		must(t, next.AddRoot("Music", root))
		ok, err := next.RescanPaths(context.Background(), current, changed)
		must(t, err)
		failIfFmt(t, !ok, "%s: incremental rescan declined", step.name)
		sameFiles(t, step.name, next, scanFresh(t, root, rules))
		current = next
	}
}

func TestRescanPathsDeclinesWhatItCannotDo(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a/b.flac": "x"})
	previous := scanFresh(t, root, nil)
	fresh := func(rules []string) *ShareIndex {
		index, err := NewShareIndexWithExclusions(rules)
		must(t, err)
		must(t, index.AddRoot("Music", root))
		return index
	}
	inside := filepath.Join(root, "a", "b.flac")
	for name, tc := range map[string]struct {
		index    *ShareIndex
		previous *ShareIndex
		changed  []string
	}{
		"no previous":       {fresh(nil), nil, []string{inside}},
		"no changes":        {fresh(nil), previous, nil},
		"outside the share": {fresh(nil), previous, []string{t.TempDir()}},
		"rules changed":     {fresh([]string{"*.flac"}), previous, []string{inside}},
	} {
		ok, err := tc.index.RescanPaths(context.Background(), tc.previous, tc.changed)
		failIfFmt(t, ok || err != nil, "%s: ok=%v err=%v", name, ok, err)
	}
	many := make([]string, maxRescanDirs+1)
	for i := range many {
		many[i] = filepath.Join(root, fmt.Sprintf("d%03d", i), "x")
	}
	ok, _ := fresh(nil).RescanPaths(context.Background(), previous, many)
	failIf(t, ok, "rescan of too many directories was not declined")
}
