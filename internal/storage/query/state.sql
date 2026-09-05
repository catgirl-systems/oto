-- name: GetStateMeta :one
SELECT id, download_sequence, upload_sequence, upload_queue_sequence, wishlist_sequence, history_sequence, stats_since
FROM state_meta WHERE id = 1;

-- name: SetDownloadSequence :exec
UPDATE state_meta SET download_sequence = sqlc.arg(sequence) WHERE id = 1;

-- name: SetUploadSequence :exec
UPDATE state_meta SET upload_sequence = sqlc.arg(sequence) WHERE id = 1;

-- name: SetUploadQueueSequence :exec
UPDATE state_meta SET upload_queue_sequence = sqlc.arg(sequence) WHERE id = 1;

-- name: SetWishlistSequence :exec
UPDATE state_meta SET wishlist_sequence = sqlc.arg(sequence) WHERE id = 1;

-- name: SetHistorySequence :exec
UPDATE state_meta SET history_sequence = sqlc.arg(sequence) WHERE id = 1;

-- name: SetStatsSince :exec
UPDATE state_meta SET stats_since = sqlc.arg(stats_since) WHERE id = 1;

-- name: UpsertDownload :exec
INSERT INTO downloads (id, stats_account, filter_bypass, username, filename, size, offset, download_dir, destination, state, retry_at, error, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
 stats_account=excluded.stats_account, filter_bypass=excluded.filter_bypass, username=excluded.username,
 filename=excluded.filename, size=excluded.size, offset=excluded.offset, download_dir=excluded.download_dir,
 destination=excluded.destination, state=excluded.state, retry_at=excluded.retry_at, error=excluded.error,
 created_at=excluded.created_at, updated_at=excluded.updated_at;

-- name: GetDownload :one
SELECT id, stats_account, filter_bypass, username, filename, size, offset, download_dir, destination, state, retry_at, error, created_at, updated_at
FROM downloads WHERE id = ?;

-- name: ListDownloads :many
SELECT id, stats_account, filter_bypass, username, filename, size, offset, download_dir, destination, state, retry_at, error, created_at, updated_at
FROM downloads ORDER BY rowid;

-- name: DeleteDownload :exec
DELETE FROM downloads WHERE id = ?;

-- name: UpdateDownloadState :exec
UPDATE downloads SET state = ?, error = ?, retry_at = ?, updated_at = ? WHERE id = ?;

-- name: UpdateDownloadProgress :exec
UPDATE downloads SET offset = ?, updated_at = ? WHERE id = ?;

-- name: UpsertUpload :exec
INSERT INTO uploads (id, account, username, filename, direction, state, done, total, elapsed_ms, speed_bps, eta_seconds, queue, error, queue_order, fingerprint, created_at, queued_at, updated_at, recoverable)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
 account=excluded.account, username=excluded.username, filename=excluded.filename, direction=excluded.direction,
 state=excluded.state, done=excluded.done, total=excluded.total, elapsed_ms=excluded.elapsed_ms,
 speed_bps=excluded.speed_bps, eta_seconds=excluded.eta_seconds, queue=excluded.queue, error=excluded.error,
 queue_order=excluded.queue_order, fingerprint=excluded.fingerprint, created_at=excluded.created_at,
 queued_at=excluded.queued_at, updated_at=excluded.updated_at, recoverable=excluded.recoverable;

-- name: GetUpload :one
SELECT id, account, username, filename, direction, state, done, total, elapsed_ms, speed_bps, eta_seconds, queue, error, queue_order, fingerprint, created_at, queued_at, updated_at, recoverable
FROM uploads WHERE id = ?;

-- name: ListUploads :many
SELECT id, account, username, filename, direction, state, done, total, elapsed_ms, speed_bps, eta_seconds, queue, error, queue_order, fingerprint, created_at, queued_at, updated_at, recoverable
FROM uploads ORDER BY rowid;

-- name: DeleteUpload :exec
DELETE FROM uploads WHERE id = ?;

-- name: UpdateUploadState :exec
UPDATE uploads SET state = ?, error = ?, recoverable = ?, updated_at = ? WHERE id = ?;

-- name: UpdateUploadProgress :exec
UPDATE uploads SET done = ?, elapsed_ms = ?, speed_bps = ?, eta_seconds = ?, updated_at = ? WHERE id = ?;

-- name: UpsertActiveAttempt :exec
INSERT INTO active_attempts (id, transfer_id, attempt, direction, username, filename, event_id, payload)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET transfer_id=excluded.transfer_id, attempt=excluded.attempt,
 direction=excluded.direction, username=excluded.username, filename=excluded.filename,
 event_id=excluded.event_id, payload=excluded.payload;

-- name: GetActiveAttempt :one
SELECT id, transfer_id, attempt, direction, username, filename, event_id, payload
FROM active_attempts WHERE id = ?;

-- name: ListActiveAttempts :many
SELECT id, transfer_id, attempt, direction, username, filename, event_id, payload
FROM active_attempts ORDER BY id;

-- name: DeleteActiveAttempt :exec
DELETE FROM active_attempts WHERE id = ?;

-- name: DeleteAllActiveAttempts :exec
DELETE FROM active_attempts;
