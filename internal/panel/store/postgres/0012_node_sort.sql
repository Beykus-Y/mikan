-- +goose Up
-- The order of the servers in the subscription, set by the admin. Every node starts
-- where its id puts it, so a panel that never reorders sees no change.
ALTER TABLE nodes ADD COLUMN sort BIGINT NOT NULL DEFAULT 0;
UPDATE nodes SET sort = ranked.pos FROM (SELECT id, ROW_NUMBER() OVER (ORDER BY id) AS pos FROM nodes) AS ranked WHERE nodes.id = ranked.id;

-- +goose Down
ALTER TABLE nodes DROP COLUMN sort;
