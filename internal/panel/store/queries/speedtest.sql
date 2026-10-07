-- name: AddNodeSpeedTest :one
INSERT INTO node_speedtests (node_id, at, ping_ms, jitter_ms, loss_pct, down_bps, up_bps, error)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: ListNodeSpeedTests :many
SELECT * FROM node_speedtests WHERE node_id = $1 ORDER BY id DESC LIMIT $2;

-- name: PruneNodeSpeedTests :exec
-- Keeps the latest hundred of a node.
DELETE FROM node_speedtests t WHERE t.node_id = sqlc.arg(node_id)::BIGINT AND t.id NOT IN (
  SELECT k.id FROM node_speedtests k WHERE k.node_id = sqlc.arg(node_id)::BIGINT ORDER BY k.id DESC LIMIT 100
);
