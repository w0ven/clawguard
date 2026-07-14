-- name: DeleteOldViolations :execrows
DELETE FROM violations
WHERE created_at < NOW() - make_interval(days => sqlc.arg(days)::int);

-- name: DeleteOldAIDecisions :execrows
DELETE FROM ai_decisions
WHERE created_at < NOW() - make_interval(days => sqlc.arg(days)::int)
  AND admin_override IS NULL;

-- name: DeleteOldConfigAudit :execrows
DELETE FROM config_audit
WHERE created_at < NOW() - make_interval(days => sqlc.arg(days)::int);

-- name: ArchiveZombieUserTrust :execrows
UPDATE user_trust
SET status = 'archived',
    notes = COALESCE(notes, '') || ' [archived ' || NOW()::text || ']',
    updated_at = NOW()
WHERE status NOT IN ('banned', 'archived')
  AND status = 'new'
  AND messages_checked = 0
  AND joined_at < NOW() - make_interval(days => sqlc.arg(days)::int);

-- name: DeleteOldBannedUserTrust :execrows
DELETE FROM user_trust
WHERE status = 'banned'
  AND updated_at < NOW() - make_interval(days => sqlc.arg(days)::int);
