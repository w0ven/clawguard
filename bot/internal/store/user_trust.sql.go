package store

import (
	"context"
	"time"
)

const getUserTrust = `-- name: GetUserTrust :one
SELECT chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, banned_at, banned_reason, notes
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
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, banned_at, banned_reason, notes
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
    banned_at,
    banned_reason,
    notes
) VALUES ($1, $2, $3, $4, $5, $6, NOW(), $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (chat_id, user_id) DO UPDATE
SET joined_at = LEAST(user_trust.joined_at, EXCLUDED.joined_at),
    username = COALESCE(EXCLUDED.username, user_trust.username),
    first_name = COALESCE(EXCLUDED.first_name, user_trust.first_name),
    last_name = COALESCE(EXCLUDED.last_name, user_trust.last_name),
    updated_at = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.updated_at ELSE NOW() END,
    status = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.status ELSE EXCLUDED.status END,
    score = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.score ELSE EXCLUDED.score END,
    messages_checked = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.messages_checked ELSE EXCLUDED.messages_checked END,
    messages_clean = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.messages_clean ELSE EXCLUDED.messages_clean END,
    graduated_at = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.graduated_at ELSE EXCLUDED.graduated_at END,
    banned_at = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.banned_at ELSE EXCLUDED.banned_at END,
    banned_reason = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.banned_reason ELSE EXCLUDED.banned_reason END,
    notes = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.notes ELSE EXCLUDED.notes END
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, banned_at, banned_reason, notes
`

const incrementUserTrustCounters = `-- name: IncrementUserTrustCounters :one
UPDATE user_trust
SET messages_checked = messages_checked + $3,
    messages_clean = messages_clean + $4,
    score = $5,
    updated_at = NOW()
WHERE chat_id = $1 AND user_id = $2
  AND status NOT IN ('banned', 'archived')
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, banned_at, banned_reason, notes
`

const updateUserTrustStatus = `-- name: UpdateUserTrustStatus :one
UPDATE user_trust
SET status = $3,
    score = $4,
    graduated_at = $5,
    banned_at = CASE WHEN $6::TIMESTAMPTZ IS NULL THEN banned_at ELSE $6 END,
    banned_reason = CASE WHEN $7::JSONB IS NULL THEN banned_reason ELSE $7 END,
    notes = $8,
    updated_at = NOW()
WHERE chat_id = $1 AND user_id = $2
  AND status NOT IN ('banned', 'archived')
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, banned_at, banned_reason, notes
`

const resetUserTrustClean = `-- name: ResetUserTrustClean :one
UPDATE user_trust
SET messages_clean = 0,
    score = $3,
    updated_at = NOW()
WHERE chat_id = $1 AND user_id = $2
  AND status NOT IN ('banned', 'archived')
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, banned_at, banned_reason, notes
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
SET score = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.score ELSE LEAST(1, GREATEST(0, user_trust.score + $3)) END,
    updated_at = CASE WHEN user_trust.status IN ('banned', 'archived') THEN user_trust.updated_at ELSE NOW() END
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, banned_at, banned_reason, notes
`

const unbanUserTrust = `-- name: UnbanUserTrust :one
UPDATE user_trust
SET status = 'new',
    score = $3,
    graduated_at = NULL,
    banned_at = NULL,
    banned_reason = NULL,
    notes = $4,
    updated_at = NOW()
WHERE chat_id = $1
  AND user_id = $2
  AND status = 'banned'
RETURNING chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, banned_at, banned_reason, notes
`

const clearUserTrustBanMeta = `-- name: ClearUserTrustBanMeta :exec
UPDATE user_trust
SET banned_at = NULL, banned_reason = NULL
WHERE chat_id = $1 AND user_id = $2
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
  AND ($7::BOOLEAN OR chat_id = ANY($8::BIGINT[]))
`

const listUserTrustPaginated = `-- name: ListUserTrustPaginated :many
SELECT chat_id, user_id, username, first_name, last_name, joined_at, updated_at, status, score, messages_checked, messages_clean, graduated_at, banned_at, banned_reason, notes
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
	BannedAt        *time.Time
	BannedReason    []byte
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
	ChatID       int64
	UserID       int64
	Status       string
	Score        float64
	GraduatedAt  *time.Time
	BannedAt     *time.Time
	BannedReason []byte
	Notes        *string
}

type UnbanUserTrustParams struct {
	ChatID int64
	UserID int64
	Score  float64
	Notes  *string
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
	ChatID       *int64
	UserID       *int64
	Status       string
	Username     string
	JoinedSince  *string
	JoinedUntil  *string
	Limit        int32
	Offset       int32
	ScopeGlobal  bool
	ScopeChatIDs []int64
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
		&i.BannedAt,
		&i.BannedReason,
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
		&i.BannedAt,
		&i.BannedReason,
		&i.Notes,
	)
	return i, err
}

func (q *Queries) UpsertUserTrust(ctx context.Context, arg UpsertUserTrustParams) (UserTrust, error) {
	row := q.db.QueryRow(ctx, upsertUserTrust, arg.ChatID, arg.UserID, arg.Username, arg.FirstName, arg.LastName, arg.JoinedAt, arg.Status, arg.Score, arg.MessagesChecked, arg.MessagesClean, arg.GraduatedAt, arg.BannedAt, arg.BannedReason, arg.Notes)
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
		&i.BannedAt,
		&i.BannedReason,
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
		&i.BannedAt,
		&i.BannedReason,
		&i.Notes,
	)
	return i, err
}

func (q *Queries) UpdateUserTrustStatus(ctx context.Context, arg UpdateUserTrustStatusParams) (UserTrust, error) {
	row := q.db.QueryRow(ctx, updateUserTrustStatus, arg.ChatID, arg.UserID, arg.Status, arg.Score, arg.GraduatedAt, arg.BannedAt, arg.BannedReason, arg.Notes)
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
		&i.BannedAt,
		&i.BannedReason,
		&i.Notes,
	)
	return i, err
}

func (q *Queries) UnbanUserTrust(ctx context.Context, arg UnbanUserTrustParams) (UserTrust, error) {
	row := q.db.QueryRow(ctx, unbanUserTrust, arg.ChatID, arg.UserID, arg.Score, arg.Notes)
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
		&i.BannedAt,
		&i.BannedReason,
		&i.Notes,
	)
	return i, err
}

func (q *Queries) ClearUserTrustBanMeta(ctx context.Context, chatID, userID int64) error {
	_, err := q.db.Exec(ctx, clearUserTrustBanMeta, chatID, userID)
	return err
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
		&i.BannedAt,
		&i.BannedReason,
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
		&i.BannedAt,
		&i.BannedReason,
		&i.Notes,
	)
	return i, err
}

func (q *Queries) CountUserTrust(ctx context.Context, chatID *int64, userID *int64, status string, username string, joinedSince *string, joinedUntil *string, scopeGlobal bool, scopeChatIDs []int64) (int64, error) {
	row := q.db.QueryRow(ctx, countUserTrust, chatID, userID, status, username, joinedSince, joinedUntil, scopeGlobal, pgInt64Array(scopeChatIDs))
	var count int64
	err := row.Scan(&count)
	return count, err
}
func (q *Queries) ListUserTrustPaginated(ctx context.Context, arg ListUserTrustPaginatedParams) ([]UserTrust, error) {
	rows, err := q.db.Query(ctx, listUserTrustPaginated, arg.ChatID, arg.UserID, arg.Status, arg.Username, arg.JoinedSince, arg.JoinedUntil, arg.Limit, arg.Offset, arg.ScopeGlobal, pgInt64Array(arg.ScopeChatIDs))
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
			&i.BannedAt,
			&i.BannedReason,
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
