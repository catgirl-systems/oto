package storage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestEveryConnectionSettingsAndLivePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	db, err := OpenDaemon(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var conns []*sql.Conn
	defer func() {
		for _, conn := range conns {
			conn.Close()
		}
	}()
	for i := 0; i < 4; i++ {
		conn, err := db.SQL().Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
		for name, want := range map[string]string{"journal_mode": "wal", "synchronous": "2", "foreign_keys": "1", "busy_timeout": "5000", "query_only": "0"} {
			var got string
			if err := conn.QueryRowContext(context.Background(), "PRAGMA "+name).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("connection %d: %s=%s, want %s", i, name, got, want)
			}
		}
	}
	for _, name := range []string{path, path + "-wal", path + "-shm", path + ".lock"} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s: mode %o", name, info.Mode().Perm())
		}
	}
}
