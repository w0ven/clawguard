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
	err = q.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM group_exempt_users WHERE chat_id=$2 AND user_id=$1 AND (expires_at IS NULL OR expires_at>now()) AND NOT EXISTS(SELECT 1 FROM user_trust WHERE user_id=$1 AND is_bot)), EXISTS(SELECT 1 FROM user_trust WHERE chat_id=$2 AND user_id=$1 AND status='banned')`, userID, chatID).Scan(&active, &banned)
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

// KnownBotUser checks observed identities across groups; unknown IDs remain eligible
// for future joins, but are never exempt once a bot identity is observed.
func (q *Queries) KnownBotUser(ctx context.Context, userID int64) (bool, error) {
	var known bool
	err := q.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_trust WHERE user_id=$1 AND is_bot)`, userID).Scan(&known)
	return known, err
}

// ListGroupExemptUsersPage applies all filters before bounded pagination. Search
// uses literal substring semantics (including percent and underscore).
func (q *Queries) ListGroupExemptUsersPage(ctx context.Context, chatID int64, search, status string, limit, offset int) ([]GroupExemptUser, int64, error) {
	const where = `chat_id=$1 AND ($2='' OR strpos(lower(user_id::text),lower($2))>0 OR strpos(lower(note),lower($2))>0) AND ($3='all' OR ($3='active' AND (expires_at IS NULL OR expires_at>now())) OR ($3='expired' AND expires_at<=now()))`
	// One statement provides a common MVCC snapshot and expiry boundary even
	// when concurrent edits or expiration occur between count and page reads.
	rows, err := q.db.Query(ctx, `SELECT counted.total,page.user_id,page.note,page.expires_at,page.created_at,page.updated_at FROM (SELECT count(*) AS total FROM group_exempt_users WHERE `+where+`) counted LEFT JOIN LATERAL (SELECT user_id,note,expires_at,created_at,updated_at FROM group_exempt_users WHERE `+where+` ORDER BY user_id LIMIT $4 OFFSET $5) page ON true ORDER BY page.user_id`, chatID, search, status, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var total int64
	items := []GroupExemptUser{}
	for rows.Next() {
		var id *int64
		var note *string
		var expires, created, updated *time.Time
		if err = rows.Scan(&total, &id, &note, &expires, &created, &updated); err != nil {
			return nil, 0, err
		}
		if id != nil {
			items = append(items, GroupExemptUser{UserID: *id, Note: *note, ExpiresAt: expires, CreatedAt: *created, UpdatedAt: *updated})
		}
	}
	return items, total, rows.Err()
}
