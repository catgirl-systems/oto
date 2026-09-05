package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFilteredDownloadsPersistAndRequireExplicitForce(t *testing.T) {
	cfg := testConfig(t)
	cfg.Downloads.FiltersEnabled = true
	cfg.Downloads.FilterPatterns = []string{"*.exe", "blocked/"}
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	s, err := New(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: `Music\APP.EXE`, Size: 10}, {Filename: `Music\song.flac`, Size: 20}, {Filename: `blocked\readme.txt`, Size: 5}}}})
	if err != nil || len(rows) != 3 || rows[0].State != "filtered" || rows[1].State != "queued" || rows[2].State != "filtered" {
		t.Fatalf("queue: %+v %v", rows, err)
	}
	if _, err = s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "ok.txt", Size: 1}, {Filename: "../bad.exe", Size: 1}}}}); err == nil {
		t.Fatal("invalid filtered path accepted")
	}
	if len(s.journal.Downloads) != 3 {
		t.Fatal("invalid batch partially committed")
	}
	if err = s.TransferAction(rows[0].ID, "retry"); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.downloadByID(rows[0].ID); d.State != "filtered" || d.FilterBypass {
		t.Fatal("ordinary retry bypassed filter")
	}
	result, err := s.ForceDownloads([]string{rows[0].ID, rows[0].ID, rows[1].ID, rows[2].ID})
	if err != nil || result.Changed != 2 || result.Skipped != 1 {
		t.Fatalf("force: %+v %v", result, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, i := range []int{0, 2} {
		if d, _ := s.downloadByID(rows[i].ID); !d.FilterBypass || d.State != "queued" {
			t.Fatalf("lost durable bypass: %+v", d)
		}
	}
	again, err := s.QueueDownloads([]DownloadRequest{{Username: "peer", Files: []DownloadItem{{Filename: "new.exe", Size: 1}}}})
	if err != nil || again[0].State != "filtered" {
		t.Fatal("bypass escaped its record")
	}
	files, err := os.ReadDir(cfg.DownloadDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(files) > 0 {
		t.Fatalf("offline/filtered records created files: %v", files)
	}
}
