// Package storage is the CGO-free SQLite foundation for the single durable
// state.sqlite3 database. It owns operational records for transfers, history,
// wishlist state, local and saved remote inventories, statistics, and Community.
// Configuration remains JSON-only. Schema 2 upgrades schema 1 only in OpenDaemon,
// under the daemon lock, after validating a private SQLite snapshot backup.
// OpenTUI never upgrades an existing database. Unknown schemas fail safely.
// SQLite WAL/SHM files and the daemon lock are private sidecars; never copy a live
// database without its WAL state. Use SQLite backup or stop every reader/writer.
//
// SQLite INTEGER columns holding time.Time values use UTC Unix nanoseconds;
// nullable times are NULL. Boolean fields use INTEGER 0/1. Every uint64 that
// must not pass through SQLite's signed INTEGER affinity is an exactly 8-byte
// big-endian BLOB and must be encoded/decoded with EncodeUint64/DecodeUint64.
// Generated rows intentionally expose those blobs as []byte and booleans as
// int64 so storage does not import domain types.
package storage
