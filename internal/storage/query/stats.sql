-- name: InsertSeen :execresult
INSERT OR IGNORE INTO seen(id) VALUES (?);

-- name: InsertEvent :exec
INSERT INTO events(id, account, peer, direction, session, kind, at, data)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetTotalsData :one
SELECT data FROM totals
WHERE account = ? AND peer = ? AND direction = ? AND session = ? AND day = ?;

-- name: UpsertTotals :exec
INSERT INTO totals(account, peer, direction, session, day, data)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(account, peer, direction, session, day)
DO UPDATE SET data = excluded.data;

-- name: CountEventsBefore :one
SELECT count(*) FROM events WHERE at < ?;

-- name: DeleteEventsBefore :exec
DELETE FROM events WHERE at < ?;

-- name: CountDailyTotalsBefore :one
SELECT count(*) FROM totals WHERE day <> '' AND day < ?;

-- name: DeleteDailyTotalsBefore :exec
DELETE FROM totals WHERE day <> '' AND day < ?;
