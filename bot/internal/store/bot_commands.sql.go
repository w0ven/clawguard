package store

import "context"

const getTodayChatStats = `-- name: GetTodayChatStats :one
WITH day_start AS (
    SELECT date_trunc('day', NOW()) AS ts
)
SELECT
    COALESCE((
        SELECT COUNT(*)::BIGINT
        FROM user_trust ut, day_start
        WHERE ut.chat_id = $1
          AND ut.joined_at >= day_start.ts
    ), 0) AS joined_count,
    COALESCE((
        SELECT GREATEST(
            COUNT(*)::BIGINT
            - (
                SELECT COUNT(*)::BIGINT
                FROM violations v, day_start
                WHERE v.chat_id = $1
                  AND v.created_at >= day_start.ts
                  AND v.rule IN ('verify_timeout', 'verify_random_wrong')
            )
            - (
                SELECT COUNT(*)::BIGINT
                FROM pending_verifications pv, day_start
                WHERE pv.chat_id = $1
                  AND pv.created_at >= day_start.ts
            ),
            0
        )
        FROM user_trust ut, day_start
        WHERE ut.chat_id = $1
          AND ut.joined_at >= day_start.ts
    ), 0) AS verification_passed_count,
    COALESCE((
        SELECT COUNT(*)::BIGINT
        FROM violations v, day_start
        WHERE v.chat_id = $1
          AND v.created_at >= day_start.ts
          AND v.rule IN ('verify_timeout', 'verify_random_wrong')
    ), 0) AS verification_failed_count,
    COALESCE((
        SELECT COUNT(*)::BIGINT
        FROM ai_decisions ad, day_start
        WHERE ad.chat_id = $1
          AND ad.created_at >= day_start.ts
    ), 0) AS ai_calls,
    COALESCE((
        SELECT COUNT(*)::BIGINT
        FROM ai_decisions ad, day_start
        WHERE ad.chat_id = $1
          AND ad.created_at >= day_start.ts
          AND lower(ad.verdict) = 'ad'
    ), 0) AS ai_ad_count,
    COALESCE((
        SELECT COUNT(*)::BIGINT
        FROM ai_decisions ad, day_start
        WHERE ad.chat_id = $1
          AND ad.created_at >= day_start.ts
          AND lower(ad.verdict) = 'scam'
    ), 0) AS ai_scam_count,
    COALESCE((
        SELECT COUNT(*)::BIGINT
        FROM ai_decisions ad, day_start
        WHERE ad.chat_id = $1
          AND ad.created_at >= day_start.ts
          AND lower(ad.verdict) = 'clean'
    ), 0) AS ai_clean_count,
    COALESCE((
        SELECT COUNT(*)::BIGINT
        FROM violations v, day_start
        WHERE v.chat_id = $1
          AND v.created_at >= day_start.ts
          AND v.action IN ('ban', 'delete_ban', 'kick')
    ), 0) AS ban_kick_count,
    COALESCE((
        SELECT COUNT(*)::BIGINT
        FROM violations v, day_start
        WHERE v.chat_id = $1
          AND v.created_at >= day_start.ts
          AND v.action LIKE '%warn%'
    ), 0) AS warn_count,
    COALESCE((
        SELECT SUM(ad.cost_cents)::DOUBLE PRECISION
        FROM ai_decisions ad, day_start
        WHERE ad.chat_id = $1
          AND ad.created_at >= day_start.ts
    ), 0) AS ai_cost_cents
`

const countRecentViolationsByUser = `-- name: CountRecentViolationsByUser :one
SELECT COUNT(*)::BIGINT
FROM violations
WHERE chat_id = $1
  AND user_id = $2
  AND created_at >= NOW() - make_interval(days => $3::int)
`

const resolveChatUserByUsername = `-- name: ResolveChatUserByUsername :one
SELECT user_id, username
FROM (
    SELECT user_id, username, created_at
    FROM violations
    WHERE chat_id = $1
      AND lower(COALESCE(username, '')) = lower($2)

    UNION ALL

    SELECT user_id, username, created_at
    FROM pending_verifications
    WHERE chat_id = $1
      AND lower(COALESCE(username, '')) = lower($2)
) candidates
ORDER BY created_at DESC
LIMIT 1
`

func (q *Queries) GetTodayChatStats(ctx context.Context, chatID int64) (ChatTodayStats, error) {
	row := q.db.QueryRow(ctx, getTodayChatStats, chatID)
	var stats ChatTodayStats
	err := row.Scan(
		&stats.JoinedCount,
		&stats.VerificationPassedCount,
		&stats.VerificationFailedCount,
		&stats.AICalls,
		&stats.AIAdCount,
		&stats.AIScamCount,
		&stats.AICleanCount,
		&stats.BanKickCount,
		&stats.WarnCount,
		&stats.AICostCents,
	)
	return stats, err
}

func (q *Queries) CountRecentViolationsByUser(ctx context.Context, chatID, userID int64, days int32) (int64, error) {
	row := q.db.QueryRow(ctx, countRecentViolationsByUser, chatID, userID, days)
	var count int64
	err := row.Scan(&count)
	return count, err
}

func (q *Queries) ResolveChatUserByUsername(ctx context.Context, chatID int64, username string) (ResolvedChatUser, error) {
	row := q.db.QueryRow(ctx, resolveChatUserByUsername, chatID, username)
	var user ResolvedChatUser
	err := row.Scan(&user.UserID, &user.Username)
	return user, err
}
