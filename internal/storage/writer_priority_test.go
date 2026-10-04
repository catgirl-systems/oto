package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestOrdinaryWritesOvertakeQueuedBulkBatches(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	must(t, err)
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	const batch = 30 * time.Millisecond
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			for ctx.Err() == nil {
				_ = db.WriteTxBulk(ctx, func(*sql.Tx) error { time.Sleep(batch); return nil })
			}
		})
	}
	time.Sleep(3 * batch) // let the bulk writers form a queue
	start := time.Now()
	must(t, db.WriteTx(context.Background(), func(*sql.Tx) error { return nil }))
	waited := time.Since(start)
	cancel()
	wg.Wait()
	// Behind the queue it would wait for every queued batch (~3 × 30 ms).
	failIfFmt(t, waited > batch+batch/2+10*time.Millisecond, "ordinary write waited %v behind bulk batches", waited)
}
