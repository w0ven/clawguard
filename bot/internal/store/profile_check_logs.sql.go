package store

import "context"

const insertProfileCheckLog = `-- name: InsertProfileCheckLog :one
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
RETURNING id, chat_id, user_id, user_name, username, bio, check_mode, result, matched_rule, ai_confidence, ai_verdict, created_at
`

const listProfileCheckLogs = `-- name: ListProfileCheckLogs :many
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
LIMIT $5 OFFSET $6
`

const countProfileCheckLogs = `-- name: CountProfileCheckLogs :one
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
  AND ($4::TEXT = '' OR check_mode = $4)
`

const deleteOldProfileCheckLogs = `-- name: DeleteOldProfileCheckLogs :execrows
DELETE FROM profile_check_logs
WHERE created_at < NOW() - make_interval(days => $1::int)
`

type InsertProfileCheckLogParams struct {
	ChatID       int64
	UserID       int64
	UserName     *string
	Username     *string
	Bio          *string
	CheckMode    string
	Result       string
	MatchedRule  *string
	AiConfidence *float32
	AiVerdict    *string
}

type ListProfileCheckLogsParams struct {
	ChatID    int64
	Search    string
	Result    string
	CheckMode string
	Limit     int32
	Offset    int32
}

type CountProfileCheckLogsParams struct {
	ChatID    int64
	Search    string
	Result    string
	CheckMode string
}

func (q *Queries) InsertProfileCheckLog(ctx context.Context, arg InsertProfileCheckLogParams) (ProfileCheckLog, error) {
	row := q.db.QueryRow(ctx, insertProfileCheckLog, arg.ChatID, arg.UserID, arg.UserName, arg.Username, arg.Bio, arg.CheckMode, arg.Result, arg.MatchedRule, arg.AiConfidence, arg.AiVerdict)
	var i ProfileCheckLog
	err := row.Scan(&i.ID, &i.ChatID, &i.UserID, &i.UserName, &i.Username, &i.Bio, &i.CheckMode, &i.Result, &i.MatchedRule, &i.AiConfidence, &i.AiVerdict, &i.CreatedAt)
	return i, err
}

func (q *Queries) ListProfileCheckLogs(ctx context.Context, arg ListProfileCheckLogsParams) ([]ProfileCheckLog, error) {
	rows, err := q.db.Query(ctx, listProfileCheckLogs, arg.ChatID, arg.Search, arg.Result, arg.CheckMode, arg.Limit, arg.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []ProfileCheckLog
	for rows.Next() {
		var i ProfileCheckLog
		if err := rows.Scan(&i.ID, &i.ChatID, &i.UserID, &i.UserName, &i.Username, &i.Bio, &i.CheckMode, &i.Result, &i.MatchedRule, &i.AiConfidence, &i.AiVerdict, &i.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (q *Queries) CountProfileCheckLogs(ctx context.Context, arg CountProfileCheckLogsParams) (int64, error) {
	row := q.db.QueryRow(ctx, countProfileCheckLogs, arg.ChatID, arg.Search, arg.Result, arg.CheckMode)
	var count int64
	err := row.Scan(&count)
	return count, err
}

func (q *Queries) DeleteOldProfileCheckLogs(ctx context.Context, days int32) (int64, error) {
	result, err := q.db.Exec(ctx, deleteOldProfileCheckLogs, days)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}
