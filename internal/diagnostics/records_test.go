package diagnostics

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestRecords(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("daemon.log", `{"time":"2025-01-02T03:04:06Z","level":"INFO","msg":"second","component":"daemon"}`+"\n"+
		`{"time":"2025-01-02T03:04:07Z","level":"ERROR","msg":"third","peer_username":"ann"}`+"\n")
	write("daemon-000001.log.gz", "")
	f, err := os.Create(filepath.Join(dir, "daemon-000001.log.gz"))
	if err != nil {
		t.Fatal(err)
	}
	z := gzip.NewWriter(f)
	_, _ = z.Write([]byte(`{"time":"2025-01-02T03:04:05Z","level":"WARN","msg":"first","component":"daemon"}` + "\n"))
	_ = z.Close()
	_ = f.Close()
	write("daemon-000001.log.gz.tmp", "ignored")
	write("other.log", "ignored")

	m := &Manager{directory: dir}
	records, err := m.Records(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || records[0].Msg != "first" || records[1].Msg != "second" || records[2].Msg != "third" {
		t.Fatalf("records = %+v", records)
	}
	if records[0].Level != "WARN" || records[2].Level != "ERROR" {
		t.Fatalf("levels = %q %q", records[0].Level, records[2].Level)
	}
	if records[2].Text != "peer_username=ann" {
		t.Fatalf("attrs = %q", records[2].Text)
	}
	// Limit keeps the newest records.
	recent, err := m.Records(2)
	if err != nil || len(recent) != 2 || recent[0].Msg != "second" {
		t.Fatalf("recent = %+v err %v", recent, err)
	}
}
