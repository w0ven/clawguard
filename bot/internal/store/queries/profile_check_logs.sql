-- name: InsertProfileCheckLog :one
INSERT INTO profile_check_logs (
    chat_id,
    user_id,
    user_name,
    username,
    bio,
    check_mode,
    result,
    matched_rule,
    ai_confidence,
    ai_verdict
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id, chat_id, user_id, user_name, username, bio, check_mode, result, matched_rule, ai_confidence, ai_verdict, created_at;

-- name: ListProfileCheckLogs :many
SELECT id, chat_id, user_id, user_name, username, bio, check_mode, result, matched_rule, ai_confidence, ai_verdict, created_at
FROM profile_check_logs
WHERE chat_id = $1
  AND (
    $2::TEXT = ''
    OR COALESCE(user_name, '') ILIKE '%' || $2 || '%'
    OR COALESCE(username, '') ILIKE '%' || $2 || '%'
    OR COALESCE(bio, '') ILIKE '%' || $2 || '%'
  )
  AND ($3::TEXT = '' OR result = $3)
  AND ($4::TEXT = '' OR check_mode = $4)
ORDER BY created_at DESC
LIMIT $5 OFFSET $6;

-- name: CountProfileCheckLogs :one
SELECT COUNT(*)::BIGINT
FROM profile_check_logs
WHERE chat_id = $1
  AND (
    $2::TEXT = ''
    OR COALESCE(user_name, '') ILIKE '%' || $2 || '%'
    OR COALESCE(username, '') ILIKE '%' || $2 || '%'
    OR COALESCE(bio, '') ILIKE '%' || $2 || '%'
  )
  AND ($3::TEXT = '' OR result = $3)
  AND ($4::TEXT = '' OR check_mode = $4);

-- name: DeleteOldProfileCheckLogs :execrows
DELETE FROM profile_check_logs
WHERE created_at < NOW() - make_interval(days => $1::int);
