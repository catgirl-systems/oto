package soulseek

import (
	"slices"
	"sort"
	"strings"
	"unicode"
)

// Incoming searches arrive constantly from the distributed network, so a
// linear scan of every shared path per search costs real CPU on large shares.
//
// tokenIndex keeps the exact substring semantics. A query term made only of
// word characters can only occur inside one maximal run of word characters
// in a path, never across a separator. Indexing every distinct run (token)
// with the files that contain it therefore yields an exact candidate set:
// the files holding a token that contains the term. Tokens live in one
// contiguous string, so finding those containing a term is a strings.Index
// loop over a small buffer instead of every path. Candidates are then checked
// with the original matcher, in file order, so results are identical.
type tokenIndex struct {
	dict     string     // "\x00tok\x00tok\x00…", each token preceded by \x00
	starts   []int      // offset of each token's first byte in dict
	postings [][]uint32 // per token, ascending indexes into files
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) }

// indexable reports whether every occurrence of term lies inside one token.
func indexable(term string) bool {
	if term == "" {
		return false
	}
	for _, r := range term {
		if !wordRune(r) {
			return false
		}
	}
	return true
}

func buildTokenIndex(paths []string) *tokenIndex {
	ids := make(map[string]int)
	var tokens []string
	var postings [][]uint32
	for i, path := range paths {
		for _, token := range strings.FieldsFunc(path, func(r rune) bool { return !wordRune(r) }) {
			id, ok := ids[token]
			if !ok {
				id = len(tokens)
				ids[token] = id
				tokens = append(tokens, token)
				postings = append(postings, nil)
			}
			if list := postings[id]; len(list) == 0 || list[len(list)-1] != uint32(i) {
				postings[id] = append(list, uint32(i))
			}
		}
	}
	var dict strings.Builder
	starts := make([]int, len(tokens))
	for id, token := range tokens {
		dict.WriteByte(0)
		starts[id] = dict.Len()
		dict.WriteString(token)
	}
	return &tokenIndex{dict: dict.String(), starts: starts, postings: postings}
}

// candidates returns, ascending, the files containing a token that contains
// term. term must be indexable.
func (x *tokenIndex) candidates(term string) []uint32 {
	var out []uint32
	lists := 0
	for from := 0; from < len(x.dict); {
		at := strings.Index(x.dict[from:], term)
		if at < 0 {
			break
		}
		at += from
		// The token holding this occurrence starts at the last start <= at.
		id := sort.SearchInts(x.starts, at+1) - 1
		out = append(out, x.postings[id]...)
		lists++
		// Skip the rest of this token: one match per token is enough.
		if id+1 < len(x.starts) {
			from = x.starts[id+1]
		} else {
			break
		}
	}
	if lists > 1 {
		slices.Sort(out)
		out = slices.Compact(out)
	}
	return out
}

// tokens returns the search index for the current files, building it on
// first use. setFiles resets it; searches never run concurrently with a scan
// of the same index.
func (s *ShareIndex) tokens() *tokenIndex {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	if s.tokenIndex == nil {
		s.tokenIndex = buildTokenIndex(s.searchPaths)
	}
	return s.tokenIndex
}
