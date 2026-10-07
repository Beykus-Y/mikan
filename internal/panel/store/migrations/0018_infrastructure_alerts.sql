-- +goose Up
ALTER TABLE nodes ADD COLUMN public_name TEXT NOT NULL DEFAULT '';

CREATE TABLE infrastructure_alert_state (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE infrastructure_alert_state;
ALTER TABLE nodes DROP COLUMN public_name;
