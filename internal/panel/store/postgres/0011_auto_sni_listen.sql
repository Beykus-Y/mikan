-- +goose Up
-- An inbound behind a proxy (its own listen address) no longer switches its REALITY site
-- by itself: the proxy may route by the site's name and would lose the clients.
UPDATE inbounds SET auto_sni = 0 WHERE listen <> '';

-- +goose Down
-- Which inbounds had it on is not kept: a rollback leaves them off.
SELECT 1;
