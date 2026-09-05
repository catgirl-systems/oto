package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenReopenAndSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.sqlite3")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := db.SQL().Stats().MaxOpenConnections; got != 4 {
		t.Fatalf("max connections = %d, want 4", got)
	}
	for _, pragma := range []struct{ name, want string }{{"user_version", "1"}, {"foreign_keys", "1"}, {"synchronous", "2"}} {
		var got string
		if err := db.SQL().QueryRow("PRAGMA " + pragma.name).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != pragma.want {
			t.Fatalf("%s = %q, want %q", pragma.name, got, pragma.want)
		}
	}
	if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO ui_preferences(key, value) VALUES ('test', 'ok')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filepath.Dir(path), path, path + ".lock", path + "-wal", path + "-shm"} {
		if info, err := os.Stat(name); err == nil {
			want := os.FileMode(0600)
			if info.IsDir() {
				want = 0700
			}
			if info.Mode().Perm() != want {
				t.Errorf("%s mode %o, want %o", name, info.Mode().Perm(), want)
			}
		}
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var value string
	if err := reopened.SQL().QueryRow("SELECT value FROM ui_preferences WHERE key='test'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "ok" {
		t.Fatalf("value = %q", value)
	}
}

func TestRejectsCorruptAndUnsupported(t *testing.T) {
	corrupt := filepath.Join(t.TempDir(), "corrupt.sqlite3")
	if err := os.WriteFile(corrupt, []byte("not sqlite"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(corrupt); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt error = %v", err)
	}

	unsupported := filepath.Join(t.TempDir(), "unsupported.sqlite3")
	db, err := Open(unsupported)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec("PRAGMA user_version = 2"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(unsupported); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("unsupported error = %v", err)
	}
}

func TestUint64(t *testing.T) {
	for _, value := range []uint64{0, 1, 0x0102030405060708, ^uint64(0)} {
		if got, err := DecodeUint64(EncodeUint64(value)); err != nil || got != value {
			t.Fatalf("round trip %d: %d %v", value, got, err)
		}
	}
	for _, value := range [][]byte{nil, make([]byte, 7), make([]byte, 9)} {
		if _, err := DecodeUint64(value); !errors.Is(err, ErrInvalidUint64) {
			t.Fatalf("invalid length %d: %v", len(value), err)
		}
	}
}

func TestDaemonLockAndReadSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	first, err := OpenDaemon(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDaemon(path); !errors.Is(err, ErrDaemonLocked) {
		t.Fatalf("second daemon open = %v", err)
	}
	if err := first.ReadSnapshot(context.Background(), func(snapshot *ReadTx) error {
		var count int
		if err := snapshot.Conn().QueryRowContext(context.Background(), "SELECT count(*) FROM state_meta").Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatalf("state rows = %d", count)
		}
		_, err := snapshot.Conn().ExecContext(context.Background(), "INSERT INTO ui_preferences(key,value) VALUES ('no','write')")
		if err == nil {
			t.Fatal("read snapshot allowed a write")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenDaemon(path)
	if err != nil {
		t.Fatal(err)
	}
	second.Close()
}
