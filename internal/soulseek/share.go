package soulseek

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/cases"
)

var ErrOutsideShare = errors.New("soulseek: path outside shared root")

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
	roots map[string]string
	files []ShareFile
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
func (s *ShareIndex) Roots() []ShareRoot {
	out := make([]ShareRoot, 0, len(s.roots))
	for n, p := range s.roots {
		out = append(out, ShareRoot{n, p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func hidden(name string) bool { return strings.HasPrefix(name, ".") }

// Scan rebuilds the snapshot, never follows symlinks, and skips hidden entries.
func (s *ShareIndex) Scan() error {
	if s == nil {
		return errors.New("nil share index")
	}
	var out []ShareFile
	for _, r := range s.Roots() {
		err := filepath.WalkDir(r.Path, func(path string, d fs.DirEntry, err error) error {
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
			return nil
		})
		if err != nil {
			return err
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Root != out[j].Root {
			return out[i].Root < out[j].Root
		}
		return out[i].Path < out[j].Path
	})
	s.files = out
	return nil
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
func (s *ShareIndex) local(root, virtual string) (string, error) {
	parts, e := cleanVirtual(virtual)
	if e != nil {
		return "", e
	}
	base, ok := s.roots[root]
	if !ok {
		return "", os.ErrNotExist
	}
	p := base
	for _, x := range parts {
		p = filepath.Join(p, x)
		if st, err := os.Lstat(p); err == nil && st.Mode()&os.ModeSymlink != 0 {
			return "", ErrOutsideShare
		}
	}
	rel, e := filepath.Rel(base, p)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrOutsideShare
	}
	return p, nil
}

// Resolve maps a virtual root/path to a canonical local path and rejects escapes.
func (s *ShareIndex) Resolve(virtual string) (string, error) {
	parts, e := cleanVirtual(virtual)
	if e != nil || len(parts) == 0 {
		return "", ErrOutsideShare
	}
	return s.local(parts[0], strings.Join(parts[1:], "/"))
}

// Browse returns immediate children below a virtual path.
func (s *ShareIndex) Browse(virtual string) ([]ShareEntry, error) {
	parts, e := cleanVirtual(virtual)
	if e != nil {
		return nil, e
	}
	if len(parts) == 0 {
		var out []ShareEntry
		for _, r := range s.Roots() {
			out = append(out, ShareEntry{Name: r.Name, Directory: true})
		}
		return out, nil
	}
	root := parts[0]
	local, e := s.local(root, strings.Join(parts[1:], "/"))
	if e != nil {
		return nil, e
	}
	ents, e := os.ReadDir(local)
	if e != nil {
		return nil, e
	}
	out := make([]ShareEntry, 0, len(ents))
	for _, x := range ents {
		if hidden(x.Name()) || x.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, e := x.Info()
		if e != nil {
			continue
		}
		out = append(out, ShareEntry{Name: x.Name(), Size: uint64(maxInt64(info.Size())), Directory: x.IsDir()})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// BrowseIndexed returns immediate children from the last completed share scan.
func (s *ShareIndex) BrowseIndexed(virtual string) ([]ShareEntry, error) {
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
func maxInt64(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

// Search performs Unicode-aware case-insensitive token matching. A token prefixed by - excludes matches.
func (s *ShareIndex) Search(query string) []ShareFile {
	fold := cases.Fold()
	var need, bad []string
	for _, t := range strings.Fields(fold.String(query)) {
		if strings.HasPrefix(t, "-") && len(t) > 1 {
			bad = append(bad, t[1:])
		} else if t != "-" {
			need = append(need, t)
		}
	}
	out := make([]ShareFile, 0, minInt(len(s.files), 500))
	for _, f := range s.files {
		v := fold.String(f.Root + "/" + f.Path)
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
			out = append(out, f)
			if len(out) == 500 {
				break
			}
		}
	}
	return out
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
