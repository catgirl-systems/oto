package daemon

import (
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

const BrowsePageSize = 200

// BrowseEntry IDs and ancestor IDs are stable only within one snapshot revision.
type BrowseEntry struct {
	soulseek.ShareEntry
	ID         int   `json:"id"`
	Ancestors  []int `json:"ancestors,omitempty"`
	ChildCount int   `json:"child_count,omitempty"`
	FileCount  int   `json:"file_count,omitempty"`
}

type BrowsePageRequest struct {
	Username string `json:"username"`
	Revision uint64 `json:"revision"`
	Folder   string `json:"folder"`
	Query    string `json:"query"`
	Cursor   int    `json:"cursor"`
}

type BrowsePage struct {
	Entries      []BrowseEntry `json:"entries"`
	Ancestors    []BrowseEntry `json:"ancestors,omitempty"`
	Folder       string        `json:"folder"`
	Query        string        `json:"query"`
	Cursor       int           `json:"cursor"`
	NextCursor   int           `json:"next_cursor,omitempty"`
	Total        int           `json:"total"`
	TotalEntries int           `json:"total_entries"`
	Revision     uint64        `json:"revision"`
	Cached       bool          `json:"cached"`
	SavedAt      time.Time     `json:"saved_at,omitempty"`
}

// Selection rules apply to an entry and its descendants; the nearest rule wins.
// False rules allow deselecting a file or subfolder within a selected folder.
type BrowseDownloadRequest struct {
	Username    string       `json:"username"`
	Revision    uint64       `json:"revision"`
	Selection   map[int]bool `json:"selection,omitempty"`
	Folder      string       `json:"folder,omitempty"`
	Recursive   bool         `json:"recursive"`
	DownloadDir string       `json:"download_dir,omitempty"`
	Destination string       `json:"destination,omitempty"` // Single explicitly selected file only.
}

type BrowseDownloadResult struct {
	Queued int `json:"queued"`
}
