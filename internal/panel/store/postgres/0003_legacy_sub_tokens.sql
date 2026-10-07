-- +goose Up
-- Subscription links of the panel users were imported from (Marzban, PasarGuard,
-- Remnawave): an old token that is the same every time (Remnawave's short UUID), or the
-- name or id a signed one carries, leads to the user, so the links people have keep
-- working once the old domain points here (settings.KeyLegacySubPath).
CREATE TABLE legacy_sub_tokens (
  token   TEXT PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  source  TEXT NOT NULL,
  -- A signed token made before this (unix seconds) is not taken: the old panel refuses
  -- the tokens of a user made again under the same name.
  not_before BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX legacy_sub_tokens_user ON legacy_sub_tokens (user_id);

-- +goose Down
DROP TABLE legacy_sub_tokens;
