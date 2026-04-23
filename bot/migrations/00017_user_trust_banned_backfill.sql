-- +goose Up
-- 修复历史数据：有 ban violation 但 trust status 不是 banned 的用户
UPDATE user_trust ut
SET status = 'banned',
    score = 0,
    updated_at = NOW(),
    notes = COALESCE(
        ut.notes,
        'auto-fixed from violations: ' || (
            SELECT v.rule || ':' || COALESCE(v.matched, '')
            FROM violations v
            WHERE v.chat_id = ut.chat_id
              AND v.user_id = ut.user_id
              AND v.action = 'ban'
            ORDER BY v.created_at DESC
            LIMIT 1
        )
    )
WHERE ut.status != 'banned'
  AND ut.status != 'archived'
  AND EXISTS (
    SELECT 1
    FROM violations v
    WHERE v.chat_id = ut.chat_id
      AND v.user_id = ut.user_id
      AND v.action = 'ban'
  );

-- +goose Down
-- 无法精确回滚（原始 status 可能是多种）
SELECT 1;
