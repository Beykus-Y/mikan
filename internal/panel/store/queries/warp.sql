-- name: GetNodeWarp :one
SELECT * FROM node_warp WHERE node_id = $1;

-- name: SaveNodeWarp :exec
INSERT INTO node_warp (node_id, enabled, source, private_key, peer_public_key, endpoint, ipv4, ipv6, reserved, mtu,
  account_id, account_token, plus, routes, created_at, updated_at)
VALUES ($1, 1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
ON CONFLICT (node_id) DO UPDATE SET enabled = 1, source = excluded.source, private_key = excluded.private_key,
  peer_public_key = excluded.peer_public_key, endpoint = excluded.endpoint, ipv4 = excluded.ipv4, ipv6 = excluded.ipv6,
  reserved = excluded.reserved, mtu = excluded.mtu, account_id = excluded.account_id, account_token = excluded.account_token,
  plus = excluded.plus, updated_at = excluded.updated_at;

-- name: SetNodeWarpOptions :exec
UPDATE node_warp SET enabled = $1, routes = $2, updated_at = $3 WHERE node_id = $4;

-- name: SetNodeWarpPlus :exec
UPDATE node_warp SET plus = $1, updated_at = $2 WHERE node_id = $3;

-- name: DeleteNodeWarp :execrows
DELETE FROM node_warp WHERE node_id = $1;

-- name: SetInboundOutbound :exec
UPDATE inbounds SET outbound = $1 WHERE id = $2;
