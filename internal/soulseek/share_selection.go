package soulseek

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"
)

const MaxUploadSelectionFiles = 10000

// SelectUploadFiles captures names from this immutable index, not a fresh
// filesystem walk. Limits reject the entire selection, never truncate it.
func (s *ShareIndex) SelectUploadFiles(ctx context.Context, files []string, folder string) ([]string, error) {
	if (len(files) == 0) == (folder == "") || len(files) > MaxUploadSelectionFiles {
		return nil, errors.New("select files or one shared folder (at most 10000 files)")
	}
	out := []string{}
	size := 0
	add := func(f ShareFile) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := f.Root + "/" + f.Path
		if !utf8.ValidString(name) {
			return errors.New("shared file name is not UTF-8; no files selected")
		}
		if len(name) > 16<<10 {
			return errors.New("shared path exceeds 16 KiB; no files selected")
		}
		size += len(name)
		if len(out) == MaxUploadSelectionFiles || size > 2<<20 {
			return errors.New("shared selection exceeds budget; no files selected")
		}
		out = append(out, name)
		return nil
	}
	lower := func(root, path string) int {
		return sort.Search(len(s.files), func(i int) bool { f := s.files[i]; return f.Root > root || f.Root == root && f.Path >= path })
	}
	find := func(name string) (int, string, string, error) {
		if !utf8.ValidString(name) {
			return 0, "", "", errors.New("selected path is not UTF-8")
		}
		if len(name) > 16<<10 {
			return 0, "", "", errors.New("selected path exceeds 16 KiB")
		}
		clean, err := NormalizePath(name)
		if err != nil {
			return 0, "", "", err
		}
		root, path, _ := strings.Cut(clean, "/")
		i := lower(root, path)
		if i == len(s.files) || s.files[i].Root != root || s.files[i].Path != path {
			return 0, "", "", errors.New("path is not in the current share index")
		}
		return i, root, path, nil
	}
	if folder != "" {
		i, root, path, err := find(folder)
		if err != nil {
			return nil, err
		}
		if !s.files[i].Directory {
			return nil, errors.New("selected folder is a file")
		}
		prefix := path
		if prefix != "" {
			prefix += "/"
		}
		i = lower(root, prefix)
		for ; i < len(s.files); i++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			f := s.files[i]
			if f.Root != root || !strings.HasPrefix(f.Path, prefix) {
				break
			}
			if !f.Directory {
				if err := add(f); err != nil {
					return nil, err
				}
			}
		}
	} else {
		seen := make(map[string]bool, len(files))
		for _, name := range files {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			i, _, _, err := find(name)
			if err != nil {
				return nil, err
			}
			f := s.files[i]
			if f.Directory {
				return nil, errors.New("file selection contains a directory; select it as a folder")
			}
			key := f.Root + "/" + f.Path
			if seen[key] {
				continue
			}
			seen[key] = true
			if err := add(f); err != nil {
				return nil, err
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("shared selection contains no files")
	}
	sort.Strings(out)
	return out, nil
}
