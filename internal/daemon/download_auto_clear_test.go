package daemon

import (
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/stats"
)

// Auto-clear filtered downloads drops history on enqueue and on resume while
// still recording the filter event, matching finished-download cleanup.
func TestAutoClearFilteredDownloadsDropsHistory(t *testing.T) {
	cfg := testConfig(t)
	cfg.Downloads.FiltersEnabled = true
	cfg.Downloads.FilterPatterns = []string{"*.exe"}
	cfg.Downloads.AutoClearFiltered = true
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	s, err := New(cfg, path)
	must(t, err)
	rows, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "ok.flac", Size: 4}, {Filename: "bad.exe", Size: 4}}}})
	must(t, err)
	if got := s.Downloads(); len(got) != 1 || got[0].ID != rows[0].ID {
		t.Fatalf("auto-clear kept filtered history: %+v", got)
	}
	if got := s.Transfers(); len(got) != 1 || got[0].ID != rows[0].ID {
		t.Fatalf("auto-clear kept filtered transfer: %+v", got)
	}
	// A paused download that matches a newly added filter on resume is cleared too.
	must(t, s.TransferAction(rows[0].ID, "pause"))
	s.mu.Lock()
	s.cfg.Downloads.FilterPatterns = []string{"*.flac"}
	s.mu.Unlock()
	must(t, s.TransferAction(rows[0].ID, "resume"))
	if got := s.Downloads(); len(got) != 0 {
		t.Fatalf("resume left filtered history: %+v", got)
	}
	must(t, s.flushStats())
	overview, err := s.Statistics(stats.Filter{})
	must(t, err)
	if overview.Lifetime.Download.Filtered != 2 {
		t.Fatalf("filtered statistics lost: %+v", overview.Lifetime.Download)
	}
	must(t, s.Close())
	restored, err := New(cfg, path)
	must(t, err)
	defer restored.Close()
	if got := restored.Downloads(); len(got) != 0 {
		t.Fatalf("filtered history survived restart: %+v", got)
	}

	// Disabling auto-clear must retain filtered rows as before.
	cfg.Downloads.AutoClearFiltered = false
	cfg.Downloads.FilterPatterns = []string{"*.exe"}
	plain, err := New(cfg, filepath.Join(t.TempDir(), "plain.sqlite3"))
	must(t, err)
	defer plain.Close()
	kept, err := plain.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "bad.exe", Size: 4}}}})
	failIfFmt(t, err != nil || kept[0].State != "filtered" || len(plain.Downloads()) != 1, "disabled auto-clear changed filtering: %+v %v", kept, err)
}
