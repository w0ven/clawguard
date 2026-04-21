package store

import "context"

const listViolations = `-- name: ListViolations :many
SELECT id, chat_id, user_id, username, rule, matched, action, message_text, created_at
FROM violations
WHERE ($1::BIGINT IS NULL OR chat_id = $1)
  AND ($2::BIGINT IS NULL OR user_id = $2)
  AND ($3::TEXT = '' OR rule = $3)
  AND ($4::TEXT = '' OR action = $4)
  AND ($5::TIMESTAMPTZ IS NULL OR created_at >= $5)
  AND ($6::TIMESTAMPTZ IS NULL OR created_at <= $6)
ORDER BY created_at DESC
LIMIT $7
`

const listWarnings = `-- name: ListWarnings :many
SELECT id, chat_id, user_id, reason, issued_by, created_at, consumed_at
FROM warnings
WHERE ($1::BIGINT IS NULL OR chat_id = $1)
  AND ($2::BIGINT IS NULL OR user_id = $2)
ORDER BY created_at DESC
LIMIT $3
`

const upsertBannedUser = `-- name: UpsertBannedUser :one
INSERT INTO banned_users (
    user_id,
    reason,
    source,
    banned_by
) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id) DO UPDATE
SET reason = EXCLUDED.reason,
    source = EXCLUDED.source,
    banned_by = EXCLUDED.banned_by,
    banned_at = NOW()
RETURNING user_id, reason, source, banned_at, banned_by
`

const deleteBannedUser = `-- name: DeleteBannedUser :exec
DELETE FROM banned_users
WHERE user_id = $1
`

const getAdminStats = `-- name: GetAdminStats :one
SELECT
    (SELECT COUNT(*)::BIGINT FROM groups) AS groups_count,
    (SELECT COUNT(*)::BIGINT FROM pending_verifications WHERE expires_at > NOW()) AS active_verifications,
    (SELECT COUNT(*)::BIGINT FROM violations WHERE created_at >= date_trunc('day', NOW())) AS today_violations
`

const getTodayAICostCents = `-- name: GetTodayAICostCents :one
SELECT COALESCE(SUM(cost_cents), 0)::DOUBLE PRECISION
FROM ai_decisions
WHERE created_at >= CURRENT_DATE
`

const listRecentAIDecisionVerdicts = `-- name: ListRecentAIDecisionVerdicts :many
SELECT verdict
FROM ai_decisions
ORDER BY created_at DESC
LIMIT $1
`

const getFirstOwnerAdmin = `-- name: GetFirstOwnerAdmin :one
SELECT id, telegram_id, username, first_name, photo_url, role, notes, group_scope, created_at, last_login_at
FROM admins
WHERE LOWER(role) = 'owner'
ORDER BY created_at ASC, id ASC
LIMIT 1
`

type ListViolationsParams struct {
	ChatID *int64
	UserID *int64
	Rule   string
	Action string
	Since  *string
	Until  *string
	Limit  int32
}

type ListWarningsParams struct {
	ChatID *int64
	UserID *int64
	Limit  int32
}

type UpsertBannedUserParams struct {
	UserID   int64
	Reason   *string
	Source   string
	BannedBy *int64
}

type AdminStats struct {
	GroupsCount         int64
	ActiveVerifications int64
	TodayViolations     int64
}

func (q *Queries) ListViolations(ctx context.Context, arg ListViolationsParams) ([]Violation, error) {
	rows, err := q.db.Query(ctx, listViolations, arg.ChatID, arg.UserID, arg.Rule, arg.Action, arg.Since, arg.Until, arg.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Violation
	for rows.Next() {
		var i Violation
		if err := rows.Scan(
			&i.ID,
			&i.ChatID,
			&i.UserID,
			&i.Username,
			&i.Rule,
			&i.Matched,
			&i.Action,
			&i.MessageText,
			&i.CreatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (q *Queries) ListWarnings(ctx context.Context, arg ListWarningsParams) ([]Warning, error) {
	rows, err := q.db.Query(ctx, listWarnings, arg.ChatID, arg.UserID, arg.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Warning
	for rows.Next() {
		var i Warning
		if err := rows.Scan(
			&i.ID,
			&i.ChatID,
			&i.UserID,
			&i.Reason,
			&i.IssuedBy,
			&i.CreatedAt,
			&i.ConsumedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (q *Queries) UpsertBannedUser(ctx context.Context, arg UpsertBannedUserParams) (BannedUser, error) {
	row := q.db.QueryRow(ctx, upsertBannedUser, arg.UserID, arg.Reason, arg.Source, arg.BannedBy)
	var i BannedUser
	err := row.Scan(&i.UserID, &i.Reason, &i.Source, &i.BannedAt, &i.BannedBy)
	return i, err
}

func (q *Queries) DeleteBannedUser(ctx context.Context, userID int64) error {
	_, err := q.db.Exec(ctx, deleteBannedUser, userID)
	return err
}

func (q *Queries) GetAdminStats(ctx context.Context) (AdminStats, error) {
	row := q.db.QueryRow(ctx, getAdminStats)
	var i AdminStats
	err := row.Scan(&i.GroupsCount, &i.ActiveVerifications, &i.TodayViolations)
	return i, err
}

func (q *Queries) GetTodayAICostCents(ctx context.Context) (float64, error) {
	row := q.db.QueryRow(ctx, getTodayAICostCents)
	var value float64
	err := row.Scan(&value)
	return value, err
}

func (q *Queries) ListRecentAIDecisionVerdicts(ctx context.Context, limit int32) ([]string, error) {
	rows, err := q.db.Query(ctx, listRecentAIDecisionVerdicts, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]string, 0)
	for rows.Next() {
		var verdict string
		if err := rows.Scan(&verdict); err != nil {
			return nil, err
		}
		items = append(items, verdict)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (q *Queries) GetFirstOwnerAdmin(ctx context.Context) (Admin, error) {
	row := q.db.QueryRow(ctx, getFirstOwnerAdmin)
	var i Admin
	err := scanAdmin(row, &i)
	return i, err
}
