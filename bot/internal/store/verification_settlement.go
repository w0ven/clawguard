package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// VerificationCleanupSettlement is a durable record of a completed external
// action. PendingID identifies the particular verification, not the user.
type VerificationCleanupSettlement struct {
	Key                  string
	PendingID            *int64
	ChatID, UserID       int64
	Username             *string
	Rule                 string
	Matched              *string
	Action               string
	NotifyRedis          bool
	MembershipGeneration string
	ActionUntil          time.Time
}

func (q *Queries) RecordVerificationCleanup(ctx context.Context, v VerificationCleanupSettlement) (bool, error) {
	inserted := false
	err := q.Transact(ctx, func(tx *Queries) error {
		tag, err := tx.db.Exec(ctx, `INSERT INTO verification_cleanup_settlements(operation_key,pending_id,chat_id,user_id,username,rule,matched,action)
        VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(operation_key) DO NOTHING`, v.Key, v.PendingID, v.ChatID, v.UserID, v.Username, v.Rule, v.Matched, v.Action)
		if err != nil {
			return err
		}
		inserted = tag.RowsAffected() == 1
		// Only explicitly versioned new Redis tasks may emit notifications.
		action, result := v.Action, "executed"
		switch action {
		case "partial_kick":
			action, result = "kick", "partial"
		case "temporary_ban":
			action = "ban"
		case "temporary_restrict":
			action = "mute"
		}
		if !inserted || (strings.HasPrefix(v.Key, "redis:") && !v.NotifyRedis) || (action != "kick" && action != "ban" && action != "mute") {
			return nil
		}
		ownership, _ := json.Marshal(map[string]any{"cleanup_key": v.Key, "pending_id": v.PendingID, "membership_generation": v.MembershipGeneration, "original_action": v.Action, "action_until": v.ActionUntil, "automatic_reversal": "manual_review"})
		explanation := "入群保护处理"
		if v.Rule == "verify_timeout" {
			explanation = "入群验证超时"
		}
		if result == "partial" {
			explanation = "已移出群聊，重新入群权限尚待管理员确认"
		}
		_, err = tx.RecordModerationCase(ctx, ModerationCaseInput{SourceKey: "verification_cleanup:" + v.Key, SourceKind: "verification_cleanup", SourceID: v.Key, ChatID: v.ChatID, UserID: v.UserID, Action: action, Result: result, Explanation: explanation, Ownership: ownership})
		return err
	})
	return inserted, err
}

func (q *Queries) PendingVerificationCleanupExists(ctx context.Context, key string) (bool, error) {
	var found bool
	err := q.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM verification_cleanup_settlements WHERE operation_key=$1)`, key).Scan(&found)
	return found, err
}

func (q *Queries) VerificationPendingCurrent(ctx context.Context, p PendingVerification) (bool, error) {
	var found bool
	err := q.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pending_verifications WHERE id=$1 AND ($2::text IS NULL OR (lease_owner=$2 AND lease_until>now())))`, p.ID, p.LeaseOwner).Scan(&found)
	return found, err
}

func (q *Queries) DeletePendingVerificationInstance(ctx context.Context, p PendingVerification) (int64, error) {
	tag, err := q.db.Exec(ctx, `DELETE FROM pending_verifications WHERE id=$1 AND ($2::text IS NULL OR lease_owner=$2)`, p.ID, p.LeaseOwner)
	return tag.RowsAffected(), err
}

// SettleVerificationCleanup inserts the log and deletes only the old instance
// in one short transaction. Row locking makes concurrent retries idempotent.
func (q *Queries) SettleVerificationCleanup(ctx context.Context, key string) (bool, int64, error) {
	var did bool
	var deleted int64
	err := q.Transact(ctx, func(tx *Queries) error {
		var v VerificationCleanupSettlement
		var settled bool
		err := tx.db.QueryRow(ctx, `SELECT pending_id,chat_id,user_id,username,rule,matched,action,settled FROM verification_cleanup_settlements WHERE operation_key=$1 FOR UPDATE`, key).Scan(&v.PendingID, &v.ChatID, &v.UserID, &v.Username, &v.Rule, &v.Matched, &v.Action, &settled)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if settled {
			return nil
		}
		if _, err = tx.InsertViolation(ctx, InsertViolationParams{ChatID: v.ChatID, UserID: v.UserID, Username: v.Username, Rule: v.Rule, Matched: v.Matched, Action: v.Action}); err != nil {
			return fmt.Errorf("insert cleanup violation: %w", err)
		}
		if v.PendingID != nil {
			tag, e := tx.db.Exec(ctx, `DELETE FROM pending_verifications WHERE id=$1`, *v.PendingID)
			if e != nil {
				return e
			}
			deleted = tag.RowsAffected()
		}
		if _, err = tx.db.Exec(ctx, `UPDATE verification_cleanup_settlements SET settled=true, settled_at=now() WHERE operation_key=$1`, key); err != nil {
			return err
		}
		did = true
		return nil
	})
	return did, deleted, err
}

func (q *Queries) UnsettledVerificationCleanups(ctx context.Context, limit int32) ([]string, error) {
	rows, err := q.db.Query(ctx, `SELECT operation_key FROM verification_cleanup_settlements WHERE NOT settled AND next_attempt_at<=now() ORDER BY next_attempt_at,created_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// Back off a failed row so it cannot indefinitely starve other settlements.
func (q *Queries) RescheduleVerificationCleanup(ctx context.Context, key string) error {
	_, err := q.db.Exec(ctx, `UPDATE verification_cleanup_settlements SET next_attempt_at=now()+interval '1 minute' WHERE operation_key=$1 AND NOT settled`, key)
	return err
}

// InsertWarningForModeration binds a stable event to the exact warning in the
// same transaction; no time-based matching is used during retries.
func (q *Queries) InsertWarningForModeration(ctx context.Context, key string, p InsertWarningParams) (int64, error) {
	if key == "" {
		w, err := q.InsertWarning(ctx, p)
		return w.ID, err
	}
	var id int64
	err := q.Transact(ctx, func(tx *Queries) error {
		if _, e := tx.db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('warning_event:' || $1,0))`, key); e != nil {
			return e
		}
		e := tx.db.QueryRow(ctx, `SELECT warning_id FROM moderation_warning_events WHERE event_key=$1 AND chat_id=$2 AND user_id=$3`, key, p.ChatID, p.UserID).Scan(&id)
		if e == nil {
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		w, e := tx.InsertWarning(ctx, p)
		if e != nil {
			return e
		}
		id = w.ID
		_, e = tx.db.Exec(ctx, `INSERT INTO moderation_warning_events(event_key,warning_id,chat_id,user_id) VALUES($1,$2,$3,$4)`, key, id, p.ChatID, p.UserID)
		return e
	})
	return id, err
}
func (q *Queries) WarningForModerationEvent(ctx context.Context, key string, chatID, userID int64) (int64, error) {
	var id int64
	err := q.db.QueryRow(ctx, `SELECT warning_id FROM moderation_warning_events WHERE event_key=$1 AND chat_id=$2 AND user_id=$3`, key, chatID, userID).Scan(&id)
	return id, err
}

func (q *Queries) VerificationPartialRequiresReview(ctx context.Context, pendingID int64) (bool, error) {
	var exists bool
	err := q.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM moderation_cases WHERE source_key=$1 AND result='partial')`, fmt.Sprintf("verification_immediate:pending:%d", pendingID)).Scan(&exists)
	return exists, err
}

// A claimed escalation is never blindly replayed after an uncertain Telegram result.
func (q *Queries) ClaimWarningEscalation(ctx context.Context, warningID int64, action string) (bool, error) {
	tag, err := q.db.Exec(ctx, `INSERT INTO moderation_warning_escalations(warning_id,action,state) VALUES($1,$2,'claimed') ON CONFLICT(warning_id) DO NOTHING`, warningID, action)
	return tag.RowsAffected() == 1, err
}
func (q *Queries) CompleteWarningEscalation(ctx context.Context, warningID int64) error {
	_, err := q.db.Exec(ctx, `UPDATE moderation_warning_escalations SET state='completed' WHERE warning_id=$1 AND state='claimed'`, warningID)
	return err
}

func (q *Queries) ModerationCaseExists(ctx context.Context, key string) (bool, error) {
	var exists bool
	err := q.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM moderation_cases WHERE source_key=$1)`, key).Scan(&exists)
	return exists, err
}

func (q *Queries) ModerationCasePartial(ctx context.Context, key string) (bool, error) {
	var exists bool
	err := q.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM moderation_cases WHERE source_key=$1 AND result='partial')`, key).Scan(&exists)
	return exists, err
}
