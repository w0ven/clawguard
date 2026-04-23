-- +goose Up
ALTER TABLE user_trust
  ADD COLUMN IF NOT EXISTS banned_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS banned_reason JSONB;

UPDATE user_trust ut
SET
    banned_at = v.created_at,
    banned_reason = jsonb_build_object(
        'rule', v.rule,
        'matched', v.matched,
        'source', 'historical_backfill'
    )
FROM (
    SELECT DISTINCT ON (chat_id, user_id)
        chat_id, user_id, created_at, rule, matched
    FROM violations
    WHERE action = 'ban'
    ORDER BY chat_id, user_id, created_at DESC
) v
WHERE ut.status = 'banned'
  AND ut.chat_id = v.chat_id
  AND ut.user_id = v.user_id
  AND ut.banned_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_user_trust_banned_at
  ON user_trust (chat_id, banned_at DESC)
  WHERE status = 'banned';

-- +goose Down
ALTER TABLE user_trust DROP COLUMN IF EXISTS banned_at;
ALTER TABLE user_trust DROP COLUMN IF EXISTS banned_reason;
