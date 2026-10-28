package store

import (
	"context"
	"time"
)

type GroupExemptUser struct {
	UserID    int64      `json:"user_id,string"`
	Note      string     `json:"note"`
	ExpiresAt *time.Time `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func (q *Queries) GroupExemptionStatus(ctx context.Context, chatID, userID int64) (active, banned bool, err error) {
	err = q.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM group_exempt_users WHERE chat_id=$2 AND user_id=$1 AND (expires_at IS NULL OR expires_at>now())), EXISTS(SELECT 1 FROM user_trust WHERE chat_id=$2 AND user_id=$1 AND status='banned')`, userID, chatID).Scan(&active, &banned)
	return
}
func (q *Queries) LockGroupExemptUser(ctx context.Context, chatID, userID int64) error {
	_, err := q.db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('group_exempt:' || $1::bigint::text || ':' || $2::bigint::text, 0))`, chatID, userID)
	return err
}
func (q *Queries) ListGroupExemptUsers(ctx context.Context, chatID int64) ([]GroupExemptUser, error) {
	rows, err := q.db.Query(ctx, `SELECT user_id,note,expires_at,created_at,updated_at FROM group_exempt_users WHERE chat_id=$1 ORDER BY user_id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []GroupExemptUser{}
	for rows.Next() {
		var x GroupExemptUser
		if err = rows.Scan(&x.UserID, &x.Note, &x.ExpiresAt, &x.CreatedAt, &x.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, x)
	}
	return items, rows.Err()
}
func (q *Queries) GetGroupExemptUser(ctx context.Context, chatID, userID int64) (GroupExemptUser, error) {
	var x GroupExemptUser
	err := q.db.QueryRow(ctx, `SELECT user_id,note,expires_at,created_at,updated_at FROM group_exempt_users WHERE chat_id=$1 AND user_id=$2 FOR UPDATE`, chatID, userID).Scan(&x.UserID, &x.Note, &x.ExpiresAt, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}
func (q *Queries) UpsertGroupExemptUser(ctx context.Context, chatID, userID int64, note string, expiresAt *time.Time) (GroupExemptUser, error) {
	var x GroupExemptUser
	err := q.db.QueryRow(ctx, `INSERT INTO group_exempt_users(chat_id,user_id,note,expires_at) VALUES($1,$2,$3,$4) ON CONFLICT(chat_id,user_id) DO UPDATE SET note=EXCLUDED.note,expires_at=EXCLUDED.expires_at,updated_at=now() RETURNING user_id,note,expires_at,created_at,updated_at`, chatID, userID, note, expiresAt).Scan(&x.UserID, &x.Note, &x.ExpiresAt, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}
func (q *Queries) DeleteGroupExemptUser(ctx context.Context, chatID, userID int64) (int64, error) {
	tag, err := q.db.Exec(ctx, `DELETE FROM group_exempt_users WHERE chat_id=$1 AND user_id=$2`, chatID, userID)
	return tag.RowsAffected(), err
}
