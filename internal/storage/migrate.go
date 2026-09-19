package storage

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// Only open's daemon branch calls this, after acquiring the daemon lock and
// before returning a DB to services or publishing IPC readiness.
func migrateV1(ctx context.Context, database *sql.DB, path string, communityDDL []byte) error {
	if err := verifySchema(ctx, database, 1); err != nil {
		return err
	}
	backup, err := snapshotV1(ctx, database, path)
	if err != nil {
		return fmt.Errorf("storage: cannot back up schema 1; upgrade not started (check free space and directory permissions): %w", err)
	}
	if err := upgradeV1(ctx, database, communityDDL); err != nil {
		return fmt.Errorf("storage: schema 1 to 2 upgrade failed; close conflicting database writers and retry; validated backup at %s: %w", backup, err)
	}
	return nil
}

func upgradeV1(ctx context.Context, database *sql.DB, communityDDL []byte) error {
	tx, err := database.BeginTx(ctx, nil) // DSN uses BEGIN IMMEDIATE.
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := verifySchema(ctx, tx, 1); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, string(communityDDL)); err != nil {
		return err
	}
	// Do not rebuild history/UI/transfer/share tables: already-open v1 TUIs
	// still use their original statements. Legacy roots default to public.
	if _, err := tx.ExecContext(ctx, `
ALTER TABLE share_roots ADD COLUMN access TEXT NOT NULL DEFAULT 'public' CHECK (access IN ('public', 'buddy', 'trusted'));
DROP TABLE storage_schema;
CREATE TABLE storage_schema (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    version INTEGER NOT NULL CHECK (version = 2)
);
INSERT INTO storage_schema(id, version) VALUES (1, 2);
PRAGMA user_version = 2;`); err != nil {
		return err
	}
	if err := upgradeV2(ctx, tx); err != nil {
		return err
	}
	if err := verifySchema(ctx, tx, SchemaVersion); err != nil {
		return err
	}
	return tx.Commit()
}

// apiSchemaDDL is the purely additive schema 2 to 3 step: the api_tokens table.
const apiSchemaDDL = `
CREATE TABLE IF NOT EXISTS api_tokens (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    user_agent TEXT NOT NULL DEFAULT '',
    source_ip TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    last_used_at INTEGER,
    expires_at INTEGER
);
DROP TABLE storage_schema;
CREATE TABLE storage_schema (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    version INTEGER NOT NULL CHECK (version = 3)
);
INSERT INTO storage_schema(id, version) VALUES (1, 3);
PRAGMA user_version = 3;`

// migrateV2 upgrades an open schema 2 database to 3. Additive only: no backup.
func migrateV2(ctx context.Context, database *sql.DB) error {
	tx, err := database.BeginTx(ctx, nil) // DSN uses BEGIN IMMEDIATE.
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := verifySchema(ctx, tx, 2); err != nil {
		return err
	}
	if err := upgradeV2(ctx, tx); err != nil {
		return err
	}
	if err := verifySchema(ctx, tx, SchemaVersion); err != nil {
		return err
	}
	return tx.Commit()
}

func upgradeV2(_ context.Context, tx *sql.Tx) error {
	_, err := tx.Exec(apiSchemaDDL)
	return err
}

// VACUUM INTO is SQLite's consistent snapshot facility; unlike copying the main
// file it includes committed WAL content while allowing old readers to continue.
// A private pre-created, empty destination prevents permissive creation modes or
// overwriting an earlier backup. Validate and sync it before changing the source.
func snapshotV1(ctx context.Context, database *sql.DB, path string) (backup string, err error) {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".v1-backup-*.sqlite3")
	if err != nil {
		return "", err
	}
	backup = f.Name()
	defer func() {
		_ = f.Close()
		if err != nil {
			_ = os.Remove(backup)
		}
	}()
	if _, err = database.ExecContext(ctx, "VACUUM INTO ?", backup); err != nil {
		return backup, err
	}
	if err = f.Sync(); err != nil {
		return backup, err
	}
	if err = f.Close(); err != nil {
		return backup, err
	}
	dsn := (&url.URL{Scheme: "file", Path: backup, RawQuery: "mode=ro"}).String()
	snapshot, err := sql.Open("sqlite", dsn)
	if err != nil {
		return backup, err
	}
	snapshot.SetMaxOpenConns(1)
	err = verifySchema(ctx, snapshot, 1)
	closeErr := snapshot.Close()
	if err != nil {
		return backup, err
	}
	if closeErr != nil {
		return backup, closeErr
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return backup, err
	}
	defer dir.Close()
	return backup, dir.Sync()
}
