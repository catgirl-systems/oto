// Package storage is the CGO-free SQLite foundation for the single durable
// state.sqlite3 database. It owns operational records for transfers, history,
// wishlist state, local and saved remote inventories, and statistics; config
// remains JSON-only. Schema version 1 is fresh-only: unsupported schemas are
// rejected and migrations are not supported. SQLite WAL/SHM files and the
// daemon lock are private sidecars; copy the complete database only while the
// daemon and TUI are stopped, or use SQLite backup.
//
// SQLite INTEGER columns holding time.Time values use UTC Unix nanoseconds;
// nullable times are NULL. Boolean fields use INTEGER 0/1. Every uint64 that
// must not pass through SQLite's signed INTEGER affinity is an exactly 8-byte
// big-endian BLOB and must be encoded/decoded with EncodeUint64/DecodeUint64.
// Generated rows intentionally expose those blobs as []byte and booleans as
// int64 so storage does not import domain types.
package storage
