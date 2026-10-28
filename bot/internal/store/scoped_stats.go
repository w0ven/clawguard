package store

import "context"

// Match the existing global dashboard definitions, restricted in SQL before counting.
func (q *Queries) GetScopedAdminStats(ctx context.Context, chats []int64) (AdminStats, error) {
	var stats AdminStats
	if chats == nil {
		chats = []int64{}
	}
	err := q.db.QueryRow(ctx, `SELECT
 (SELECT COUNT(*) FROM groups WHERE chat_id=ANY($1::bigint[])),
 (SELECT COUNT(*) FROM pending_verifications WHERE chat_id=ANY($1::bigint[]) AND expires_at>NOW()),
 (SELECT COUNT(*) FROM violations WHERE chat_id=ANY($1::bigint[]) AND created_at>=date_trunc('day',NOW()))`, chats).Scan(&stats.GroupsCount, &stats.ActiveVerifications, &stats.TodayViolations)
	return stats, err
}
