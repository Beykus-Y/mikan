-- name: GetInfrastructureAlertState :one
SELECT value FROM infrastructure_alert_state WHERE key = $1;

-- name: SetInfrastructureAlertState :exec
INSERT INTO infrastructure_alert_state (key, value, updated_at) VALUES ($1, $2, $3)
ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at;

-- name: MaxInboundEventID :one
SELECT CAST(COALESCE(MAX(id), 0) AS BIGINT) FROM inbound_events;
