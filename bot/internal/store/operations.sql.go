package store

import "context"

type OperationsBacklog struct {
	ActivePending           int64 `json:"active_pending"`
	DueCleanup              int64 `json:"due_cleanup"`
	RetryingCleanup         int64 `json:"retrying_cleanup"`
	OldestDueCleanupSeconds int64 `json:"oldest_due_cleanup_seconds"`
}

const getOperationsBacklog = `
SELECT
    COUNT(*) FILTER (WHERE expires_at > NOW())::BIGINT AS active_pending,
    COUNT(*) FILTER (WHERE expires_at <= NOW())::BIGINT AS due_cleanup,
    COUNT(*) FILTER (WHERE expires_at <= NOW() AND attempt_count > 0)::BIGINT AS retrying_cleanup,
    COALESCE(EXTRACT(EPOCH FROM (NOW() - MIN(expires_at) FILTER (WHERE expires_at <= NOW())))::BIGINT, 0)
        AS oldest_due_cleanup_seconds
FROM pending_verifications
`

func (q *Queries) GetOperationsBacklog(ctx context.Context) (OperationsBacklog, error) {
	row := q.db.QueryRow(ctx, getOperationsBacklog)
	var result OperationsBacklog
	err := row.Scan(
		&result.ActivePending,
		&result.DueCleanup,
		&result.RetryingCleanup,
		&result.OldestDueCleanupSeconds,
	)
	return result, err
}
