package soulseek

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/cases"
)

var ErrOutsideShare = errors.New("soulseek: path outside shared root")

type shareScanProgressKey struct{}

// WithShareScanProgress attaches a callback invoked for each accepted index entry.
func WithShareScanProgress(ctx context.Context, progress func(root string, directory bool)) context.Context {
	return context.WithValue(ctx, shareScanProgressKey{}, progress)
}

func shareScanProgress(ctx context.Context) func(string, bool) {
	progress, _ := ctx.Value(shareScanProgressKey{}).(func(string, bool))
	return progress
}

// ShareRoot names one local directory in the virtual share namespace.
type ShareRoot struct{ Name, Path string }

// ShareFile is a file or directory visible in the public share index.
type ShareFile struct {
	Root, Path string
	Size       uint64
	Directory  bool
}

// ShareIndex is a deterministic, read-only snapshot of shared files.
type ShareIndex struct {
	roots       map[string]string
	files       []ShareFile
	searchPaths []string // Case-folded once, in the same order as files.
}

func NewShareIndex() *ShareIndex { return &ShareIndex{roots: make(map[string]string)} }
func (s *ShareIndex) AddRoot(name, path string) error {
	if s == nil {
		return errors.New("nil share index")
	}
	name = strings.Trim(name, "/\\")
	if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return errors.New("invalid share name")
	}
	p, e := filepath.Abs(path)
	if e != nil {
		return e
	}
	p, e = filepath.EvalSymlinks(p)
	if e != nil {
		return e
	}
	st, e := os.Stat(p)
	if e != nil {
		return e
	}
	if !st.IsDir() {
		return errors.New("share root is not a directory")
	}
	s.roots[name] = filepath.Clean(p)
	return nil
}

// RestoreShareIndex reconstructs a scanned index after validating cached entries.
func RestoreShareIndex(roots []ShareRoot, files []ShareFile) (*ShareIndex, error) {
	index := NewShareIndex()
	for _, root := range roots {
		if err := index.AddRoot(root.Name, root.Path); err != nil {
			return nil, err
		}
	}
	for _, file := range files {
		if _, ok := index.roots[file.Root]; !ok {
			return nil, errors.New("share index contains an unknown root")
		}
		if file.Path == "" {
			if !file.Directory {
				return nil, errors.New("share index contains an invalid path")
			}
			continue
		}
		normalized, err := NormalizePath(file.Path)
		if err != nil || normalized != file.Path {
			return nil, errors.New("share index contains an invalid path")
		}
	}
	err := index.setFiles(context.Background(), append([]ShareFile(nil), files...))
	return index, err
}
func (s *ShareIndex) Roots() []ShareRoot {
	out := make([]ShareRoot, 0, len(s.roots))
	for n, p := range s.roots {
		out = append(out, ShareRoot{n, p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func hidden(name string) bool { return strings.HasPrefix(name, ".") }

func sortShareFiles(files []ShareFile) {
	sort.Slice(files, func(i, j int) bool {
		if files[i].Root != files[j].Root {
			return files[i].Root < files[j].Root
		}
		return files[i].Path < files[j].Path
	})
}

func (s *ShareIndex) setFiles(ctx context.Context, files []ShareFile) error {
	sortShareFiles(files)
	fold := cases.Fold()
	paths := make([]string, len(files))
	for i, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		paths[i] = fold.String(file.Root + "/" + file.Path)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.files, s.searchPaths = files, paths
	return nil
}

// ScanContext builds a complete replacement snapshot and stops when ctx is cancelled.
func (s *ShareIndex) ScanContext(ctx context.Context) error {
	if s == nil {
		return errors.New("nil share index")
	}
	progress := shareScanProgress(ctx)
	var out []ShareFile
	for _, r := range s.Roots() {
		err := filepath.WalkDir(r.Path, func(path string, d fs.DirEntry, err error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err != nil {
				return err
			}
			if path != r.Path && hidden(d.Name()) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if path != r.Path && d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			rel, e := filepath.Rel(r.Path, path)
			if e != nil {
				return e
			}
			if rel == "." {
				rel = ""
			}
			size := uint64(0)
			if !d.IsDir() {
				info, e := d.Info()
				if e != nil {
					return e
				}
				size = uint64(info.Size())
			}
			out = append(out, ShareFile{Root: r.Name, Path: filepath.ToSlash(rel), Size: size, Directory: d.IsDir()})
			if progress != nil {
				progress(r.Name, d.IsDir())
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return s.setFiles(ctx, out)
}
func (s *ShareIndex) Files() []ShareFile { return append([]ShareFile(nil), s.files...) }
func cleanVirtual(p string) ([]string, error) {
	p = strings.ReplaceAll(p, "\\", "/")
	if p == "" || p == "." {
		return nil, nil
	}
	if strings.HasPrefix(p, "/") {
		return nil, ErrOutsideShare
	}
	parts := strings.Split(p, "/")
	out := parts[:0]
	for _, x := range parts {
		if x == "" || x == "." {
			continue
		}
		if x == ".." {
			return nil, ErrOutsideShare
		}
		out = append(out, x)
	}
	return out, nil
}

// Resolve maps a virtual root/path to a canonical local path and rejects escapes.
func (s *ShareIndex) Resolve(virtual string) (string, error) {
	parts, err := cleanVirtual(virtual)
	if err != nil || len(parts) == 0 {
		return "", ErrOutsideShare
	}
	root, ok := s.roots[parts[0]]
	if !ok {
		return "", os.ErrNotExist
	}
	if len(parts) == 1 {
		return root, nil
	}
	return SafeJoin(root, strings.Join(parts[1:], "/"))
}

// Browse returns immediate children from the last completed share scan.
func (s *ShareIndex) Browse(virtual string) ([]ShareEntry, error) {
	parts, err := cleanVirtual(virtual)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		out := make([]ShareEntry, 0, len(s.roots))
		for _, root := range s.Roots() {
			out = append(out, ShareEntry{Name: root.Name, Directory: true})
		}
		return out, nil
	}
	root, relative := parts[0], strings.Join(parts[1:], "/")
	if _, ok := s.roots[root]; !ok {
		return nil, os.ErrNotExist
	}
	if relative != "" {
		found := false
		for _, file := range s.files {
			if file.Root == root && file.Path == relative && file.Directory {
				found = true
				break
			}
		}
		if !found {
			return nil, os.ErrNotExist
		}
	}
	var out []ShareEntry
	for _, file := range s.files {
		if file.Root != root || file.Path == "" {
			continue
		}
		parent, name := "", file.Path
		if split := strings.LastIndex(file.Path, "/"); split >= 0 {
			parent, name = file.Path[:split], file.Path[split+1:]
		}
		if parent == relative {
			out = append(out, ShareEntry{Name: name, Size: file.Size, Directory: file.Directory})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Directory != out[j].Directory {
			return out[i].Directory
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// Subtree returns the requested directory and every indexed descendant using full virtual paths.
func (s *ShareIndex) Subtree(virtual string) ([]ShareEntry, error) {
	parts, err := cleanVirtual(virtual)
	if err != nil || len(parts) == 0 {
		return nil, ErrOutsideShare
	}
	if _, err = s.Browse(virtual); err != nil {
		return nil, err
	}
	root, relative := parts[0], strings.Join(parts[1:], "/")
	prefix := relative
	if prefix != "" {
		prefix += "/"
	}
	requested := strings.Join(parts, "\\")
	out := []ShareEntry{{Name: requested, Directory: true}}
	for _, file := range s.files {
		if file.Root != root || file.Path == "" || file.Path == relative || (prefix != "" && !strings.HasPrefix(file.Path, prefix)) {
			continue
		}
		out = append(out, ShareEntry{
			Name:      root + "\\" + strings.ReplaceAll(file.Path, "/", "\\"),
			Size:      file.Size,
			Directory: file.Directory,
		})
	}
	return out, nil
}

// Search performs Unicode-aware case-insensitive token matching. A token prefixed by - excludes matches.
func (s *ShareIndex) Search(query string, limit int) []ShareFile {
	fold := cases.Fold()
	var need, bad []string
	for _, t := range strings.Fields(fold.String(query)) {
		if strings.HasPrefix(t, "-") && len(t) > 1 {
			bad = append(bad, t[1:])
		} else if t != "-" {
			need = append(need, t)
		}
	}
	if limit <= 0 {
		return nil
	}
	out := make([]ShareFile, 0, min(len(s.files), limit))
	// ponytail: still a linear scan; add a search index only if cached matching is too slow.
	for i, v := range s.searchPaths {
		ok := true
		for _, x := range need {
			if !strings.Contains(v, x) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		for _, x := range bad {
			if strings.Contains(v, x) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, s.files[i])
			if len(out) == limit {
				break
			}
		}
	}
	return out
}
