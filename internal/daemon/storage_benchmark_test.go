package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/storage"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

func benchmarkStorageService(b *testing.B, n int) *Service {
	b.Helper()
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "u", "p"
	cfg.Soulseek.NATPMPPortMapping, cfg.Soulseek.UPnPPortMapping = false, false
	cfg.DownloadDir = b.TempDir()
	path := filepath.Join(b.TempDir(), "state.sqlite3")
	owner, err := storage.OpenDaemon(path)
	if err != nil {
		b.Fatal(err)
	}
	rows := make([]Download, n)
	at := time.Unix(1, 0).UTC()
	for i := range rows {
		rows[i] = Download{ID: fmt.Sprintf("d-%d", i+1), Username: "peer", Filename: fmt.Sprintf("file-%d", i), Size: 1024, DownloadDir: cfg.DownloadDir, Destination: fmt.Sprintf("peer/file-%d", i), State: "queued", CreatedAt: at, UpdatedAt: at}
	}
	if err := owner.WriteTx(context.Background(), func(tx *sql.Tx) error {
		q := db.New(tx)
		for _, row := range rows {
			if err := q.UpsertDownload(context.Background(), downloadParams(row)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		_ = owner.Close()
		b.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		b.Fatal(err)
	}
	s, err := New(cfg, path)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	return s
}

func BenchmarkPersistDownloadLocked(b *testing.B) {
	for _, n := range []int{1000, 100000} {
		b.Run(fmt.Sprintf("queue%d", n), func(b *testing.B) {
			s := benchmarkStorageService(b, n)
			d := s.journal.Downloads[n/2]
			s.mu.Lock()
			defer s.mu.Unlock()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d.Offset = uint64(i + 1)
				if err := s.persistDownloadLocked(d); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
