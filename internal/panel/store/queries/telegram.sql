-- name: GetTgChat :one
SELECT * FROM tg_chats WHERE tg_id = $1;

-- name: UpsertTgChat :exec
INSERT INTO tg_chats (tg_id, username, first_name, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (tg_id) DO UPDATE SET username = excluded.username, first_name = excluded.first_name, blocked = 0, updated_at = excluded.updated_at;

-- name: SetTgMenu :exec
UPDATE tg_chats SET menu_msg_id = $1 WHERE tg_id = $2;

-- name: SetTgCurrent :exec
UPDATE tg_chats SET current = $1 WHERE tg_id = $2;

-- name: SetTgBlocked :exec
UPDATE tg_chats SET blocked = $1 WHERE tg_id = $2;

-- name: LinkTg :exec
INSERT INTO tg_links (user_id, tg_id, created_at) VALUES ($1, $2, $3)
ON CONFLICT (user_id) DO UPDATE SET tg_id = excluded.tg_id, created_at = excluded.created_at;

-- name: UnlinkTg :exec
DELETE FROM tg_links WHERE user_id = $1;

-- name: GetTgLink :one
SELECT * FROM tg_links WHERE user_id = $1;

-- name: ListTgLinksOf :many
-- The subscriptions a Telegram account owns, oldest link first.
SELECT u.* FROM tg_links l JOIN users u ON u.id = l.user_id WHERE l.tg_id = $1 ORDER BY l.created_at, u.id;

-- name: CountTgLinksOf :one
SELECT COUNT(*) FROM tg_links WHERE tg_id = $1;

-- name: TgLinkOfUser :one
SELECT l.tg_id, COALESCE(c.username, '') AS username, COALESCE(c.first_name, '') AS first_name
FROM tg_links l LEFT JOIN tg_chats c ON c.tg_id = l.tg_id WHERE l.user_id = $1;

-- name: CountTgChats :one
SELECT COUNT(DISTINCT tg_id) FROM tg_links;

-- name: BroadcastTargets :many
-- Accounts with a linked subscription that did not block the bot.
SELECT DISTINCT l.tg_id FROM tg_links l LEFT JOIN tg_chats c ON c.tg_id = l.tg_id WHERE COALESCE(c.blocked, 0) = 0;

-- name: AddTgNotice :execrows
INSERT INTO tg_notices (user_id, kind, period, sent_at) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING;

-- name: PruneTgNotices :exec
DELETE FROM tg_notices WHERE sent_at < $1;

-- name: CountTgLinks :one
SELECT COUNT(*) FROM tg_links;
