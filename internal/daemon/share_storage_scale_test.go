package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage"
)

func scaleShareEntries(n int) []soulseek.ShareEntry {
	entries := make([]soulseek.ShareEntry, n)
	for i := range entries {
		entries[i] = soulseek.ShareEntry{Name: fmt.Sprintf("music/%d.flac", i), Size: uint64(i)}
	}
	return entries
}

func TestShareSnapshotAllowsWritesBetweenBatches(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const count = 10000
	finished := make(chan error, 1)
	go func() {
		id, err := stageShareSnapshot(ctx, db, "remote", "peer", nil, nil, nil, scaleShareEntries(count))
		if err == nil {
			err = publishShareSnapshot(ctx, db, id, "remote", "peer")
		}
		finished <- err
	}()
	progress := false
	for !progress && ctx.Err() == nil {
		err := db.WriteTx(ctx, func(tx *sql.Tx) error {
			var n int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM share_entries").Scan(&n); err != nil {
				return err
			}
			progress = n > 0 && n < count
			// An independent transfer writer must acquire the writer while the
			// inventory is incomplete, not merely after head publication.
			return db.Queries().WithTx(tx).UpsertDownload(ctx, downloadParams(Download{
				ID: "d-1", Username: "peer", Filename: "concurrent.flac", Size: count,
				Offset: uint64(n), State: "running", CreatedAt: time.Now().UTC(),
			}))
		})
		if err != nil {
			cancel()
			<-finished
			t.Fatal(err)
		}
		if !progress {
			time.Sleep(time.Millisecond)
		}
	}
	if !progress {
		cancel()
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if !progress {
		t.Fatal("inventory held the writer for the entire publication")
	}
}

func BenchmarkShareSnapshot100000(b *testing.B) {
	db, err := storage.Open(filepath.Join(b.TempDir(), "state.sqlite3"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	s := &Service{stateDB: db}
	ctx := context.Background()
	entries := scaleShareEntries(100000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id, err := stageShareSnapshot(ctx, db, "remote", "peer", nil, nil, nil, entries)
		if err == nil {
			err = publishShareSnapshot(ctx, db, id, "remote", "peer")
		}
		if err != nil {
			b.Fatal(err)
		}
		cache, err := s.loadRemoteShareCache("peer")
		if err != nil || len(cache.Entries) != len(entries) || cache.Entries[len(entries)-1] != entries[len(entries)-1] {
			b.Fatalf("restore: %d entries, %v", len(cache.Entries), err)
		}
		b.StopTimer()
		if err := gcShareSnapshots(ctx, db); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}
