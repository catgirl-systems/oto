package daemon

import (
	"errors"
	"sync"
)

// ErrInvalidAction rejects an unknown batch transfer action.
var ErrInvalidAction = errors.New("daemon: invalid action")

// DownloadActionRequest applies Action to every listed download.
type DownloadActionRequest struct {
	Action string   `json:"action" enum:"pause,cancel,retry,resume,clear"`
	IDs    []string `json:"ids"`
}
type DownloadActionError struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// DownloadActionResult counts changed downloads; IDs that no longer exist are
// skipped rather than failed.
type DownloadActionResult struct {
	Changed int                   `json:"changed"`
	Skipped int                   `json:"skipped"`
	Errors  []DownloadActionError `json:"errors"`
}

// journalIndex caches ID -> position for a journal slice. A cached position
// is trusted only after confirming the entry there still has the ID, and the
// map is rebuilt when appends, removals or reorders invalidate it, so lookups
// are never stale without every mutation having to maintain the index. A
// miss always rebuilds: unknown IDs are rare, and trusting a partial check
// could hide an entry that replaced another at the same position.
type journalIndex struct {
	mu        sync.Mutex
	positions map[string]int
}

// find returns id's position among n entries, or -1. The caller holds the
// lock that guards the journal (read or write); mu guards only the map.
func (x *journalIndex) find(id string, n int, idAt func(int) string) int {
	x.mu.Lock()
	defer x.mu.Unlock()
	if i, ok := x.positions[id]; ok && i < n && idAt(i) == id {
		return i
	}
	x.positions = make(map[string]int, n)
	for i := range n {
		x.positions[idAt(i)] = i
	}
	if i, ok := x.positions[id]; ok {
		return i
	}
	return -1
}

func (s *Service) downloadIndexLocked(id string) int {
	return s.downloadIndex.find(id, len(s.journal.Downloads), func(i int) string { return s.journal.Downloads[i].ID })
}

func (s *Service) uploadIndexLocked(id string) int {
	return s.uploadIndex.find(id, len(s.journal.Uploads), func(i int) string { return s.journal.Uploads[i].ID })
}
