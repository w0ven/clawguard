package store

import (
	"context"
	"time"
)

const getUserTrust = `-- name: GetUserTrust :one
SELECT chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, notes
FROM user_trust
WHERE chat_id = $1 AND user_id = $2
`

const reactivateArchivedUserTrust = `-- name: ReactivateArchivedUserTrust :one
UPDATE user_trust
SET status = 'new',
    notes = COALESCE(notes, '') || ' [reactivated ' || NOW()::text || ']',
    updated_at = NOW()
WHERE chat_id = $1
  AND user_id = $2
  AND status = 'archived'
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, notes
`

const upsertUserTrust = `-- name: UpsertUserTrust :one
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
    status = CASE WHEN user_trust.status = 'banned' THEN user_trust.status ELSE EXCLUDED.status END,
    score = CASE WHEN user_trust.status = 'banned' THEN user_trust.score ELSE EXCLUDED.score END,
    messages_checked = CASE WHEN user_trust.status = 'banned' THEN user_trust.messages_checked ELSE EXCLUDED.messages_checked END,
    messages_clean = CASE WHEN user_trust.status = 'banned' THEN user_trust.messages_clean ELSE EXCLUDED.messages_clean END,
    graduated_at = CASE WHEN user_trust.status = 'banned' THEN user_trust.graduated_at ELSE EXCLUDED.graduated_at END,
    notes = CASE WHEN user_trust.status = 'banned' THEN user_trust.notes ELSE EXCLUDED.notes END
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, notes
`

const incrementUserTrustCounters = `-- name: IncrementUserTrustCounters :one
UPDATE user_trust
SET messages_checked = messages_checked + $3,
    messages_clean = messages_clean + $4,
    score = $5,
    updated_at = NOW()
WHERE chat_id = $1 AND user_id = $2
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, notes
`

const updateUserTrustStatus = `-- name: UpdateUserTrustStatus :one
UPDATE user_trust
SET status = $3,
    score = $4,
    graduated_at = $5,
    notes = $6,
    updated_at = NOW()
WHERE chat_id = $1 AND user_id = $2
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, notes
`

const resetUserTrustClean = `-- name: ResetUserTrustClean :one
UPDATE user_trust
SET messages_clean = 0,
    score = $3,
    updated_at = NOW()
WHERE chat_id = $1 AND user_id = $2
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, notes
`

const adjustUserTrustScore = `-- name: AdjustUserTrustScore :one
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
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, notes
`

const countUserTrust = `-- name: CountUserTrust :one
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
`

const listUserTrustPaginated = `-- name: ListUserTrustPaginated :many
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
OFFSET $8
`

type UpsertUserTrustParams struct {
	ChatID          int64
	UserID          int64
	Username        *string
	FirstName       *string
	LastName        *string
	JoinedAt        time.Time
	Status          string
	Score           float64
	MessagesChecked int32
	MessagesClean   int32
	GraduatedAt     *time.Time
	Notes           *string
}

type IncrementUserTrustCountersParams struct {
	ChatID       int64
	UserID       int64
	CheckedDelta int32
	CleanDelta   int32
	Score        float64
}

type UpdateUserTrustStatusParams struct {
	ChatID      int64
	UserID      int64
	Status      string
	Score       float64
	GraduatedAt *time.Time
	Notes       *string
}

type ResetUserTrustCleanParams struct {
	ChatID int64
	UserID int64
	Score  float64
}

type AdjustUserTrustScoreParams struct {
	ChatID int64
	UserID int64
	Delta  float64
}

type ListUserTrustPaginatedParams struct {
	ChatID      *int64
	UserID      *int64
	Status      string
	Username    string
	JoinedSince *string
	JoinedUntil *string
	Limit       int32
	Offset      int32
}

func (q *Queries) GetUserTrust(ctx context.Context, chatID, userID int64) (UserTrust, error) {
	row := q.db.QueryRow(ctx, getUserTrust, chatID, userID)
	var i UserTrust
	err := row.Scan(
		&i.ChatID,
		&i.UserID,
		&i.Username,
		&i.FirstName,
		&i.LastName,
		&i.JoinedAt,
		&i.UpdatedAt,
		&i.Status,
		&i.Score,
		&i.MessagesChecked,
		&i.MessagesClean,
		&i.GraduatedAt,
		&i.Notes,
	)
	return i, err
}

func (q *Queries) ReactivateArchivedUserTrust(ctx context.Context, chatID, userID int64) (UserTrust, error) {
	row := q.db.QueryRow(ctx, reactivateArchivedUserTrust, chatID, userID)
	var i UserTrust
	err := row.Scan(
		&i.ChatID,
		&i.UserID,
		&i.Username,
		&i.FirstName,
		&i.LastName,
		&i.JoinedAt,
		&i.UpdatedAt,
		&i.Status,
		&i.Score,
		&i.MessagesChecked,
		&i.MessagesClean,
		&i.GraduatedAt,
		&i.Notes,
	)
	return i, err
}

func (q *Queries) UpsertUserTrust(ctx context.Context, arg UpsertUserTrustParams) (UserTrust, error) {
	row := q.db.QueryRow(ctx, upsertUserTrust, arg.ChatID, arg.UserID, arg.Username, arg.FirstName, arg.LastName, arg.JoinedAt, arg.Status, arg.Score, arg.MessagesChecked, arg.MessagesClean, arg.GraduatedAt, arg.Notes)
	var i UserTrust
	err := row.Scan(
		&i.ChatID,
		&i.UserID,
		&i.Username,
		&i.FirstName,
		&i.LastName,
		&i.JoinedAt,
		&i.UpdatedAt,
		&i.Status,
		&i.Score,
		&i.MessagesChecked,
		&i.MessagesClean,
		&i.GraduatedAt,
		&i.Notes,
	)
	return i, err
}

func (q *Queries) IncrementUserTrustCounters(ctx context.Context, arg IncrementUserTrustCountersParams) (UserTrust, error) {
	row := q.db.QueryRow(ctx, incrementUserTrustCounters, arg.ChatID, arg.UserID, arg.CheckedDelta, arg.CleanDelta, arg.Score)
	var i UserTrust
	err := row.Scan(
		&i.ChatID,
		&i.UserID,
		&i.Username,
		&i.FirstName,
		&i.LastName,
		&i.JoinedAt,
		&i.UpdatedAt,
		&i.Status,
		&i.Score,
		&i.MessagesChecked,
		&i.MessagesClean,
		&i.GraduatedAt,
		&i.Notes,
	)
	return i, err
}

func (q *Queries) UpdateUserTrustStatus(ctx context.Context, arg UpdateUserTrustStatusParams) (UserTrust, error) {
	row := q.db.QueryRow(ctx, updateUserTrustStatus, arg.ChatID, arg.UserID, arg.Status, arg.Score, arg.GraduatedAt, arg.Notes)
	var i UserTrust
	err := row.Scan(
		&i.ChatID,
		&i.UserID,
		&i.Username,
		&i.FirstName,
		&i.LastName,
		&i.JoinedAt,
		&i.UpdatedAt,
		&i.Status,
		&i.Score,
		&i.MessagesChecked,
		&i.MessagesClean,
		&i.GraduatedAt,
		&i.Notes,
	)
	return i, err
}

func (q *Queries) ResetUserTrustClean(ctx context.Context, arg ResetUserTrustCleanParams) (UserTrust, error) {
	row := q.db.QueryRow(ctx, resetUserTrustClean, arg.ChatID, arg.UserID, arg.Score)
	var i UserTrust
	err := row.Scan(
		&i.ChatID,
		&i.UserID,
		&i.Username,
		&i.FirstName,
		&i.LastName,
		&i.JoinedAt,
		&i.UpdatedAt,
		&i.Status,
		&i.Score,
		&i.MessagesChecked,
		&i.MessagesClean,
		&i.GraduatedAt,
		&i.Notes,
	)
	return i, err
}

func (q *Queries) AdjustUserTrustScore(ctx context.Context, arg AdjustUserTrustScoreParams) (UserTrust, error) {
	row := q.db.QueryRow(ctx, adjustUserTrustScore, arg.ChatID, arg.UserID, arg.Delta)
	var i UserTrust
	err := row.Scan(
		&i.ChatID,
		&i.UserID,
		&i.Username,
		&i.FirstName,
		&i.LastName,
		&i.JoinedAt,
		&i.UpdatedAt,
		&i.Status,
		&i.Score,
		&i.MessagesChecked,
		&i.MessagesClean,
		&i.GraduatedAt,
		&i.Notes,
	)
	return i, err
}

func (q *Queries) CountUserTrust(ctx context.Context, chatID *int64, userID *int64, status string, username string, joinedSince *string, joinedUntil *string) (int64, error) {
	row := q.db.QueryRow(ctx, countUserTrust, chatID, userID, status, username, joinedSince, joinedUntil)
	var count int64
	err := row.Scan(&count)
	return count, err
}
func (q *Queries) ListUserTrustPaginated(ctx context.Context, arg ListUserTrustPaginatedParams) ([]UserTrust, error) {
	rows, err := q.db.Query(ctx, listUserTrustPaginated, arg.ChatID, arg.UserID, arg.Status, arg.Username, arg.JoinedSince, arg.JoinedUntil, arg.Limit, arg.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []UserTrust
	for rows.Next() {
		var i UserTrust
		if err := rows.Scan(
			&i.ChatID,
			&i.UserID,
			&i.Username,
			&i.FirstName,
			&i.LastName,
			&i.JoinedAt,
			&i.UpdatedAt,
			&i.Status,
			&i.Score,
			&i.MessagesChecked,
			&i.MessagesClean,
			&i.GraduatedAt,
			&i.Notes,
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
