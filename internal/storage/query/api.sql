-- name: InsertAPIToken :exec
INSERT INTO api_tokens (id, name, token_hash, user_agent, source_ip, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetAPITokenByHash :one
SELECT id, name, token_hash, user_agent, source_ip, created_at, last_used_at, expires_at
FROM api_tokens WHERE token_hash = ?;

-- name: ListAPITokens :many
SELECT id, name, token_hash, user_agent, source_ip, created_at, last_used_at, expires_at
FROM api_tokens ORDER BY created_at, id;

-- name: TouchAPIToken :exec
UPDATE api_tokens SET last_used_at = ? WHERE id = ?;

-- name: DeleteAPIToken :exec
DELETE FROM api_tokens WHERE id = ?;
