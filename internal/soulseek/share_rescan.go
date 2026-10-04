package soulseek

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// maxRescanDirs bounds incremental rescans: past this many changed
// directories a full walk costs about the same and is simpler.
const maxRescanDirs = 256

func carriedKey(root, p string) string { return root + "\x00" + p }

// RescanPaths fills s, a fresh index with previous's roots and exclusions,
// from previous plus a re-walk of the directories holding changed (absolute
// local paths reported by a file watcher). Entries outside those directories
// are copied, so one new file in a large library costs one directory walk
// instead of walking every root. It reports false, leaving s untouched, when
// it cannot apply; the caller then runs a full ScanContext.
func (s *ShareIndex) RescanPaths(ctx context.Context, previous *ShareIndex, changed []string) (bool, error) {
	if s == nil || previous == nil || len(changed) == 0 ||
		!slices.Equal(s.Roots(), previous.Roots()) || !slices.Equal(s.Exclusions(), previous.Exclusions()) {
		return false, nil
	}
	roots := s.Roots()
	// Each change dirties its parent directory (its listing changed); a
	// directory change also covers the subtree below it, which the parent's
	// walk includes. "" stands for the whole root.
	dirty := make(map[string]map[string]bool)
	for _, name := range changed {
		abs := filepath.Clean(name)
		var root *ShareRoot
		for i := range roots {
			if abs == roots[i].Path || strings.HasPrefix(abs, roots[i].Path+string(filepath.Separator)) {
				root = &roots[i]
				break
			}
		}
		if root == nil {
			return false, nil
		}
		rel, err := filepath.Rel(root.Path, abs)
		if err != nil {
			return false, nil
		}
		dir := ""
		if rel != "." {
			if dir = filepath.ToSlash(filepath.Dir(rel)); dir == "." {
				dir = ""
			}
		}
		if dirty[root.Name] == nil {
			dirty[root.Name] = make(map[string]bool)
		}
		dirty[root.Name][dir] = true
	}
	under := func(dirs map[string]bool, p string) bool {
		for {
			if dirs[p] {
				return true
			}
			if p == "" {
				return false
			}
			if p = path.Dir(p); p == "." {
				p = ""
			}
		}
	}
	total := 0
	for name, dirs := range dirty {
		for dir := range dirs {
			if dir != "" && under(dirs, parentDir(dir)) {
				delete(dirs, dir) // an ancestor's walk already covers it
			}
		}
		total += len(dirty[name])
	}
	if total > maxRescanDirs {
		return false, nil
	}

	progress := shareScanProgress(ctx)
	out := make([]ShareFile, 0, len(previous.files))
	carried := make(map[string]bool, len(previous.files))
	for _, f := range previous.files {
		if dirs := dirty[f.Root]; dirs != nil && under(dirs, f.Path) {
			continue
		}
		out = append(out, f)
		carried[carriedKey(f.Root, f.Path)] = true
		if progress != nil {
			progress(f.Root, f.Directory)
		}
	}
	for _, r := range roots {
		for dir := range dirty[r.Name] {
			if !s.visibleDir(r, dir) {
				continue
			}
			start := filepath.Join(r.Path, filepath.FromSlash(dir))
			info, err := os.Lstat(start)
			if errors.Is(err, os.ErrNotExist) || err == nil && (!info.IsDir() || dir != "" && info.Mode()&os.ModeSymlink != 0) {
				continue // removed, or no longer a walkable directory
			}
			if err != nil {
				return false, err
			}
			if err := s.walkShare(ctx, r, start, &out, progress); err != nil {
				return false, err
			}
		}
	}
	if err := s.setFiles(ctx, out); err != nil {
		return false, err
	}
	s.carried = carried
	return true, nil
}

// SameContent reports whether other publishes exactly the same roots,
// exclusions and entries, audio metadata included.
func (s *ShareIndex) SameContent(other *ShareIndex) bool {
	if s == nil || other == nil {
		return s == other
	}
	return slices.Equal(s.Roots(), other.Roots()) && slices.Equal(s.Exclusions(), other.Exclusions()) && slices.Equal(s.files, other.files)
}

func parentDir(dir string) string {
	if parent := path.Dir(dir); parent != "." {
		return parent
	}
	return ""
}

// visibleDir reports whether a full walk would descend into dir: no hidden or
// excluded ancestor, the directory itself included.
func (s *ShareIndex) visibleDir(r ShareRoot, dir string) bool {
	if dir == "" {
		return true
	}
	parts := strings.Split(dir, "/")
	for i := range parts {
		if hidden(parts[i]) || s.Excluded(r.Name+"/"+strings.Join(parts[:i+1], "/"), true) {
			return false
		}
	}
	return true
}
