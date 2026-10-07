-- name: AddTorrentHit :one
INSERT INTO torrent_hits (user_id, node_id, ip, inbound, network, kind, dest, hits, at, banned_until)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id;

-- name: ActiveTorrentBans :many
SELECT user_id, max(banned_until)::BIGINT AS banned_until FROM torrent_hits
WHERE banned_until > $1 AND lifted_at IS NULL
GROUP BY user_id;

-- name: LiftTorrentBans :execrows
UPDATE torrent_hits SET lifted_at = sqlc.arg(now)::BIGINT
WHERE user_id = sqlc.arg(user_id) AND banned_until > sqlc.arg(now)::BIGINT AND lifted_at IS NULL;

-- name: ListTorrentHits :many
SELECT h.id, h.user_id, u.name AS user_name, h.node_id, COALESCE(n.name, '') AS node_name, h.ip, h.inbound,
  h.network, h.kind, h.dest, h.hits, h.at, h.banned_until, h.lifted_at
FROM torrent_hits h
JOIN users u ON u.id = h.user_id
LEFT JOIN nodes n ON n.id = h.node_id
WHERE (sqlc.narg('user_id')::BIGINT IS NULL OR h.user_id = sqlc.narg('user_id'))
  AND (sqlc.narg('before')::BIGINT IS NULL OR h.id < sqlc.narg('before'))
ORDER BY h.id DESC
LIMIT $1;

-- name: TorrentHitsAfter :many
SELECT h.id, h.user_id, u.name AS user_name, COALESCE(n.name, '') AS node_name, h.ip, h.kind, h.dest, h.hits,
  h.at, h.banned_until
FROM torrent_hits h
JOIN users u ON u.id = h.user_id
LEFT JOIN nodes n ON n.id = h.node_id
WHERE h.id > $1
ORDER BY h.id
LIMIT $2;

-- name: LastTorrentHitID :one
SELECT COALESCE(max(id), 0)::BIGINT FROM torrent_hits;

-- name: PruneTorrentHits :exec
DELETE FROM torrent_hits WHERE at < $1 AND banned_until < $1;

-- name: TorrentTrackerCatches :one
-- How many HTTP tracker lines (TCP) a user was caught with since a time; a hit counts as
-- many as it stands for. Lines before the admin last lifted the user's ban do not count.
SELECT COALESCE(sum(hits), 0)::BIGINT AS catches FROM torrent_hits h
WHERE h.user_id = $1 AND h.network = 'tcp' AND h.kind = 'tracker' AND h.at > $2
  AND h.at > COALESCE((SELECT max(l.lifted_at) FROM torrent_hits l WHERE l.user_id = $1), 0);
