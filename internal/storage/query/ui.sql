-- name: UpsertWishlist :exec
INSERT INTO wishlist (id, query, filter, added_at, last_run_at, result_count, result_signature, unread, notification_sequence)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET query=excluded.query, filter=excluded.filter, added_at=excluded.added_at,
 last_run_at=excluded.last_run_at, result_count=excluded.result_count, result_signature=excluded.result_signature,
 unread=excluded.unread, notification_sequence=excluded.notification_sequence;

-- name: GetWishlist :one
SELECT id, query, filter, added_at, last_run_at, result_count, result_signature, unread, notification_sequence
FROM wishlist WHERE id = ?;

-- name: ListWishlist :many
SELECT id, query, filter, added_at, last_run_at, result_count, result_signature, unread, notification_sequence
FROM wishlist ORDER BY rowid;

-- name: DeleteWishlist :exec
DELETE FROM wishlist WHERE id = ?;

-- name: UpsertHistory :exec
INSERT OR REPLACE INTO history(kind, value, recency) VALUES (?, ?, ?);

-- name: ListHistory :many
SELECT kind, value, recency FROM history WHERE kind = ? ORDER BY recency DESC, value;

-- name: DeleteHistory :exec
DELETE FROM history WHERE kind = ? AND value = ?;

-- name: ClearHistoryKind :exec
DELETE FROM history WHERE kind = ?;

-- name: TrimHistory :exec
DELETE FROM history
WHERE history.kind = ?
  AND history.value IN (
    SELECT h.value FROM history AS h
    WHERE h.kind = ?
    ORDER BY h.recency DESC, h.value
    LIMIT -1 OFFSET ?
  );

-- name: UpsertUIPreference :exec
INSERT INTO ui_preferences(key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value;

-- name: GetUIPreference :one
SELECT key, value FROM ui_preferences WHERE key = ?;

-- name: ListUIPreferences :many
SELECT key, value FROM ui_preferences ORDER BY key;

-- name: DeleteUIPreference :exec
DELETE FROM ui_preferences WHERE key = ?;
