package soulseek

import (
	"path/filepath"
	"strings"
)

// A physical path can appear under several virtual roots. Watch it if any root
// includes it; otherwise an overlapping explicit root could lose its updates.
func (s *ShareIndex) ExcludedLocalPath(local string, directory bool) bool {
	found := false
	for name, root := range s.roots {
		rel, err := filepath.Rel(root, local)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		found = true
		if rel == "." || !s.Excluded(name+"/"+filepath.ToSlash(rel), directory) {
			return false
		}
	}
	return found
}
