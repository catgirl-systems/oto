// Package storage owns the shared, versioned SQLite foundation.
//
// It intentionally contains no daemon, TUI, or domain imports. Domain code uses
// Queries and WriteTx/ReadSnapshot to choose its own transaction boundaries.
package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/catgirl-systems/oto/internal/storage/db"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaFS embed.FS

const (
	SchemaVersion  = 1
	ShareBatchSize = 1000
)

var (
	ErrUnsupportedSchema = errors.New("storage: unsupported schema")
	ErrCorrupt           = errors.New("storage: corrupt database")
	ErrDaemonLocked      = errors.New("storage: daemon lock is held")
)

// DB is a configured shared database handle. Open is for ordinary readers
// (including the TUI); OpenDaemon also takes the process-wide daemon lock.
type DB struct {
	sql        *sql.DB
	path       string
	daemonLock *DaemonLock
	writer     chan struct{}
	close      sync.Once
	closeErr   error
}

// Open opens an ordinary, unlocked state database.
func Open(path string) (*DB, error) { return open(path, false) }

// OpenTUI is an explicit alias for ordinary opens. It never takes the daemon lock.
func OpenTUI(path string) (*DB, error) { return Open(path) }

// OpenDaemon takes the advisory lock before opening or validating the database.
func OpenDaemon(path string) (*DB, error) { return open(path, true) }

func open(path string, daemon bool) (*DB, error) {
	if path == "" {
		return nil, errors.New("storage: database path required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(absolute), 0700); err != nil {
		return nil, err
	}

	var daemonLock *DaemonLock
	if daemon {
		daemonLock, err = AcquireDaemonLock(absolute)
		if err != nil {
			return nil, err
		}
	}
	fail := func(err error) (*DB, error) {
		if daemonLock != nil {
			_ = daemonLock.Close()
		}
		return nil, err
	}

	dsn := sqliteDSN(absolute)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fail(err)
	}
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(4)
	closeDB := func(err error) (*DB, error) {
		_ = sqlDB.Close()
		if daemonLock != nil {
			_ = daemonLock.Close()
		}
		return nil, err
	}
	if err = sqlDB.Ping(); err != nil {
		return closeDB(fmt.Errorf("%w: open database: %v", ErrCorrupt, err))
	}
	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return closeDB(err)
	}
	if err = bootstrapSchema(sqlDB, schema); err != nil {
		return closeDB(err)
	}
	if err = verifySchema(sqlDB); err != nil {
		return closeDB(err)
	}
	if err = chmodStateFiles(absolute); err != nil {
		return closeDB(err)
	}
	return &DB{sql: sqlDB, path: absolute, daemonLock: daemonLock, writer: make(chan struct{}, 1)}, nil
}

func sqliteDSN(path string) string {
	q := url.Values{}
	q.Add("_txlock", "immediate")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	return (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String()
}

func bootstrapSchema(db *sql.DB, schema []byte) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("storage: begin schema bootstrap: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("%w: read user_version: %v", ErrCorrupt, err)
	}
	var objects int
	if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&objects); err != nil {
		return fmt.Errorf("%w: inspect schema: %v", ErrCorrupt, err)
	}
	if version != 0 || objects != 0 {
		if version != SchemaVersion {
			return fmt.Errorf("%w: got user_version %d", ErrUnsupportedSchema, version)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("storage: finish schema check: %w", err)
		}
		committed = true
		return nil
	}
	if _, err := tx.Exec(string(schema)); err != nil {
		return fmt.Errorf("storage: initialize schema: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit schema: %w", err)
	}
	committed = true
	return nil
}

func verifySchema(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("%w: read user_version: %v", ErrCorrupt, err)
	}
	if version != SchemaVersion {
		return fmt.Errorf("%w: got user_version %d, want %d", ErrUnsupportedSchema, version, SchemaVersion)
	}
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("%w: integrity check: %v", ErrCorrupt, err)
	}
	if integrity != "ok" {
		return fmt.Errorf("%w: integrity_check: %s", ErrCorrupt, integrity)
	}
	var marker int
	if err := db.QueryRow("SELECT version FROM storage_schema WHERE id = 1").Scan(&marker); err != nil {
		return fmt.Errorf("%w: schema marker: %v", ErrCorrupt, err)
	}
	if marker != SchemaVersion {
		return fmt.Errorf("%w: schema marker %d", ErrUnsupportedSchema, marker)
	}
	return nil
}

func chmodStateFiles(path string) error {
	for _, name := range []string{path, path + "-wal", path + "-shm", path + ".lock"} {
		if err := os.Chmod(name, 0600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("storage: chmod %s: %w", name, err)
		}
	}
	return nil
}

// SQL returns the configured database for fixed or caller-owned dynamic reads.
func (d *DB) SQL() *sql.DB {
	if d == nil {
		return nil
	}
	return d.sql
}

// Queries returns generated sqlc queries backed by the database pool.
func (d *DB) Queries() *db.Queries {
	if d == nil {
		return nil
	}
	return db.New(d.sql)
}

// BeginWrite begins the driver's immediate transaction. The DSN's txlock is
// deliberate: all database/sql write transactions acquire RESERVED up front.
func (d *DB) BeginWrite(ctx context.Context) (*sql.Tx, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("storage: nil database")
	}
	return d.sql.BeginTx(ctx, nil)
}

// WriteTx runs fn in an immediate transaction and rolls it back on any error.
func (d *DB) WriteTx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	if d == nil || d.sql == nil {
		return errors.New("storage: nil database")
	}
	// Queue local writers fairly so snapshot batches cannot repeatedly beat
	// waiting transfer writes to SQLite's busy handler. Waiting is cancellable.
	select {
	case d.writer <- struct{}{}:
		defer func() { <-d.writer }()
	case <-ctx.Done():
		return ctx.Err()
	}
	tx, err := d.BeginWrite(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if fn == nil {
		return errors.New("storage: nil write callback")
	}
	if err = fn(tx); err != nil {
		return err
	}
	// Permissions must be part of the pre-commit path so a chmod failure cannot
	// turn an already committed write into a reported failure.
	if err = chmodStateFiles(d.path); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// ReadTx is a connection-pinned, query-only deferred snapshot. Use Queries to
// run generated queries without accidentally invoking the DSN's immediate tx.
type ReadTx struct{ conn *sql.Conn }

func (r *ReadTx) Conn() *sql.Conn      { return r.conn }
func (r *ReadTx) Queries() *db.Queries { return db.New(r.conn) }

// ReadSnapshot runs fn in BEGIN DEFERRED with query_only enabled. This helper
// exists because modernc's _txlock=immediate applies to database/sql BeginTx.
func (d *DB) ReadSnapshot(ctx context.Context, fn func(*ReadTx) error) (err error) {
	if d == nil || d.sql == nil {
		return errors.New("storage: nil database")
	}
	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return err
	}
	begun := false
	cleanupFailed := false
	defer func() {
		cleanupCtx := context.Background()
		if begun {
			if _, cleanupErr := conn.ExecContext(cleanupCtx, "ROLLBACK"); cleanupErr != nil {
				cleanupFailed = true
				if err == nil {
					err = cleanupErr
				}
			}
		}
		if _, cleanupErr := conn.ExecContext(cleanupCtx, "PRAGMA query_only = OFF"); cleanupErr != nil {
			cleanupFailed = true
			if err == nil {
				err = cleanupErr
			}
		}
		if cleanupFailed {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			_ = conn.Close()
			return
		}
		if closeErr := conn.Close(); err == nil {
			err = closeErr
		}
	}()
	if _, err = conn.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "BEGIN DEFERRED"); err != nil {
		return err
	}
	begun = true
	if fn == nil {
		return errors.New("storage: nil read callback")
	}
	return fn(&ReadTx{conn: conn})
}

// Close closes the pool and releases the daemon lock, if held.
func (d *DB) Close() error {
	if d == nil {
		return nil
	}
	d.close.Do(func() {
		d.closeErr = d.sql.Close()
		if d.daemonLock != nil {
			if err := d.daemonLock.Close(); d.closeErr == nil {
				d.closeErr = err
			}
		}
	})
	return d.closeErr
}

// DaemonLock is the exclusive advisory lock held by a daemon database owner.
type DaemonLock struct {
	file     *os.File
	close    sync.Once
	closeErr error
}

// AcquireDaemonLock locks databasePath + ".lock" without taking a database
// connection. Call it before any recovery work when the daemon owns the state.
func AcquireDaemonLock(databasePath string) (*DaemonLock, error) {
	absolute, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, err
	}
	if databasePath == "" {
		return nil, errors.New("storage: database path required")
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(absolute), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(absolute+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = file.Chmod(0600); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrDaemonLocked
		}
		return nil, fmt.Errorf("storage: acquire daemon lock: %w", err)
	}
	return &DaemonLock{file: file}, nil
}

// Close releases the advisory lock. It is safe to call more than once.
func (l *DaemonLock) Close() error {
	if l == nil {
		return nil
	}
	l.close.Do(func() {
		unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		closeErr := l.file.Close()
		if unlockErr != nil {
			l.closeErr = unlockErr
		} else {
			l.closeErr = closeErr
		}
	})
	return l.closeErr
}
