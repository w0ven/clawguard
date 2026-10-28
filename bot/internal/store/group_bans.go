package store

import "context"

func (q *Queries) UpsertGroupBan(ctx context.Context, chatID, userID int64, reason *string, source string, bannedBy *int64) error {
	_, err := q.db.Exec(ctx, `INSERT INTO group_ban_records(chat_id,user_id,reason,source,banned_by) VALUES($1,$2,$3,$4,$5) ON CONFLICT(chat_id,user_id) DO UPDATE SET reason=excluded.reason,source=excluded.source,banned_by=excluded.banned_by,created_at=now()`, chatID, userID, reason, source, bannedBy)
	return err
}
func (q *Queries) DeleteGroupBan(ctx context.Context, chatID, userID int64) error {
	_, err := q.db.Exec(ctx, `DELETE FROM group_ban_records WHERE chat_id=$1 AND user_id=$2`, chatID, userID)
	return err
}
