package store

import "context"

const deleteOldViolations = `-- name: DeleteOldViolations :execrows
DELETE FROM violations
WHERE created_at < NOW() - make_interval(days => $1::int)
`

const deleteOldAIDecisions = `-- name: DeleteOldAIDecisions :execrows
DELETE FROM ai_decisions
WHERE created_at < NOW() - make_interval(days => $1::int)
  AND admin_override IS NULL
`

const deleteOldConfigAudit = `-- name: DeleteOldConfigAudit :execrows
DELETE FROM config_audit
WHERE created_at < NOW() - make_interval(days => $1::int)
`

const archiveZombieUserTrust = `-- name: ArchiveZombieUserTrust :execrows
UPDATE user_trust
SET status = 'archived',
    notes = COALESCE(notes, '') || ' [archived ' || NOW()::text || ']',
    updated_at = NOW()
WHERE status NOT IN ('banned', 'archived')
  AND status = 'new'
  AND messages_checked = 0
  AND joined_at < NOW() - make_interval(days => $1::int)
`

const deleteOldBannedUserTrust = `-- name: DeleteOldBannedUserTrust :execrows
DELETE FROM user_trust
WHERE status = 'banned'
  AND updated_at < NOW() - make_interval(days => $1::int)
`

func (q *Queries) DeleteOldViolations(ctx context.Context, days int32) (int64, error) {
	result, err := q.db.Exec(ctx, deleteOldViolations, days)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

func (q *Queries) DeleteOldAIDecisions(ctx context.Context, days int32) (int64, error) {
	result, err := q.db.Exec(ctx, deleteOldAIDecisions, days)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

func (q *Queries) DeleteOldConfigAudit(ctx context.Context, days int32) (int64, error) {
	result, err := q.db.Exec(ctx, deleteOldConfigAudit, days)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

func (q *Queries) ArchiveZombieUserTrust(ctx context.Context, days int32) (int64, error) {
	result, err := q.db.Exec(ctx, archiveZombieUserTrust, days)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

func (q *Queries) DeleteOldBannedUserTrust(ctx context.Context, days int32) (int64, error) {
	result, err := q.db.Exec(ctx, deleteOldBannedUserTrust, days)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}
