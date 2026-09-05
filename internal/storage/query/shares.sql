-- name: CreateShareSnapshot :one
INSERT INTO share_snapshots(source, normalized_username, username, saved_at, state, created_at)
VALUES (?, ?, ?, ?, 'staging', ?) RETURNING id;

-- name: GetShareSnapshot :one
SELECT id, source, normalized_username, username, saved_at, state, created_at
FROM share_snapshots WHERE id = ?;

-- name: PublishShareSnapshot :execresult
UPDATE share_snapshots SET state = 'published' WHERE id = ? AND state = 'staging';

-- name: SetShareHead :exec
INSERT INTO share_heads(source, normalized_username, snapshot_id) VALUES (?, ?, ?)
ON CONFLICT(source, normalized_username) DO UPDATE SET snapshot_id = excluded.snapshot_id;

-- name: GetShareHead :one
SELECT source, normalized_username, snapshot_id FROM share_heads
WHERE source = ? AND normalized_username = ?;

-- name: ListShareHeads :many
SELECT source, normalized_username, snapshot_id FROM share_heads ORDER BY source, normalized_username;

-- name: InsertShareRoot :exec
INSERT INTO share_roots(snapshot_id, ordinal, name, path) VALUES (?, ?, ?, ?);

-- name: InsertShareRoots :many
INSERT INTO share_roots(snapshot_id, ordinal, name, path) VALUES (?, ?, ?, ?) RETURNING ordinal;

-- name: ListShareRoots :many
SELECT snapshot_id, ordinal, name, path FROM share_roots
WHERE snapshot_id = ? ORDER BY ordinal;

-- name: InsertShareExclusion :exec
INSERT INTO share_exclusions(snapshot_id, ordinal, pattern) VALUES (?, ?, ?);

-- name: InsertShareExclusions :many
INSERT INTO share_exclusions(snapshot_id, ordinal, pattern) VALUES (?, ?, ?) RETURNING ordinal;

-- name: ListShareExclusions :many
SELECT snapshot_id, ordinal, pattern FROM share_exclusions
WHERE snapshot_id = ? ORDER BY ordinal;

-- name: InsertShareEntry :exec
INSERT INTO share_entries (snapshot_id, ordinal, kind, root, path, name, size, directory, private, vbr, vbr_known, extension, bitrate, duration, sample_rate, bit_depth, audio_source, fingerprint_size, fingerprint_mtime, fingerprint_ctime, extractor_version)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertShareEntries :many
INSERT INTO share_entries (snapshot_id, ordinal, kind, root, path, name, size, directory, private, vbr, vbr_known, extension, bitrate, duration, sample_rate, bit_depth, audio_source, fingerprint_size, fingerprint_mtime, fingerprint_ctime, extractor_version)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING ordinal;

-- name: ListShareEntries :many
SELECT snapshot_id, ordinal, kind, root, path, name, size, directory, private, vbr, vbr_known, extension, bitrate, duration, sample_rate, bit_depth, audio_source, fingerprint_size, fingerprint_mtime, fingerprint_ctime, extractor_version
FROM share_entries WHERE snapshot_id = ? ORDER BY ordinal;

-- name: ListCollectableShareSnapshots :many
SELECT s.id, s.source, s.normalized_username, s.username, s.saved_at, s.state, s.created_at
FROM share_snapshots AS s
WHERE s.state = 'staging'
   OR (s.state = 'published'
       AND NOT EXISTS (SELECT 1 FROM share_heads AS h WHERE h.snapshot_id = s.id))
ORDER BY s.id;

-- name: DeleteShareSnapshot :exec
DELETE FROM share_snapshots WHERE id = ? AND state <> 'staging'
  AND NOT EXISTS (SELECT 1 FROM share_heads WHERE snapshot_id = share_snapshots.id);

-- name: DeleteStagingShareSnapshot :exec
DELETE FROM share_snapshots WHERE id = ? AND state = 'staging'
  AND NOT EXISTS (SELECT 1 FROM share_heads WHERE snapshot_id = share_snapshots.id);


-- name: CountShareEntries :one
SELECT count(*) FROM share_entries WHERE snapshot_id = ?;

-- name: CountShareRoots :one
SELECT count(*) FROM share_roots WHERE snapshot_id = ?;

-- name: CountShareExclusions :one
SELECT count(*) FROM share_exclusions WHERE snapshot_id = ?;
-- name: DeleteShareEntriesBatch :exec
DELETE FROM share_entries
WHERE rowid IN (SELECT e.rowid FROM share_entries AS e WHERE e.snapshot_id = ? LIMIT ?);

-- name: DeleteShareRootsBatch :exec
DELETE FROM share_roots
WHERE rowid IN (SELECT r.rowid FROM share_roots AS r WHERE r.snapshot_id = ? LIMIT ?);

-- name: DeleteShareExclusionsBatch :exec
DELETE FROM share_exclusions
WHERE rowid IN (SELECT e.rowid FROM share_exclusions AS e WHERE e.snapshot_id = ? LIMIT ?);
