package soulseek

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"golang.org/x/text/cases"
)

// linearSearch is the original matcher, kept as the reference.
func linearSearch(s *ShareIndex, query string, limit int) []ShareFile {
	fold := cases.Fold()
	var need, bad []string
	for _, t := range strings.Fields(fold.String(query)) {
		if strings.HasPrefix(t, "-") && len(t) > 1 {
			bad = append(bad, t[1:])
		} else if t != "-" {
			need = append(need, t)
		}
	}
	var out []ShareFile
	for i, v := range s.searchPaths {
		ok := true
		for _, x := range need {
			ok = ok && strings.Contains(v, x)
		}
		for _, x := range bad {
			ok = ok && !strings.Contains(v, x)
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

func TestTokenIndexMatchesLinearSearchExactly(t *testing.T) {
	words := []string{"Boards", "of", "Canada", "ROYGBIV", "Straße", "STRASSE", "猫", "café", "café", "AC/DC", "01", "02-track", "live", "Beatles", "beat", "x"}
	rng := rand.New(rand.NewPCG(1, 2))
	var files []ShareFile
	for i := range 3000 {
		var parts []string
		for range 2 + rng.IntN(5) {
			parts = append(parts, words[rng.IntN(len(words))])
		}
		path, err := NormalizePath(fmt.Sprintf(`Music\%s\%s %d.flac`, strings.Join(parts[:1], " "), strings.Join(parts[1:], " - "), i))
		if err != nil {
			continue
		}
		files = append(files, ShareFile{Root: "Music", Path: path})
	}
	index, err := RestoreShareIndex([]ShareRoot{{Name: "Music", Path: t.TempDir()}}, files)
	must(t, err)
	queries := []string{"beat", "BEAT -live", "strasse", "straße", "猫 live", "café", "café", "ac/dc", "02-track", "-beat", "o", "of canada", "roygbiv -boards", "music", "flac", "absent", "x 01", "eat"}
	for range 200 {
		var terms []string
		for range 1 + rng.IntN(3) {
			term := strings.ToLower(words[rng.IntN(len(words))])
			if len(term) > 3 && rng.IntN(2) == 0 {
				term = term[1 : len(term)-1] // substrings must still match
			}
			if rng.IntN(5) == 0 {
				term = "-" + term
			}
			terms = append(terms, term)
		}
		queries = append(queries, strings.Join(terms, " "))
	}
	for _, query := range queries {
		for _, limit := range []int{1, 7, 300, 100000} {
			got, want := index.Search(query, limit), linearSearch(index, query, limit)
			if len(got) != len(want) {
				t.Fatalf("query %q limit %d: %d results, want %d", query, limit, len(got), len(want))
			}
			for i := range got {
				if got[i].Path != want[i].Path {
					t.Fatalf("query %q limit %d: result %d is %q, want %q", query, limit, i, got[i].Path, want[i].Path)
				}
			}
		}
	}
}

func BenchmarkIncomingSearch200k(b *testing.B) {
	files := make([]ShareFile, 0, 200000)
	for i := range 200000 {
		p, err := NormalizePath(fmt.Sprintf(`Music\Artist %d\Album %d (20%02d) [FLAC]\%02d - Some Track Title %d.flac`, i/120, i/12, i%25, i%12, i))
		if err != nil {
			b.Fatal(err)
		}
		files = append(files, ShareFile{Root: "Music", Path: p})
	}
	index, err := RestoreShareIndex([]ShareRoot{{Name: "Music", Path: b.TempDir()}}, files)
	if err != nil {
		b.Fatal(err)
	}
	index.Search("warm", 1) // build the index outside the timed loop
	b.ResetTimer()
	for b.Loop() {
		_ = index.Search("boards of canada roygbiv", 300)
	}
}
