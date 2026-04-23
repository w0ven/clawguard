-- name: GetUserTrust :one
SELECT chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, notes
FROM user_trust
WHERE chat_id = $1 AND user_id = $2;

-- name: ReactivateArchivedUserTrust :one
UPDATE user_trust
SET status = 'new',
    notes = COALESCE(notes, '') || ' [reactivated ' || NOW()::text || ']',
    updated_at = NOW()
WHERE chat_id = $1
  AND user_id = $2
  AND status = 'archived'
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, notes;

-- name: UpsertUserTrust :one
INSERT INTO user_trust (
    chat_id,
    user_id,
    username,
    first_name,
    last_name,
    joined_at,
    updated_at,
    status,
    score,
    messages_checked,
    messages_clean,
    graduated_at,
    notes
) VALUES ($1, $2, $3, $4, $5, $6, NOW(), $7, $8, $9, $10, $11, $12)
ON CONFLICT (chat_id, user_id) DO UPDATE
SET joined_at = LEAST(user_trust.joined_at, EXCLUDED.joined_at),
    username = COALESCE(EXCLUDED.username, user_trust.username),
    first_name = COALESCE(EXCLUDED.first_name, user_trust.first_name),
    last_name = COALESCE(EXCLUDED.last_name, user_trust.last_name),
    updated_at = NOW(),
    -- banned 是终态，不允许被入群/老成员回迁等路径覆盖
    status = CASE WHEN user_trust.status = 'banned' THEN user_trust.status ELSE EXCLUDED.status END,
    score = CASE WHEN user_trust.status = 'banned' THEN user_trust.score ELSE EXCLUDED.score END,
    messages_checked = CASE WHEN user_trust.status = 'banned' THEN user_trust.messages_checked ELSE EXCLUDED.messages_checked END,
    messages_clean = CASE WHEN user_trust.status = 'banned' THEN user_trust.messages_clean ELSE EXCLUDED.messages_clean END,
    graduated_at = CASE WHEN user_trust.status = 'banned' THEN user_trust.graduated_at ELSE EXCLUDED.graduated_at END,
    notes = CASE WHEN user_trust.status = 'banned' THEN user_trust.notes ELSE EXCLUDED.notes END
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, notes;

-- name: IncrementUserTrustCounters :one
UPDATE user_trust
SET messages_checked = messages_checked + $3,
    messages_clean = messages_clean + $4,
    score = $5,
    updated_at = NOW()
WHERE chat_id = $1 AND user_id = $2
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, notes;

-- name: UpdateUserTrustStatus :one
UPDATE user_trust
SET status = $3,
    score = $4,
    graduated_at = $5,
    notes = $6,
    updated_at = NOW()
WHERE chat_id = $1 AND user_id = $2
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, notes;

-- name: ResetUserTrustClean :one
UPDATE user_trust
SET messages_clean = 0,
    score = $3,
    updated_at = NOW()
WHERE chat_id = $1 AND user_id = $2
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, notes;

-- name: AdjustUserTrustScore :one
INSERT INTO user_trust (
    chat_id,
    user_id,
    username,
    first_name,
    last_name,
    joined_at,
    updated_at,
    status,
    score,
    messages_checked,
    messages_clean,
    graduated_at,
    notes
) VALUES ($1, $2, NULL, NULL, NULL, NOW(), NOW(), 'new', LEAST(1, GREATEST(0, 0.5 + $3)), 0, 0, NULL, NULL)
ON CONFLICT (chat_id, user_id) DO UPDATE
SET score = LEAST(1, GREATEST(0, user_trust.score + $3)),
    updated_at = NOW()
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, notes;

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
  AND ($6::TIMESTAMPTZ IS NULL OR joined_at <= $6::TIMESTAMPTZ);

-- name: ListUserTrustPaginated :many
SELECT chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, notes
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
ORDER BY joined_at DESC, user_id DESC
LIMIT $7
OFFSET $8;
