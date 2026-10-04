package daemon

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestDownloadActionBatchesAndSkipsMissing(t *testing.T) {
	cfg := testConfig(t)
	s, err := New(cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
	must(t, err)
	var files []DownloadItem
	for i := range 40 {
		files = append(files, DownloadItem{Filename: fmt.Sprintf(`Album\%02d.flac`, i), Size: 4})
	}
	rows, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: files}})
	must(t, err)
	ids := make([]string, 0, len(rows)+1)
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	ids = append(ids, "d-missing")

	paused, err := s.DownloadAction(DownloadActionRequest{Action: "pause", IDs: ids})
	must(t, err)
	failIfFmt(t, paused.Changed != len(rows) || paused.Skipped != 1 || len(paused.Errors) != 0, "pause result %+v", paused)
	for _, d := range s.Downloads() {
		failIfFmt(t, d.State != "paused", "%s not paused: %s", d.ID, d.State)
	}
	cleared, err := s.DownloadAction(DownloadActionRequest{Action: "clear", IDs: ids[:20]})
	must(t, err)
	failIfFmt(t, cleared.Changed != 20 || len(s.Downloads()) != 20, "clear result %+v, %d left", cleared, len(s.Downloads()))
	// The journal shifted under the cached index; lookups must still land.
	again, err := s.DownloadAction(DownloadActionRequest{Action: "cancel", IDs: ids[20:]})
	must(t, err)
	failIfFmt(t, again.Changed != 20 || again.Skipped != 1, "cancel after removals %+v", again)
	for _, d := range s.Downloads() {
		failIfFmt(t, d.State != "cancelled", "%s not cancelled after the index shifted: %s", d.ID, d.State)
	}
	if _, err := s.DownloadAction(DownloadActionRequest{Action: "explode", IDs: ids}); err == nil {
		t.Fatal("unknown action accepted")
	}
}

func TestJournalIndexNeverReturnsAStaleEntry(t *testing.T) {
	var x journalIndex
	ids := []string{"a", "b", "c"}
	at := func(i int) string { return ids[i] }
	failIf(t, x.find("b", len(ids), at) != 1, "initial lookup")
	ids = []string{"a", "c"} // removal shifts positions
	failIf(t, x.find("c", len(ids), at) != 1 || x.find("b", len(ids), at) != -1, "lookup after removal")
	ids = []string{"a", "z"} // replaced in place, same length
	failIf(t, x.find("z", len(ids), at) != 1, "replacement hidden by the cache")
}
