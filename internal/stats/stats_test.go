package stats

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestStoreReplayRetentionAndQueries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "stats.sqlite3")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2024, 1, 1, 23, 59, 59, 0, time.UTC)
	events := []Event{}
	for i := 0; i < 500; i++ {
		events = append(events, Event{ID: fmt.Sprint(i), Account: "server/local", Session: "run", Peer: fmt.Sprintf("peer%d", i%3), Direction: "download", Kind: KindCompleted, FileComplete: true, Attempt: 1, At: base.AddDate(0, 0, i), Bytes: 5, CompletedBytes: 10, Peak: uint64(i)})
	}
	if err = store.RecordBatch(events); err != nil {
		t.Fatal(err)
	}
	if err = store.RecordBatch(events); err != nil {
		t.Fatal(err)
	}
	totals, err := store.Totals(Filter{Account: "server/local"})
	if err != nil {
		t.Fatal(err)
	}
	if totals.Bytes != 2500 || totals.CompletedFiles != 500 || totals.CompletedBytes != 5000 || totals.Peak != 499 || totals.UniquePeers != 3 {
		t.Fatalf("totals: %+v", totals)
	}
	points, err := store.Series(Filter{Bins: 7})
	if err != nil {
		t.Fatal(err)
	}
	var sum uint64
	for _, p := range points {
		sum += p.Bytes
	}
	if len(points) > 7 || sum != 2500 {
		t.Fatalf("all-time series truncated: %d points, %d bytes", len(points), sum)
	}
	empty, err := store.Totals(Filter{Account: "other"})
	if err != nil || empty.Bytes != 0 {
		t.Fatalf("account isolation: %+v %v", empty, err)
	}
	cursor := ""
	count := 0
	for {
		page, err := store.Log(Filter{Limit: 19, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		count += len(page.Entries)
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if count != 500 {
		t.Fatalf("pagination: %d", count)
	}
	peers, err := store.Peers(Filter{Limit: 2})
	if err != nil || len(peers.Peers) != 2 || peers.NextCursor == "" {
		t.Fatalf("peers: %+v %v", peers, err)
	}
	cutoff := base.AddDate(0, 0, 200)
	preview, err := store.Prune(cutoff, true, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Logs != 200 || preview.Daily != 200 {
		t.Fatalf("preview: %+v", preview)
	}
	applied, err := store.Prune(cutoff, true, true, true)
	if err != nil || applied != preview {
		t.Fatalf("prune: %+v %v", applied, err)
	}
	if err = store.RecordBatch(events); err != nil {
		t.Fatal(err)
	} // Pruning must not discard dedupe keys.
	after, err := store.Totals(Filter{Account: "server/local"})
	if err != nil || !reflect.DeepEqual(totals, after) {
		t.Fatalf("pruning changed totals: %+v %v", after, err)
	}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("permissions %s: %o", name, info.Mode().Perm())
		}
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	after, err = store.Totals(Filter{Account: "server/local"})
	if err != nil || !reflect.DeepEqual(after, totals) {
		t.Fatalf("restart: %+v %v", after, err)
	}
}

func TestStoreOverflowCorruptionAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.sqlite3")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := Event{ID: "large", Account: "a", Session: "s", Peer: "p", Direction: "upload", Kind: KindProgress, At: time.Now(), Bytes: math.MaxUint64}
	if err = store.Record(e); err != nil {
		t.Fatal(err)
	}
	e.ID = "overflow"
	e.Bytes = 1
	if err = store.Record(e); err == nil {
		t.Fatal("overflow accepted")
	}
	page, err := store.Log(Filter{})
	if err != nil || len(page.Entries) != 1 {
		t.Fatalf("failed transaction was retained: %+v %v", page, err)
	}
	if _, err = store.Log(Filter{Cursor: "garbage"}); err == nil {
		t.Fatal("invalid cursor accepted")
	}
	if _, err = store.Log(Filter{Direction: "other"}); err == nil {
		t.Fatal("invalid direction accepted")
	}
	corrupt := filepath.Join(t.TempDir(), "corrupt.sqlite3")
	if err = os.WriteFile(corrupt, []byte("not a sqlite database"), 0600); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(corrupt); err == nil {
		opened.Close()
		t.Fatal("corrupt database replaced")
	}
	data, _ := os.ReadFile(corrupt)
	if string(data) != "not a sqlite database" {
		t.Fatal("corrupt database changed")
	}
}
