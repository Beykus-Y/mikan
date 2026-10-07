-- +goose Up
-- A traffic pool a tariff leaves out: its inbounds are not in the subscription and the
-- nodes turn the user away from them, so a cheap tariff does not reach the expensive
-- server. The user's row is set from the tariff, like the limits, and the admin may
-- change it per user.
ALTER TABLE tariff_pools ADD COLUMN excluded BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE user_pools ADD COLUMN excluded BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE user_pools DROP COLUMN excluded;
ALTER TABLE tariff_pools DROP COLUMN excluded;
