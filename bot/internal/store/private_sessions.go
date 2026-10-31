package store

import "context"

func (q *Queries) SetMiniAppNotificationResult(ctx context.Context, pendingID int64, status string) error {
	_, err := q.db.Exec(ctx, `UPDATE miniapp_verification_notifications SET status=$2, updated_at=now() WHERE pending_id=$1 AND status='unconfirmed'`, pendingID, status)
	return err
}

func (q *Queries) RecordPrivateStart(ctx context.Context, userID int64) error {
	_, err := q.db.Exec(ctx, `INSERT INTO telegram_private_sessions(user_id) VALUES($1) ON CONFLICT (user_id) DO UPDATE SET updated_at=now()`, userID)
	return err
}
