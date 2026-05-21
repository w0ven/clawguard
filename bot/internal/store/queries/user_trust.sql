-- name: GetUserTrust :one
SELECT chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status_changed_at, status, score, messages_checked, messages_clean, graduated_at, banned_at, banned_reason, notes, is_bot
FROM user_trust
WHERE chat_id = $1 AND user_id = $2;

-- name: ReactivateArchivedUserTrust :one
UPDATE user_trust
SET status = 'new',
    notes = COALESCE(notes, '') || ' [reactivated ' || NOW()::text || ']',
    status_changed_at = NOW(),
    updated_at = NOW()
WHERE chat_id = $1
  AND user_id = $2
  AND status = 'archived'
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status_changed_at, status, score, messages_checked, messages_clean, graduated_at, banned_at, banned_reason, notes, is_bot;

-- name: UpsertUserTrust :one
INSERT INTO user_trust (
    chat_id,
    user_id,
    username,
    first_name,
    last_name,
    joined_at,
    updated_at,
    status_changed_at,
    status,
    score,
    messages_checked,
    messages_clean,
    graduated_at,
    banned_at,
    banned_reason,
    notes,
    is_bot
) VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW(), $7, $8, $9, $10, $11, $12, $13, $14, $15)
ON CONFLICT (chat_id, user_id) DO UPDATE
SET joined_at = LEAST(user_trust.joined_at, EXCLUDED.joined_at),
    username = COALESCE(EXCLUDED.username, user_trust.username),
    first_name = COALESCE(EXCLUDED.first_name, user_trust.first_name),
    last_name = COALESCE(EXCLUDED.last_name, user_trust.last_name),
    updated_at = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.updated_at ELSE NOW() END,
    status_changed_at = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.status_changed_at WHEN user_trust.status IS DISTINCT FROM EXCLUDED.status THEN NOW() ELSE user_trust.status_changed_at END,
    -- banned/archived 是终态，不允许被入群/老成员回迁等路径覆盖
    status = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.status ELSE EXCLUDED.status END,
    score = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.score ELSE EXCLUDED.score END,
    messages_checked = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.messages_checked ELSE EXCLUDED.messages_checked END,
    messages_clean = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.messages_clean ELSE EXCLUDED.messages_clean END,
    graduated_at = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.graduated_at ELSE EXCLUDED.graduated_at END,
    banned_at = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.banned_at ELSE EXCLUDED.banned_at END,
    banned_reason = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.banned_reason ELSE EXCLUDED.banned_reason END,
    notes = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.notes ELSE EXCLUDED.notes END,
    is_bot = EXCLUDED.is_bot
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status_changed_at, status, score, messages_checked, messages_clean, graduated_at, banned_at, banned_reason, notes, is_bot;

-- name: IncrementUserTrustCounters :one
UPDATE user_trust
SET messages_checked = messages_checked + $3,
    messages_clean = messages_clean + $4,
    score = $5,
    updated_at = NOW()
WHERE chat_id = $1 AND user_id = $2
  AND status NOT IN ('banned', 'archived')
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status_changed_at, status, score, messages_checked, messages_clean, graduated_at, banned_at, banned_reason, notes, is_bot;

-- name: UpdateUserTrustStatus :one
UPDATE user_trust
SET status = $3,
    score = $4,
    status_changed_at = CASE WHEN status IS DISTINCT FROM $3 THEN NOW() ELSE status_changed_at END,
    graduated_at = $5,
    banned_at = CASE WHEN $6::TIMESTAMPTZ IS NULL THEN banned_at ELSE $6 END,
    banned_reason = CASE WHEN $7::JSONB IS NULL THEN banned_reason ELSE $7 END,
    notes = $8,
    updated_at = NOW()
WHERE chat_id = $1 AND user_id = $2
  AND status NOT IN ('banned', 'archived')
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status_changed_at, status, score, messages_checked, messages_clean, graduated_at, banned_at, banned_reason, notes, is_bot;

-- name: UpdateUserTrustIsBot :one
UPDATE user_trust
SET is_bot = $3,
    updated_at = NOW()
WHERE chat_id = $1 AND user_id = $2
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status_changed_at, status, score, messages_checked, messages_clean, graduated_at, banned_at, banned_reason, notes, is_bot;

-- name: ResetUserTrustClean :one
UPDATE user_trust
SET messages_clean = 0,
    score = $3,
    updated_at = NOW()
WHERE chat_id = $1 AND user_id = $2
  AND status NOT IN ('banned', 'archived')
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status_changed_at, status, score, messages_checked, messages_clean, graduated_at, banned_at, banned_reason, notes, is_bot;

-- name: AdjustUserTrustScore :one
INSERT INTO user_trust (
    chat_id,
    user_id,
    username,
    first_name,
    last_name,
    joined_at,
    updated_at,
    status_changed_at,
    status,
    score,
    messages_checked,
    messages_clean,
    graduated_at,
    notes
) VALUES ($1, $2, NULL, NULL, NULL, NOW(), NOW(), NOW(), 'new', LEAST(1, GREATEST(0, 0.5 + $3)), 0, 0, NULL, NULL)
ON CONFLICT (chat_id, user_id) DO UPDATE
SET score = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.score ELSE LEAST(1, GREATEST(0, user_trust.score + $3)) END,
    updated_at = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.updated_at ELSE NOW() END
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status_changed_at, status, score, messages_checked, messages_clean, graduated_at, banned_at, banned_reason, notes, is_bot;


-- name: UnbanUserTrust :one
UPDATE user_trust
SET status = 'new',
    score = $3,
    status_changed_at = NOW(),
    graduated_at = NULL,
    banned_at = NULL,
    banned_reason = NULL,
    notes = $4,
    updated_at = NOW()
WHERE chat_id = $1
  AND user_id = $2
  AND status = 'banned'
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status_changed_at, status, score, messages_checked, messages_clean, graduated_at, banned_at, banned_reason, notes, is_bot;

-- name: ClearUserTrustBanMeta :exec
UPDATE user_trust
SET banned_at = NULL, banned_reason = NULL
WHERE chat_id = $1 AND user_id = $2;

-- name: CountUserTrust :one
SELECT COUNT(*)
FROM user_trust
WHERE ($1::BIGINT IS NULL OR chat_id = $1)
  AND ($2::BIGINT IS NULL OR user_id = $2)
  AND (
    ($3::TEXT = '' AND status != 'archived')
    OR ($3::TEXT != '' AND status = $3)
  )
  AND (
    $4::TEXT = ''
    OR COALESCE(username, '') ILIKE '%' || $4 || '%'
    OR COALESCE(first_name, '') ILIKE '%' || $4 || '%'
    OR COALESCE(last_name, '') ILIKE '%' || $4 || '%'
  )
  AND ($5::TIMESTAMPTZ IS NULL OR joined_at >= $5::TIMESTAMPTZ)
  AND ($6::TIMESTAMPTZ IS NULL OR joined_at <= $6::TIMESTAMPTZ)
  AND ($7::BOOLEAN OR chat_id = ANY($8::BIGINT[]));

-- name: ListUserTrustPaginated :many
SELECT chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status_changed_at, status, score, messages_checked, messages_clean, graduated_at, banned_at, banned_reason, notes, is_bot
FROM user_trust
WHERE ($1::BIGINT IS NULL OR chat_id = $1)
  AND ($2::BIGINT IS NULL OR user_id = $2)
  AND (
    ($3::TEXT = '' AND status != 'archived')
    OR ($3::TEXT != '' AND status = $3)
  )
  AND (
    $4::TEXT = ''
    OR COALESCE(username, '') ILIKE '%' || $4 || '%'
    OR COALESCE(first_name, '') ILIKE '%' || $4 || '%'
    OR COALESCE(last_name, '') ILIKE '%' || $4 || '%'
  )
  AND ($5::TIMESTAMPTZ IS NULL OR joined_at >= $5::TIMESTAMPTZ)
  AND ($6::TIMESTAMPTZ IS NULL OR joined_at <= $6::TIMESTAMPTZ)
  AND ($9::BOOLEAN OR chat_id = ANY($10::BIGINT[]))
ORDER BY joined_at DESC, user_id DESC
LIMIT $7
OFFSET $8;
