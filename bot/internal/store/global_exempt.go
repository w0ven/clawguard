package store

import (
	"context"
	"time"
)

type GlobalExemptUser struct {
	UserID    int64      `json:"user_id,string"`
	Note      string     `json:"note"`
	ExpiresAt *time.Time `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func (q *Queries) GlobalExemptionStatus(ctx context.Context, chatID, userID int64) (active, banned bool, err error) {
	err = q.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM global_exempt_users WHERE user_id=$1 AND (expires_at IS NULL OR expires_at > now())), EXISTS(SELECT 1 FROM user_trust WHERE chat_id=$2 AND user_id=$1 AND status='banned')`, userID, chatID).Scan(&active, &banned)
	return
}

// Serialize concurrent changes for the same ID, including first creation, so
// each audit record describes the value it actually replaced.
func (q *Queries) LockGlobalExemptUser(ctx context.Context, userID int64) error {
	_, err := q.db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('global_exempt:' || $1::bigint::text, 0))`, userID)
	return err
}

func (q *Queries) ListGlobalExemptUsers(ctx context.Context) ([]GlobalExemptUser, error) {
	rows, err := q.db.Query(ctx, `SELECT user_id, note, expires_at, created_at, updated_at FROM global_exempt_users ORDER BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []GlobalExemptUser{}
	for rows.Next() {
		var x GlobalExemptUser
		if err = rows.Scan(&x.UserID, &x.Note, &x.ExpiresAt, &x.CreatedAt, &x.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, x)
	}
	return items, rows.Err()
}

func (q *Queries) GetGlobalExemptUser(ctx context.Context, userID int64) (GlobalExemptUser, error) {
	var x GlobalExemptUser
	err := q.db.QueryRow(ctx, `SELECT user_id, note, expires_at, created_at, updated_at FROM global_exempt_users WHERE user_id=$1 FOR UPDATE`, userID).Scan(&x.UserID, &x.Note, &x.ExpiresAt, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}

func (q *Queries) UpsertGlobalExemptUser(ctx context.Context, userID int64, note string, expiresAt *time.Time) (GlobalExemptUser, error) {
	var x GlobalExemptUser
	err := q.db.QueryRow(ctx, `INSERT INTO global_exempt_users(user_id,note,expires_at) VALUES($1,$2,$3) ON CONFLICT(user_id) DO UPDATE SET note=EXCLUDED.note,expires_at=EXCLUDED.expires_at,updated_at=now() RETURNING user_id,note,expires_at,created_at,updated_at`, userID, note, expiresAt).Scan(&x.UserID, &x.Note, &x.ExpiresAt, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}

func (q *Queries) DeleteGlobalExemptUser(ctx context.Context, userID int64) (int64, error) {
	tag, err := q.db.Exec(ctx, `DELETE FROM global_exempt_users WHERE user_id=$1`, userID)
	return tag.RowsAffected(), err
}
