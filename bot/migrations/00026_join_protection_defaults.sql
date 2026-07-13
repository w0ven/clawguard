-- +goose Up
ALTER TABLE groups
    ALTER COLUMN config SET DEFAULT '{
        "join_protection": {
            "enabled": true,
            "join_threshold": 20,
            "join_window_seconds": 60,
            "protection_duration_seconds": 900,
            "temporary_ban_seconds": 3600,
            "admin_notify_interval_seconds": 300,
            "max_pending_verifications": 30,
            "telegram_failure_cooldown_seconds": 300
        }
    }'::jsonb;

UPDATE groups
SET config = jsonb_set(
    CASE WHEN jsonb_typeof(config) = 'object' THEN config ELSE '{}'::jsonb END,
    '{join_protection}',
    '{
        "enabled": true,
        "join_threshold": 20,
        "join_window_seconds": 60,
        "protection_duration_seconds": 900,
        "temporary_ban_seconds": 3600,
        "admin_notify_interval_seconds": 300,
        "max_pending_verifications": 30,
        "telegram_failure_cooldown_seconds": 300
    }'::jsonb,
    true
)
WHERE jsonb_typeof(config) IS DISTINCT FROM 'object'
   OR NOT (config ? 'join_protection');

-- +goose Down
ALTER TABLE groups ALTER COLUMN config SET DEFAULT '{}'::jsonb;

UPDATE groups
SET config = config - 'join_protection'
WHERE jsonb_typeof(config) = 'object'
  AND config ? 'join_protection';
