package store

import "context"

// DualCaptchaSafety reads authoritative state rather than runtime snapshots:
// a stale cache must not authorize changing the user's Telegram restrictions.
func (q *Queries) DualCaptchaSafety(ctx context.Context, chatID, userID int64) (bool, error) {
	var safe bool
	err := q.db.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM authorized_groups WHERE chat_id=$1 AND enabled)
		AND NOT EXISTS(SELECT 1 FROM group_ban_records WHERE chat_id=$1 AND user_id=$2)
		AND NOT EXISTS(SELECT 1 FROM user_trust WHERE chat_id=$1 AND user_id=$2 AND status='banned')
		AND NOT EXISTS(SELECT 1 FROM ai_mute_ownership WHERE chat_id=$1 AND user_id=$2 AND state IN ('pending','active','reviewing'))`, chatID, userID).Scan(&safe)
	return safe, err
}
