package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// VerificationCleanupSettlement is a durable record of a completed external
// action. PendingID identifies the particular verification, not the user.
type VerificationCleanupSettlement struct {
	Key            string
	PendingID      *int64
	ChatID, UserID int64
	Username       *string
	Rule           string
	Matched        *string
	Action         string
}

func (q *Queries) RecordVerificationCleanup(ctx context.Context, v VerificationCleanupSettlement) (bool, error) {
	tag, err := q.db.Exec(ctx, `INSERT INTO verification_cleanup_settlements(operation_key,pending_id,chat_id,user_id,username,rule,matched,action)
        VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(operation_key) DO NOTHING`, v.Key, v.PendingID, v.ChatID, v.UserID, v.Username, v.Rule, v.Matched, v.Action)
	return tag.RowsAffected() == 1, err
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
