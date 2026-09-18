package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Frozen verbatim from 7aa4c41, not synthesized from the new schema.
//
//go:embed testdata/schema_v1.sql
var schemaV1 []byte

func legacyDatabase(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(string(schemaV1)); err != nil {
		t.Fatal(err)
	}
	// Keep committed data in WAL so a naive main-file copy cannot pass.
	if _, err := db.Exec(`
PRAGMA wal_autocheckpoint = 0;
UPDATE state_meta SET history_sequence = x'FFFFFFFFFFFFFFFF', stats_since = 123;
INSERT INTO downloads(id,username,filename,size,offset,destination,state,created_at,updated_at) VALUES ('d1','Alice','song',x'FFFFFFFFFFFFFFFF',zeroblob(8),'song','paused',1,2);
INSERT INTO uploads(id,account,username,filename,direction,state,done,total,speed_bps,queue_order,created_at,queued_at,updated_at) VALUES ('u1','local/Me','Alice','song','upload','queued',zeroblob(8),x'FFFFFFFFFFFFFFFF',zeroblob(8),x'FFFFFFFFFFFFFFFF',1,2,3);
INSERT INTO active_attempts VALUES ('a1','d1',x'FFFFFFFFFFFFFFFF','download','Alice','song','e1',x'010203');
INSERT INTO seen VALUES ('s1');
INSERT INTO events(id,account,data) VALUES ('e1','local/Me','{}');
INSERT INTO totals(account,peer,direction,session,day,data) VALUES ('local/Me','Alice','download','session','2026-09-07','{}');
INSERT INTO wishlist(id,query,added_at,notification_sequence) VALUES ('w1','music',1,x'FFFFFFFFFFFFFFFF');
INSERT INTO history VALUES ('search','music',x'FFFFFFFFFFFFFFFF');
INSERT INTO ui_preferences VALUES ('sort','name');
INSERT INTO share_snapshots(id,source,normalized_username,username,saved_at,state,created_at) VALUES (1,'saved','alice','Alice',1,'published',1);
INSERT INTO share_heads VALUES ('saved','alice',1);
INSERT INTO share_roots VALUES (1,0,'music','/music');
INSERT INTO share_exclusions VALUES (1,0,'*.tmp');
INSERT INTO share_entries(snapshot_id,ordinal,kind,root,path,name,size) VALUES (1,0,'remote','music','song','song',x'FFFFFFFFFFFFFFFF');
`); err != nil {
		t.Fatal(err)
	}
	return db, path
}

type legacyTable struct {
	columns string
	rows    [][]any
}

func tableRows(t *testing.T, db *sql.DB, name, columns string) [][]any {
	t.Helper()
	rows, err := db.Query("SELECT " + columns + " FROM " + name + " ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]any
	for rows.Next() {
		values, ptrs := make([]any, len(names)), make([]any, len(names))
		for i := range ptrs {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func legacyContents(t *testing.T, db *sql.DB) map[string]legacyTable {
	t.Helper()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name <> 'storage_schema' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	result := map[string]legacyTable{}
	for _, name := range tables {
		rows, err := db.Query("SELECT * FROM " + name + " LIMIT 0")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		_ = rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		for i := range columns {
			columns[i] = `"` + columns[i] + `"`
		}
		projection := strings.Join(columns, ",")
		result[name] = legacyTable{projection, tableRows(t, db, name, projection)}
	}
	return result
}

func assertLegacyContents(t *testing.T, db *sql.DB, before map[string]legacyTable) {
	t.Helper()
	for name, table := range before {
		if got := tableRows(t, db, name, table.columns); !reflect.DeepEqual(got, table.rows) {
			t.Errorf("legacy table %s changed", name)
		}
	}
}

func TestCommunityMigrationPreservesV1AndWALBackup(t *testing.T) {
	legacy, path := legacyDatabase(t)
	before := legacyContents(t, legacy)
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("fixture has no WAL: %v", err)
	}
	// A statement prepared by a v1 TUI must survive the schema change.
	statement, err := legacy.Prepare("INSERT INTO history(kind,value,recency) VALUES ('search',?,zeroblob(8))")
	if err != nil {
		t.Fatal(err)
	}
	defer statement.Close()
	db, err := OpenDaemon(path)
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyContents(t, db.SQL(), before)
	var access string
	if err := db.SQL().QueryRow("SELECT access FROM share_roots").Scan(&access); err != nil || access != "public" {
		t.Fatalf("legacy share access: %s %v", access, err)
	}
	if _, err := db.SQL().Exec("UPDATE storage_schema SET version = 1"); err == nil {
		t.Fatal("v2 marker still permits v1")
	}
	if err := verifySchema(context.Background(), db.SQL(), 1); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("downgrade version guard: %v", err)
	}
	backups, _ := filepath.Glob(path + ".v1-backup-*.sqlite3")
	failIfFmt(t, len(backups) != 1, "backups: %v", backups)
	info, err := os.Stat(backups[0])
	failIfFmt(t, err != nil || info.Mode().Perm() != 0600, "backup permissions: %v %v", info, err)
	backup, err := sql.Open("sqlite", "file:"+backups[0]+"?mode=ro")
	must(t, err)
	defer backup.Close()
	must(t, verifySchema(context.Background(), backup, 1))
	assertLegacyContents(t, backup, before)
	if _, err := statement.Exec("after upgrade"); err != nil {
		t.Fatalf("old TUI statement: %v", err)
	}
	must(t, db.Close())
	for _, open := range []func(string) (*DB, error){OpenDaemon, Open} {
		reopened, err := open(path)
		must(t, err)
		var count int
		if err := reopened.SQL().QueryRow("SELECT count(*) FROM history WHERE value = 'after upgrade'").Scan(&count); err != nil || count != 1 {
			t.Fatalf("old TUI write lost: %d %v", count, err)
		}
		_ = reopened.Close()
	}
	afterBackups, _ := filepath.Glob(path + ".v1-backup-*.sqlite3")
	failIf(t, !reflect.DeepEqual(backups, afterBackups), "repeated opens created new backups")
}

func TestCommunityMigrationOnlyLockedDaemon(t *testing.T) {
	legacy, path := legacyDatabase(t)
	lock, err := AcquireDaemonLock(path)
	must(t, err)
	defer lock.Close()
	for _, open := range []func(string) (*DB, error){Open} {
		if db, err := open(path); !errors.Is(err, ErrUnsupportedSchema) || !strings.Contains(err.Error(), "restart the daemon") {
			if db != nil {
				_ = db.Close()
			}
			t.Fatalf("ordinary open upgraded under old daemon: %v", err)
		}
	}
	if _, err := OpenDaemon(path); !errors.Is(err, ErrDaemonLocked) {
		t.Fatalf("daemon lock: %v", err)
	}
	if err := verifySchema(context.Background(), legacy, 1); err != nil {
		t.Fatal(err)
	}
	backups, _ := filepath.Glob(path + ".v1-backup-*.sqlite3")
	if len(backups) != 0 {
		t.Fatalf("non-owner made a backup: %v", backups)
	}
}

func TestCommunityMigrationRollback(t *testing.T) {
	for name, ddl := range map[string]string{
		"DDL error":       string(communitySchema) + "; SELECT * FROM nonexistent;",
		"precommit check": string(communitySchema) + "; CREATE TABLE invalid_fk (account TEXT REFERENCES community_accounts(account) DEFERRABLE INITIALLY DEFERRED); INSERT INTO invalid_fk VALUES ('missing');",
	} {
		t.Run(name, func(t *testing.T) {
			legacy, path := legacyDatabase(t)
			before := legacyContents(t, legacy)
			err := migrateV1(context.Background(), legacy, path, []byte(ddl))
			if err == nil || !strings.Contains(err.Error(), "validated backup") {
				t.Fatalf("failed upgrade: %v", err)
			}
			if err := verifySchema(context.Background(), legacy, 1); err != nil {
				t.Fatal(err)
			}
			assertLegacyContents(t, legacy, before)
			if _, err := legacy.Exec("UPDATE storage_schema SET version = 2"); err == nil {
				t.Fatal("rollback lost the original marker CHECK constraint")
			}
			var count int
			if err := legacy.QueryRow("SELECT count(*) FROM sqlite_master WHERE name LIKE 'community_%' OR name = 'invalid_fk'").Scan(&count); err != nil || count != 0 {
				t.Fatalf("partially migrated tables: %d %v", count, err)
			}
			// A failed attempt must neither poison the source nor overwrite its
			// validated backup on the next daemon startup.
			db, err := OpenDaemon(path)
			if err != nil {
				t.Fatal(err)
			}
			_ = db.Close()
			backups, _ := filepath.Glob(path + ".v1-backup-*.sqlite3")
			if len(backups) != 2 {
				t.Fatalf("retry backups: %v", backups)
			}
		})
	}
}

func TestCommunityMigrationConcurrentHistory(t *testing.T) {
	legacy, path := legacyDatabase(t)
	tx, err := legacy.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO history VALUES ('filter','concurrent',zeroblob(8))"); err != nil {
		t.Fatal(err)
	}
	started, done := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		db, err := OpenDaemon(path)
		if db != nil {
			_ = db.Close()
		}
		done <- err
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("upgrade bypassed active legacy writer: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("upgrade did not resume after legacy writer")
	}
	var count int
	if err := legacy.QueryRow("SELECT count(*) FROM history WHERE value = 'concurrent'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("concurrent history: %d %v", count, err)
	}
}

func TestCommunityMigrationBusyAndBackupFailure(t *testing.T) {
	legacy, path := legacyDatabase(t)
	before := legacyContents(t, legacy)
	if err := migrateV1(context.Background(), legacy, filepath.Join(path, "missing", "db"), communitySchema); err == nil {
		t.Fatal("backup failure did not stop upgrade")
	}
	tx, err := legacy.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	contender, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()
	contender.SetMaxOpenConns(1)
	if _, err := contender.Exec("PRAGMA busy_timeout=25"); err != nil {
		t.Fatal(err)
	}
	if err := migrateV1(context.Background(), contender, path, communitySchema); err == nil {
		t.Fatal("upgrade bypassed busy writer")
	}
	_ = tx.Rollback()
	if err := verifySchema(context.Background(), legacy, 1); err != nil {
		t.Fatal(err)
	}
	assertLegacyContents(t, legacy, before)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := upgradeV1(ctx, legacy, communitySchema); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled upgrade: %v", err)
	}
	if err := verifySchema(context.Background(), legacy, 1); err != nil {
		t.Fatal(err)
	}
	// Future schemas must not get a backup or an attempted downgrade.
	if _, err := legacy.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	if db, err := OpenDaemon(path); !errors.Is(err, ErrUnsupportedSchema) {
		if db != nil {
			_ = db.Close()
		}
		t.Fatal(fmt.Errorf("future schema: %w", err))
	}
}

func TestCommunityMigrationLiveReader(t *testing.T) {
	legacy, path := legacyDatabase(t)
	ctx := context.Background()
	reader, err := legacy.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.ExecContext(ctx, "BEGIN DEFERRED"); err != nil {
		t.Fatal(err)
	}
	defer reader.ExecContext(ctx, "ROLLBACK")
	var version int
	if err := reader.QueryRowContext(ctx, "SELECT version FROM storage_schema").Scan(&version); err != nil || version != 1 {
		t.Fatalf("old reader snapshot: %d %v", version, err)
	}
	database, err := OpenDaemon(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := reader.QueryRowContext(ctx, "SELECT version FROM storage_schema").Scan(&version); err != nil || version != 1 {
		t.Fatalf("upgrade invalidated old snapshot: %d %v", version, err)
	}
	if _, err := reader.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err := reader.QueryRowContext(ctx, "SELECT version FROM storage_schema").Scan(&version); err != nil || version != 2 {
		t.Fatalf("old connection cannot resynchronize: %d %v", version, err)
	}
	if _, err := reader.ExecContext(ctx, "UPDATE ui_preferences SET value='after upgrade' WHERE key='sort'"); err != nil {
		t.Fatalf("old connection cannot write after read: %v", err)
	}
}

func TestCommunityFreshBootstrapRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	raw, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := bootstrapSchema(raw, append(append([]byte(nil), schema...), []byte("; SELECT * FROM nonexistent;")...)); err == nil {
		t.Fatal("invalid fresh schema succeeded")
	}
	var version, objects int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 0 {
		t.Fatalf("failed bootstrap changed version: %d %v", version, err)
	}
	if err := raw.QueryRow("SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&objects); err != nil || objects != 0 {
		t.Fatalf("failed bootstrap retained objects: %d %v", objects, err)
	}
	database, err := OpenDaemon(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := verifySchema(context.Background(), database.SQL(), 2); err != nil {
		t.Fatal(err)
	}
}
