-- +goose Up
-- What an applied tariff payment changed, as JSON, so a refund can take back exactly that:
-- the user it created, or the user's fields before the purchase and after it. '' on
-- payments applied before this, and on packages (a grant is found by its payment_id).
ALTER TABLE payments ADD COLUMN revert TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE payments DROP COLUMN revert;
