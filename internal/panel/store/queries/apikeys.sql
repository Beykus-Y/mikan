-- name: CreateAPIKey :one
INSERT INTO api_keys (admin_id, name, prefix, hash, scope, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetAPIKeyByHash :one
SELECT * FROM api_keys WHERE hash = $1;

-- name: ListAPIKeys :many
SELECT * FROM api_keys ORDER BY id;

-- name: CountAPIKeys :one
SELECT count(*) FROM api_keys;

-- name: DeleteAPIKey :execrows
DELETE FROM api_keys WHERE id = $1;

-- name: DeleteAPIKeysOf :execrows
DELETE FROM api_keys WHERE admin_id = $1;

-- name: TouchAPIKey :exec
UPDATE api_keys SET last_used_at = $1, last_ip = $2
WHERE id = $3 AND (last_used_at IS NULL OR last_used_at < $4);
