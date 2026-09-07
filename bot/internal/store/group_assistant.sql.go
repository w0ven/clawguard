package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrGroupAssistantConflictMemoryChanged = errors.New("group assistant conflict memory changed")
	ErrGroupAssistantConflictMemoryGone    = errors.New("group assistant conflict memory unavailable")
	ErrGroupAssistantConflictAuthority     = errors.New("group assistant conflict authority requires explicit admin confirmation")
	ErrGroupAssistantSourceInvalid         = errors.New("group assistant source is no longer approved")
)

const groupAssistantPolicyColumns = `chat_id, version, chat_enabled, learning_enabled, trigger_mode,
	followup_window_sec, max_followup_turns, chat_model_ref, learning_model_ref, temperature,
	system_prompt, history_limit, retention_days, collection_policy, tool_allowlist, allow_domains,
	max_queue_depth, max_queue_wait_sec, updated_by, created_at, updated_at`

const getGroupAssistantPolicySQL = `SELECT ` + groupAssistantPolicyColumns + `
FROM group_assistant_policies WHERE chat_id = $1`

const insertGroupAssistantPolicySQL = `INSERT INTO group_assistant_policies
(chat_id, chat_enabled, learning_enabled, trigger_mode, followup_window_sec, max_followup_turns,
 chat_model_ref, learning_model_ref, temperature, system_prompt, history_limit, retention_days,
 collection_policy, tool_allowlist, allow_domains, max_queue_depth, max_queue_wait_sec, updated_by)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
ON CONFLICT (chat_id) DO NOTHING
RETURNING ` + groupAssistantPolicyColumns

const updateGroupAssistantPolicySQL = `UPDATE group_assistant_policies SET
chat_enabled=$3, learning_enabled=$4, trigger_mode=$5, followup_window_sec=$6,
max_followup_turns=$7, chat_model_ref=$8, learning_model_ref=$9, temperature=$10,
system_prompt=$11, history_limit=$12, retention_days=$13, collection_policy=$14,
tool_allowlist=$15, allow_domains=$16, max_queue_depth=$17, max_queue_wait_sec=$18,
updated_by=$19, version=version+1, updated_at=NOW()
WHERE chat_id=$1 AND version=$2
RETURNING ` + groupAssistantPolicyColumns

func scanGroupAssistantPolicy(row interface{ Scan(...any) error }) (GroupAssistantPolicy, error) {
	var v GroupAssistantPolicy
	err := row.Scan(&v.ChatID, &v.Version, &v.ChatEnabled, &v.LearningEnabled, &v.TriggerMode,
		&v.FollowupWindowSec, &v.MaxFollowupTurns, &v.ChatModelRef, &v.LearningModelRef,
		&v.Temperature, &v.SystemPrompt, &v.HistoryLimit, &v.RetentionDays, &v.CollectionPolicy,
		&v.ToolAllowlist, &v.AllowDomains, &v.MaxQueueDepth, &v.MaxQueueWaitSec, &v.UpdatedBy,
		&v.CreatedAt, &v.UpdatedAt)
	return v, err
}

func (q *Queries) GetGroupAssistantPolicy(ctx context.Context, chatID int64) (GroupAssistantPolicy, error) {
	return scanGroupAssistantPolicy(q.db.QueryRow(ctx, getGroupAssistantPolicySQL, chatID))
}

func (q *Queries) UpsertGroupAssistantPolicy(ctx context.Context, arg UpsertGroupAssistantPolicyParams) (GroupAssistantPolicy, error) {
	params := []any{arg.ChatID, arg.ChatEnabled, arg.LearningEnabled, arg.TriggerMode, arg.FollowupWindowSec,
		arg.MaxFollowupTurns, arg.ChatModelRef, arg.LearningModelRef, arg.Temperature, arg.SystemPrompt,
		arg.HistoryLimit, arg.RetentionDays, arg.CollectionPolicy, arg.ToolAllowlist, arg.AllowDomains,
		arg.MaxQueueDepth, arg.MaxQueueWaitSec, arg.UpdatedBy}
	if arg.ExpectedVersion <= 0 {
		v, err := scanGroupAssistantPolicy(q.db.QueryRow(ctx, insertGroupAssistantPolicySQL, params...))
		if err == nil {
			return v, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return GroupAssistantPolicy{}, err
		}
		return GroupAssistantPolicy{}, pgx.ErrNoRows
	}
	updateArgs := []any{arg.ChatID, arg.ExpectedVersion}
	updateArgs = append(updateArgs, params[1:]...)
	return scanGroupAssistantPolicy(q.db.QueryRow(ctx, updateGroupAssistantPolicySQL, updateArgs...))
}

const groupAssistantPoolColumns = `chat_id, version, strategy, config, updated_by, created_at, updated_at`
const getGroupAssistantPoolSQL = `SELECT ` + groupAssistantPoolColumns + ` FROM group_assistant_pools WHERE chat_id=$1`
const insertGroupAssistantPoolSQL = `INSERT INTO group_assistant_pools (chat_id,strategy,config,updated_by)
VALUES ($1,$2,$3,$4) ON CONFLICT (chat_id) DO NOTHING RETURNING ` + groupAssistantPoolColumns
const updateGroupAssistantPoolSQL = `UPDATE group_assistant_pools SET strategy=$3, config=$4, updated_by=$5,
version=version+1, updated_at=NOW() WHERE chat_id=$1 AND version=$2 RETURNING ` + groupAssistantPoolColumns

func scanGroupAssistantPool(row interface{ Scan(...any) error }) (GroupAssistantPool, error) {
	var v GroupAssistantPool
	err := row.Scan(&v.ChatID, &v.Version, &v.Strategy, &v.Config, &v.UpdatedBy, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

func (q *Queries) GetGroupAssistantPool(ctx context.Context, chatID int64) (GroupAssistantPool, error) {
	return scanGroupAssistantPool(q.db.QueryRow(ctx, getGroupAssistantPoolSQL, chatID))
}

// ListEnabledGroupAssistantPools returns only pools referenced by a currently
// enabled chat or learning policy. It is used to derive the process-wide
// provider/model cap without mutating runtime state from read-only endpoints.
func (q *Queries) ListEnabledGroupAssistantPools(ctx context.Context) ([]GroupAssistantPool, error) {
	rows, err := q.db.Query(ctx, `SELECT p.chat_id,p.version,p.strategy,p.config,p.updated_by,p.created_at,p.updated_at
		FROM group_assistant_pools p JOIN group_assistant_policies a ON a.chat_id=p.chat_id
		WHERE a.chat_enabled=TRUE OR a.learning_enabled=TRUE
		ORDER BY p.chat_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]GroupAssistantPool, 0)
	for rows.Next() {
		item, scanErr := scanGroupAssistantPool(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (q *Queries) UpsertGroupAssistantPool(ctx context.Context, arg UpsertGroupAssistantPoolParams) (GroupAssistantPool, error) {
	if arg.ExpectedVersion <= 0 {
		v, err := scanGroupAssistantPool(q.db.QueryRow(ctx, insertGroupAssistantPoolSQL, arg.ChatID, arg.Strategy, arg.Config, arg.UpdatedBy))
		if err == nil {
			return v, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return GroupAssistantPool{}, err
		}
		return GroupAssistantPool{}, pgx.ErrNoRows
	}
	return scanGroupAssistantPool(q.db.QueryRow(ctx, updateGroupAssistantPoolSQL, arg.ChatID, arg.ExpectedVersion, arg.Strategy, arg.Config, arg.UpdatedBy))
}

const groupAssistantMessageColumns = `id, chat_id, thread_id, telegram_message_id, sender_id, sender_name,
role, text, approved, delivered, content_hash, expires_at, source_type, source_id, created_at, updated_at`
const upsertGroupAssistantMessageSQL = `INSERT INTO group_assistant_messages AS stored_message
(chat_id,thread_id,telegram_message_id,sender_id,sender_name,role,text,approved,delivered,content_hash,expires_at,source_type,source_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
ON CONFLICT (chat_id,telegram_message_id,role) DO UPDATE SET
thread_id=EXCLUDED.thread_id, sender_id=EXCLUDED.sender_id, sender_name=EXCLUDED.sender_name,
text=EXCLUDED.text, approved=EXCLUDED.approved, delivered=EXCLUDED.delivered,
content_hash=EXCLUDED.content_hash, expires_at=LEAST(stored_message.expires_at, EXCLUDED.expires_at), source_type=EXCLUDED.source_type,
source_id=EXCLUDED.source_id, updated_at=NOW()
RETURNING ` + groupAssistantMessageColumns

func scanGroupAssistantMessage(row interface{ Scan(...any) error }) (GroupAssistantMessage, error) {
	var v GroupAssistantMessage
	err := row.Scan(&v.ID, &v.ChatID, &v.ThreadID, &v.TelegramMessageID, &v.SenderID, &v.SenderName,
		&v.Role, &v.Text, &v.Approved, &v.Delivered, &v.ContentHash, &v.ExpiresAt, &v.SourceType,
		&v.SourceID, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

func (q *Queries) UpsertGroupAssistantMessage(ctx context.Context, arg UpsertGroupAssistantMessageParams) (GroupAssistantMessage, error) {
	return scanGroupAssistantMessage(q.db.QueryRow(ctx, upsertGroupAssistantMessageSQL, arg.ChatID, arg.ThreadID,
		arg.TelegramMessageID, arg.SenderID, arg.SenderName, arg.Role, arg.Text, arg.Approved, arg.Delivered,
		arg.ContentHash, arg.ExpiresAt, arg.SourceType, arg.SourceID))
}

const updateGroupAssistantMessageSQL = `UPDATE group_assistant_messages SET
thread_id=$3, sender_id=$4, sender_name=$5, text=$6, content_hash=$7, source_type=$8, source_id=$9, updated_at=NOW()
WHERE chat_id=$1 AND telegram_message_id=$2 AND role='user' AND approved=TRUE AND delivered=TRUE
RETURNING ` + groupAssistantMessageColumns

func (q *Queries) UpdateGroupAssistantMessage(ctx context.Context, arg UpdateGroupAssistantMessageParams) (GroupAssistantMessage, error) {
	return scanGroupAssistantMessage(q.db.QueryRow(ctx, updateGroupAssistantMessageSQL, arg.ChatID, arg.TelegramMessageID,
		arg.ThreadID, arg.SenderID, arg.SenderName, arg.Text, arg.ContentHash, arg.SourceType, arg.SourceID))
}

func (q *Queries) GroupAssistantMessageSourceValid(ctx context.Context, chatID, telegramMessageID int64, contentHash string) (bool, error) {
	var valid bool
	err := q.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM group_assistant_messages
		WHERE chat_id=$1 AND telegram_message_id=$2 AND role='user' AND approved=TRUE AND delivered=TRUE AND expires_at>NOW()
		AND content_hash=$3)`, chatID, telegramMessageID, contentHash).Scan(&valid)
	return valid, err
}

// InvalidateGroupAssistantSource revokes an edited/blocked source and all
// derived memories that explicitly cite that source. It never deletes the
// Telegram-origin audit rows and it never sets a forgotten tombstone back.
func (q *Queries) InvalidateGroupAssistantSource(ctx context.Context, chatID, telegramMessageID int64) error {
	return q.Transact(ctx, func(tx *Queries) error {
		if _, err := tx.db.Exec(ctx, `UPDATE group_assistant_messages
			SET approved=FALSE, delivered=FALSE, updated_at=NOW()
			WHERE chat_id=$1 AND telegram_message_id=$2 AND role='user'`, chatID, telegramMessageID); err != nil {
			return err
		}
		rows, err := tx.db.Query(ctx, `UPDATE group_assistant_memories
			SET active=FALSE, expires_at=LEAST(expires_at,NOW()), source_verified='invalid', version=version+1, updated_at=NOW()
			WHERE chat_id=$1 AND source_message_id=$2 AND active=TRUE
			RETURNING `+groupAssistantMemoryColumns, chatID, telegramMessageID)
		if err != nil {
			return err
		}
		memories := make([]GroupAssistantMemory, 0)
		for rows.Next() {
			memory, scanErr := scanGroupAssistantMemory(rows)
			if scanErr != nil {
				rows.Close()
				return scanErr
			}
			memories = append(memories, memory)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, memory := range memories {
			if err := tx.insertGroupAssistantMemoryVersion(ctx, memory, nil, "source_invalidated"); err != nil {
				return err
			}
		}
		_, err = tx.db.Exec(ctx, `UPDATE group_assistant_conflicts SET status='rejected', resolved_at=NOW()
			WHERE chat_id=$1 AND source_message_id=$2 AND status='pending'`, chatID, telegramMessageID)
		return err
	})
}

func (q *Queries) ListGroupAssistantMessages(ctx context.Context, arg ListGroupAssistantMessagesParams) ([]GroupAssistantMessage, error) {
	limit := arg.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT ` + groupAssistantMessageColumns + ` FROM group_assistant_messages
WHERE chat_id=$1 AND approved=TRUE AND delivered=TRUE AND expires_at>NOW()`
	params := []any{arg.ChatID}
	if arg.ThreadID != nil {
		query += ` AND thread_id=$` + itoa(len(params)+1)
		params = append(params, *arg.ThreadID)
	}
	if arg.SenderID != nil {
		query += ` AND sender_id=$` + itoa(len(params)+1)
		params = append(params, *arg.SenderID)
	}
	if arg.Query != "" {
		query += ` AND strpos(lower(text), lower($` + itoa(len(params)+1) + `)) > 0`
		params = append(params, arg.Query)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT $` + itoa(len(params)+1)
	params = append(params, limit)
	rows, err := q.db.Query(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]GroupAssistantMessage, 0)
	for rows.Next() {
		v, scanErr := scanGroupAssistantMessage(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, v)
	}
	return items, rows.Err()
}

const groupAssistantMemoryColumns = `id, chat_id, subject, content, memory_type, authority_level, valid_scope,
source_type, source_message_id, source_chat_id, source_operator_id, source_operator_name, source_snippet,
source_created_at, source_verified, source_content_hash, expires_at, active, forgotten_at, forgotten_by, dedupe_hash, version, created_at, updated_at`

func scanGroupAssistantMemory(row interface{ Scan(...any) error }) (GroupAssistantMemory, error) {
	var v GroupAssistantMemory
	err := row.Scan(&v.ID, &v.ChatID, &v.Subject, &v.Content, &v.MemoryType, &v.AuthorityLevel, &v.ValidScope,
		&v.SourceType, &v.SourceMessageID, &v.SourceChatID, &v.SourceOperatorID, &v.SourceOperatorName,
		&v.SourceSnippet, &v.SourceCreatedAt, &v.SourceVerified, &v.SourceContentHash, &v.ExpiresAt, &v.Active, &v.ForgottenAt,
		&v.ForgottenBy, &v.DedupeHash, &v.Version, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

const insertGroupAssistantMemorySQL = `INSERT INTO group_assistant_memories
(chat_id,subject,content,memory_type,authority_level,valid_scope,source_type,source_message_id,source_chat_id,
 source_operator_id,source_operator_name,source_snippet,source_created_at,source_verified,source_content_hash,expires_at,dedupe_hash)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
RETURNING ` + groupAssistantMemoryColumns

const insertGroupAssistantMemoryVersionSQL = `INSERT INTO group_assistant_memory_versions
(memory_id,version,content,memory_type,authority_level,valid_scope,source_type,source_message_id,source_snippet,changed_by,change_kind)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`

func (q *Queries) lockGroupAssistantSource(ctx context.Context, tx *Queries, chatID int64, messageID *int64, expectedHash string) error {
	if messageID == nil || expectedHash == "" {
		return nil
	}
	var approved, delivered bool
	var contentHash string
	var expiresAt time.Time
	err := tx.db.QueryRow(ctx, `SELECT approved,delivered,content_hash,expires_at FROM group_assistant_messages
		WHERE chat_id=$1 AND telegram_message_id=$2 AND role='user' FOR UPDATE`, chatID, *messageID).Scan(&approved, &delivered, &contentHash, &expiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrGroupAssistantSourceInvalid
		}
		return err
	}
	if !approved || !delivered || contentHash != expectedHash || !expiresAt.After(time.Now().UTC()) {
		return ErrGroupAssistantSourceInvalid
	}
	return nil
}

func (q *Queries) CreateGroupAssistantMemory(ctx context.Context, arg CreateGroupAssistantMemoryParams) (GroupAssistantMemory, error) {
	var out GroupAssistantMemory
	err := q.Transact(ctx, func(tx *Queries) error {
		if err := q.lockGroupAssistantSource(ctx, tx, arg.ChatID, arg.SourceMessageID, arg.SourceContentHash); err != nil {
			return err
		}
		var err error
		out, err = scanGroupAssistantMemory(tx.db.QueryRow(ctx, insertGroupAssistantMemorySQL, arg.ChatID, arg.Subject,
			arg.Content, arg.MemoryType, arg.AuthorityLevel, arg.ValidScope, arg.SourceType, arg.SourceMessageID,
			arg.SourceChatID, arg.SourceOperatorID, arg.SourceOperatorName, arg.SourceSnippet, arg.SourceCreatedAt,
			arg.SourceVerified, arg.SourceContentHash, arg.ExpiresAt, arg.DedupeHash))
		if err != nil {
			return err
		}
		return tx.insertGroupAssistantMemoryVersion(ctx, out, arg.ChangedBy, "create")
	})
	return out, err
}

func (q *Queries) insertGroupAssistantMemoryVersion(ctx context.Context, memory GroupAssistantMemory, changedBy *int64, kind string) error {
	_, err := q.db.Exec(ctx, insertGroupAssistantMemoryVersionSQL, memory.ID, memory.Version, memory.Content,
		memory.MemoryType, memory.AuthorityLevel, memory.ValidScope, memory.SourceType, memory.SourceMessageID,
		memory.SourceSnippet, changedBy, kind)
	return err
}

const updateGroupAssistantMemorySQL = `UPDATE group_assistant_memories SET
subject=$4, content=$5, memory_type=$6, authority_level=$7, valid_scope=$8, source_type=$9,
source_message_id=$10, source_chat_id=$11, source_operator_id=$12, source_operator_name=$13, source_snippet=$14,
source_created_at=$15, source_verified=$16, source_content_hash=$17, expires_at=$18, dedupe_hash=$19, version=version+1, updated_at=NOW()
WHERE id=$1 AND chat_id=$2 AND version=$3 AND active=TRUE
RETURNING ` + groupAssistantMemoryColumns

func (q *Queries) UpdateGroupAssistantMemory(ctx context.Context, arg UpdateGroupAssistantMemoryParams) (GroupAssistantMemory, error) {
	var out GroupAssistantMemory
	if arg.ChatID == 0 {
		// Scope is mandatory. Never infer a target group from an unscoped ID.
		return out, pgx.ErrNoRows
	}
	err := q.Transact(ctx, func(tx *Queries) error {
		if err := q.lockGroupAssistantSource(ctx, tx, arg.ChatID, arg.SourceMessageID, arg.SourceContentHash); err != nil {
			return err
		}
		var err error
		out, err = scanGroupAssistantMemory(tx.db.QueryRow(ctx, updateGroupAssistantMemorySQL, arg.ID, arg.ChatID, arg.ExpectedVersion,
			arg.Subject, arg.Content, arg.MemoryType, arg.AuthorityLevel, arg.ValidScope, arg.SourceType,
			arg.SourceMessageID, arg.SourceChatID, arg.SourceOperatorID, arg.SourceOperatorName, arg.SourceSnippet,
			arg.SourceCreatedAt, arg.SourceVerified, arg.SourceContentHash, arg.ExpiresAt, arg.DedupeHash))
		if err != nil {
			return err
		}
		return tx.insertGroupAssistantMemoryVersion(ctx, out, arg.ChangedBy, "update")
	})
	return out, err
}

func (q *Queries) ListGroupAssistantMemories(ctx context.Context, chatID int64, includeInactive bool, queryText string, limit int32) ([]GroupAssistantMemory, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := `SELECT ` + groupAssistantMemoryColumns + ` FROM group_assistant_memories WHERE chat_id=$1`
	params := []any{chatID}
	if !includeInactive {
		query += ` AND active=TRUE AND expires_at>NOW()`
	}
	if queryText != "" {
		query += ` AND (strpos(lower(subject), lower($2)) > 0 OR strpos(lower(content), lower($2)) > 0)`
		params = append(params, queryText)
	}
	query += ` ORDER BY active DESC, expires_at DESC, updated_at DESC, id DESC LIMIT $` + itoa(len(params)+1)
	params = append(params, limit)
	rows, err := q.db.Query(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]GroupAssistantMemory, 0)
	for rows.Next() {
		v, scanErr := scanGroupAssistantMemory(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, v)
	}
	return items, rows.Err()
}

func (q *Queries) GetGroupAssistantMemory(ctx context.Context, id, chatID int64) (GroupAssistantMemory, error) {
	return scanGroupAssistantMemory(q.db.QueryRow(ctx, `SELECT `+groupAssistantMemoryColumns+` FROM group_assistant_memories WHERE id=$1 AND chat_id=$2`, id, chatID))
}

func (q *Queries) ListGroupAssistantMemoryVersions(ctx context.Context, id, chatID int64) ([]GroupAssistantMemoryVersion, error) {
	rows, err := q.db.Query(ctx, `SELECT v.id,v.memory_id,v.version,v.content,v.memory_type,v.authority_level,v.valid_scope,
	v.source_type,v.source_message_id,v.source_snippet,v.changed_by,v.change_kind,v.created_at
	FROM group_assistant_memory_versions v JOIN group_assistant_memories m ON m.id=v.memory_id
	WHERE v.memory_id=$1 AND m.chat_id=$2 ORDER BY v.version DESC, v.id DESC`, id, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]GroupAssistantMemoryVersion, 0)
	for rows.Next() {
		var v GroupAssistantMemoryVersion
		if err := rows.Scan(&v.ID, &v.MemoryID, &v.Version, &v.Content, &v.MemoryType, &v.AuthorityLevel,
			&v.ValidScope, &v.SourceType, &v.SourceMessageID, &v.SourceSnippet, &v.ChangedBy, &v.ChangeKind, &v.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}

func (q *Queries) ForgetGroupAssistantMemory(ctx context.Context, id, chatID, actorID int64) error {
	var memory GroupAssistantMemory
	err := q.Transact(ctx, func(tx *Queries) error {
		var err error
		memory, err = scanGroupAssistantMemory(tx.db.QueryRow(ctx, `UPDATE group_assistant_memories
		SET active=FALSE, expires_at=LEAST(expires_at,NOW()), forgotten_at=NOW(), forgotten_by=$3, version=version+1, updated_at=NOW()
		WHERE id=$1 AND chat_id=$2 AND active=TRUE RETURNING `+groupAssistantMemoryColumns, id, chatID, actorID))
		if err != nil {
			return err
		}
		return tx.insertGroupAssistantMemoryVersion(ctx, memory, &actorID, "forget")
	})
	return err
}

const insertGroupAssistantConflictSQL = `INSERT INTO group_assistant_conflicts
(chat_id,memory_id,subject,candidate_content,candidate_scope,candidate_authority,source_type,source_message_id,source_chat_id,source_snippet)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id,chat_id,memory_id,subject,candidate_content,candidate_scope,candidate_authority,
source_type,source_message_id,source_chat_id,source_snippet,status,resolved_by,resolved_at,created_at`

func scanGroupAssistantConflict(row interface{ Scan(...any) error }) (GroupAssistantConflict, error) {
	var v GroupAssistantConflict
	err := row.Scan(&v.ID, &v.ChatID, &v.MemoryID, &v.Subject, &v.CandidateContent, &v.CandidateScope,
		&v.CandidateAuthority, &v.SourceType, &v.SourceMessageID, &v.SourceChatID, &v.SourceSnippet, &v.Status,
		&v.ResolvedBy, &v.ResolvedAt, &v.CreatedAt)
	return v, err
}

func (q *Queries) CreateGroupAssistantConflict(ctx context.Context, arg CreateGroupAssistantConflictParams) (GroupAssistantConflict, error) {
	return scanGroupAssistantConflict(q.db.QueryRow(ctx, insertGroupAssistantConflictSQL, arg.ChatID, arg.MemoryID,
		arg.Subject, arg.CandidateContent, arg.CandidateScope, arg.CandidateAuthority, arg.SourceType,
		arg.SourceMessageID, arg.SourceChatID, arg.SourceSnippet))
}

func (q *Queries) GetGroupAssistantConflict(ctx context.Context, id, chatID int64) (GroupAssistantConflict, error) {
	return scanGroupAssistantConflict(q.db.QueryRow(ctx, `SELECT id,chat_id,memory_id,subject,candidate_content,candidate_scope,candidate_authority,
		source_type,source_message_id,source_chat_id,source_snippet,status,resolved_by,resolved_at,created_at
		FROM group_assistant_conflicts WHERE id=$1 AND chat_id=$2`, id, chatID))
}

func (q *Queries) HasPendingGroupAssistantConflict(ctx context.Context, chatID int64, memoryID *int64, subject, scope, content string) (bool, error) {
	var exists bool
	err := q.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM group_assistant_conflicts
		WHERE chat_id=$1 AND memory_id IS NOT DISTINCT FROM $2 AND subject=$3 AND candidate_scope=$4
		AND candidate_content=$5 AND status='pending')`, chatID, memoryID, subject, scope, content).Scan(&exists)
	return exists, err
}

func (q *Queries) ListGroupAssistantConflicts(ctx context.Context, chatID int64, status string, limit int32) ([]GroupAssistantConflict, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := `SELECT id,chat_id,memory_id,subject,candidate_content,candidate_scope,candidate_authority,source_type,
source_message_id,source_chat_id,source_snippet,status,resolved_by,resolved_at,created_at FROM group_assistant_conflicts WHERE chat_id=$1`
	params := []any{chatID}
	if status != "" {
		query += ` AND status=$2`
		params = append(params, status)
	}
	query += ` ORDER BY created_at DESC,id DESC LIMIT $` + itoa(len(params)+1)
	params = append(params, limit)
	rows, err := q.db.Query(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]GroupAssistantConflict, 0)
	for rows.Next() {
		v, scanErr := scanGroupAssistantConflict(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, v)
	}
	return items, rows.Err()
}

func groupAssistantAuthorityRank(level string) int {
	switch level {
	case "pinned_announcement", "admin_explicit":
		return 3
	case "admin_base":
		return 2
	case "learned_fact":
		return 1
	default:
		return 0
	}
}

// ResolveGroupAssistantConflict keeps the original store-level helper for
// internal callers and legacy integration tests. API callers must use the
// versioned options method below.
func (q *Queries) ResolveGroupAssistantConflict(ctx context.Context, id, chatID, actorID int64, accept bool) (GroupAssistantConflict, error) {
	return q.resolveGroupAssistantConflict(ctx, ResolveGroupAssistantConflictParams{
		ID: id, ChatID: chatID, ActorID: actorID, Accept: accept,
	})
}

func (q *Queries) ResolveGroupAssistantConflictWithOptions(ctx context.Context, arg ResolveGroupAssistantConflictParams) (GroupAssistantConflict, error) {
	return q.resolveGroupAssistantConflict(ctx, arg)
}

func (q *Queries) resolveGroupAssistantConflict(ctx context.Context, arg ResolveGroupAssistantConflictParams) (GroupAssistantConflict, error) {
	var out GroupAssistantConflict
	err := q.Transact(ctx, func(tx *Queries) error {
		var current GroupAssistantConflict
		var err error
		current, err = scanGroupAssistantConflict(tx.db.QueryRow(ctx, `SELECT id,chat_id,memory_id,subject,candidate_content,candidate_scope,
			candidate_authority,source_type,source_message_id,source_chat_id,source_snippet,status,resolved_by,resolved_at,created_at
			FROM group_assistant_conflicts WHERE id=$1 AND chat_id=$2 FOR UPDATE`, arg.ID, arg.ChatID))
		if err != nil {
			return err
		}
		if current.Status != "pending" {
			out = current
			return nil
		}
		status := "rejected"
		if arg.Accept {
			status = "accepted"
			if current.MemoryID != nil {
				var memory GroupAssistantMemory
				memory, err = scanGroupAssistantMemory(tx.db.QueryRow(ctx, `SELECT `+groupAssistantMemoryColumns+`
					FROM group_assistant_memories WHERE id=$1 AND chat_id=$2 FOR UPDATE`, *current.MemoryID, arg.ChatID))
				if err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						return ErrGroupAssistantConflictMemoryGone
					}
					return err
				}
				if !memory.Active || memory.ForgottenAt != nil {
					return ErrGroupAssistantConflictMemoryGone
				}
				if !memory.ExpiresAt.After(time.Now().UTC()) {
					return ErrGroupAssistantConflictMemoryGone
				}
				if arg.ExpectedMemoryVersion > 0 && memory.Version != arg.ExpectedMemoryVersion {
					return ErrGroupAssistantConflictMemoryChanged
				}
				manual := arg.ResolutionMode == "admin_explicit_correction"
				if !manual && groupAssistantAuthorityRank(current.CandidateAuthority) < groupAssistantAuthorityRank(memory.AuthorityLevel) {
					return ErrGroupAssistantConflictAuthority
				}

				authority := current.CandidateAuthority
				memoryType := "learned"
				if authority == "pinned_announcement" || authority == "admin_explicit" {
					memoryType = "base"
				}
				sourceType := current.SourceType
				sourceMessageID := current.SourceMessageID
				sourceChatID := current.SourceChatID
				sourceOperatorID := memory.SourceOperatorID
				sourceOperatorName := memory.SourceOperatorName
				sourceCreatedAt := memory.SourceCreatedAt
				sourceVerified := memory.SourceVerified
				operatorID := arg.ActorID
				if manual {
					// The candidate source remains the candidate source. The
					// administrator confirmation is recorded separately in the
					// operator fields and explicit source type.
					authority = "admin_explicit"
					memoryType = "base"
					sourceType = "admin_conflict_accept"
					sourceOperatorID = &operatorID
					sourceOperatorName = arg.ActorName
					now := time.Now().UTC()
					sourceCreatedAt = &now
					sourceVerified = "verified"
				}
				version := memory.Version
				if arg.ExpectedMemoryVersion > 0 {
					version = arg.ExpectedMemoryVersion
				}
				memory, err = scanGroupAssistantMemory(tx.db.QueryRow(ctx, `UPDATE group_assistant_memories SET
					content=$2, valid_scope=$3, memory_type=$4, authority_level=$5, source_type=$6,
					source_message_id=$7, source_chat_id=$8, source_operator_id=$9, source_operator_name=$10,
					source_snippet=$11, source_created_at=$12, source_verified=$13, source_content_hash=$14,
					dedupe_hash=CASE WHEN $15 <> '' THEN $15 ELSE dedupe_hash END,
					version=version+1, updated_at=NOW()
					WHERE id=$1 AND chat_id=$16 AND version=$17 AND active=TRUE AND forgotten_at IS NULL AND expires_at>NOW()
					RETURNING `+groupAssistantMemoryColumns, *current.MemoryID, current.CandidateContent, current.CandidateScope,
					memoryType, authority, sourceType, sourceMessageID, sourceChatID, sourceOperatorID,
					sourceOperatorName, current.SourceSnippet, sourceCreatedAt, sourceVerified, "",
					arg.DedupeHash, arg.ChatID, version))
				if err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						return ErrGroupAssistantConflictMemoryChanged
					}
					return err
				}
				if err = tx.insertGroupAssistantMemoryVersion(ctx, memory, &arg.ActorID, "conflict_accept"); err != nil {
					return err
				}
			}
		}
		out, err = scanGroupAssistantConflict(tx.db.QueryRow(ctx, `UPDATE group_assistant_conflicts SET status=$3,
			resolved_by=$4,resolved_at=NOW() WHERE id=$1 AND chat_id=$2
			RETURNING id,chat_id,memory_id,subject,candidate_content,candidate_scope,candidate_authority,source_type,
			source_message_id,source_chat_id,source_snippet,status,resolved_by,resolved_at,created_at`, arg.ID, arg.ChatID, status, arg.ActorID))
		return err
	})
	return out, err
}

func (q *Queries) InsertGroupAssistantDispatch(ctx context.Context, arg CreateGroupAssistantDispatchParams) (GroupAssistantDispatch, error) {
	var v GroupAssistantDispatch
	err := q.db.QueryRow(ctx, `INSERT INTO group_assistant_dispatches
	(chat_id,request_id,task_type,endpoint_id,model_ref,reason,status,error_text,latency_ms)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	RETURNING id,chat_id,request_id,task_type,endpoint_id,model_ref,reason,status,error_text,latency_ms,created_at`,
		arg.ChatID, arg.RequestID, arg.TaskType, arg.EndpointID, arg.ModelRef, arg.Reason, arg.Status, arg.ErrorText, arg.LatencyMs).Scan(
		&v.ID, &v.ChatID, &v.RequestID, &v.TaskType, &v.EndpointID, &v.ModelRef, &v.Reason, &v.Status, &v.ErrorText, &v.LatencyMs, &v.CreatedAt)
	return v, err
}

func (q *Queries) ListGroupAssistantDispatches(ctx context.Context, chatID int64, limit int32) ([]GroupAssistantDispatch, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := q.db.Query(ctx, `SELECT id,chat_id,request_id,task_type,endpoint_id,model_ref,reason,status,error_text,latency_ms,created_at
	FROM group_assistant_dispatches WHERE chat_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2`, chatID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]GroupAssistantDispatch, 0)
	for rows.Next() {
		var v GroupAssistantDispatch
		if err := rows.Scan(&v.ID, &v.ChatID, &v.RequestID, &v.TaskType, &v.EndpointID, &v.ModelRef, &v.Reason,
			&v.Status, &v.ErrorText, &v.LatencyMs, &v.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}

func (q *Queries) PruneGroupAssistantMessages(ctx context.Context) error {
	_, err := q.db.Exec(ctx, `DELETE FROM group_assistant_messages WHERE expires_at <= NOW()`)
	return err
}

// itoa is kept local to this hand-written query file to avoid introducing a
// query-builder dependency into the store package.
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
