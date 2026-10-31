package store

import (
	"context"
	"time"
)

type ModerationNotificationView struct {
	ID            int64     `json:"id"`
	CaseID        int64     `json:"case_id"`
	UserID        int64     `json:"user_id"`
	State         string    `json:"state"`
	Attempts      int32     `json:"attempts"`
	NextAttemptAt time.Time `json:"next_attempt_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ListModerationNotifications always targets a single authorized group and
// returns at most limit rows; the cursor is a notification id, descending.
func (q *Queries) ListModerationNotifications(ctx context.Context, chatID, before int64, limit int32) ([]ModerationNotificationView, error) {
	rows, err := q.db.Query(ctx, `SELECT n.id,n.case_id,n.user_id,n.state,n.attempts,n.next_attempt_at,n.updated_at FROM moderation_notifications n WHERE n.chat_id=$1 AND ($2::bigint=0 OR n.id<$2) ORDER BY n.id DESC LIMIT $3`, chatID, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModerationNotificationView{}
	for rows.Next() {
		var n ModerationNotificationView
		if err = rows.Scan(&n.ID, &n.CaseID, &n.UserID, &n.State, &n.Attempts, &n.NextAttemptAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
func (q *Queries) CountModerationNotificationBacklog(ctx context.Context, chatID int64) (int64, error) {
	var count int64
	err := q.db.QueryRow(ctx, `SELECT count(*) FROM moderation_notifications n WHERE n.chat_id=$1 AND n.state IN ('pending','leased','sending')`, chatID).Scan(&count)
	return count, err
}
