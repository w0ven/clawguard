package store

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
)

var ErrCleanupDispatchConflict = errors.New("cleanup dispatch subject or operation conflict")

// BeginCleanupDispatch persists before sending. Existing dispatched entries
// must never be replayed, even after the worker lease expires or a restart.
func (q *Queries) BeginCleanupDispatch(ctx context.Context, key string, chatID, userID int64, action string) (bool, string, error) {
	var claimed bool
	err := q.db.QueryRow(ctx, `INSERT INTO verification_cleanup_dispatch(operation_key,chat_id,user_id,action,state) VALUES($1,$2,$3,$4,'dispatched') ON CONFLICT DO NOTHING RETURNING true`, key, chatID, userID, action).Scan(&claimed)
	if err == nil {
		return true, "", nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, "", fmt.Errorf("claim cleanup dispatch: %w", err)
	}
	var state string
	var result *string
	e := q.db.QueryRow(ctx, `SELECT state,result FROM verification_cleanup_dispatch WHERE operation_key=$1 AND chat_id=$2 AND user_id=$3 AND action=$4`, key, chatID, userID, action).Scan(&state, &result)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, "", ErrCleanupDispatchConflict
	}
	if e != nil {
		return false, "", fmt.Errorf("load cleanup dispatch: %w", e)
	}
	if state == "completed" && result != nil {
		return false, *result, nil
	}
	return false, "", nil
}

// LockVerificationStartNotice serializes delivery for one join generation in
// the caller's transaction. Rollback leaves a failed send eligible for retry.
func (q *Queries) LockVerificationStartNotice(ctx context.Context, key string, chatID, userID int64) (bool, error) {
	var state string
	err := q.db.QueryRow(ctx, `INSERT INTO verification_cleanup_dispatch(operation_key,chat_id,user_id,action,state)
VALUES($1,$2,$3,'verification_start_notice','dispatched')
ON CONFLICT(operation_key) DO UPDATE SET operation_key=EXCLUDED.operation_key
WHERE verification_cleanup_dispatch.chat_id=EXCLUDED.chat_id AND verification_cleanup_dispatch.user_id=EXCLUDED.user_id AND verification_cleanup_dispatch.action=EXCLUDED.action
RETURNING state`, key, chatID, userID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrCleanupDispatchConflict
	}
	return state == "completed", err
}

func (q *Queries) HasPendingVerificationStartDispatch(ctx context.Context, key string, chatID, userID int64) (bool, error) {
	var exists bool
	err := q.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM verification_cleanup_dispatch WHERE operation_key=$1 AND chat_id=$2 AND user_id=$3 AND action='verification_start' AND (state='dispatched' OR (state='completed' AND result IN ('verification_start_not_sent_manual','verification_start_membership_changed_manual'))))`, key, chatID, userID).Scan(&exists)
	return exists, err
}

// A restriction confirmed after the membership session changed cannot be
// attributed to the old join, and must not resume a verification prompt.
func (q *Queries) MarkVerificationStartMembershipConflict(ctx context.Context, key string, chatID, userID int64) error {
	tag, err := q.db.Exec(ctx, `UPDATE verification_cleanup_dispatch SET result='verification_start_membership_changed_manual',updated_at=now() WHERE operation_key=$1 AND chat_id=$2 AND user_id=$3 AND action='verification_start' AND state='completed' AND result='restricted'`, key, chatID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("verification start result is not confirmed restricted")
	}
	return nil
}

func (q *Queries) CleanupDispatchState(ctx context.Context, key string) (string, string, error) {
	var state string
	var result *string
	err := q.db.QueryRow(ctx, `SELECT state,result FROM verification_cleanup_dispatch WHERE operation_key=$1`, key).Scan(&state, &result)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	if result != nil {
		return state, *result, nil
	}
	return state, "", nil
}
func (q *Queries) CompleteCleanupDispatch(ctx context.Context, key, result string) error {
	tag, err := q.db.Exec(ctx, `UPDATE verification_cleanup_dispatch SET state='completed',result=$2,updated_at=now() WHERE operation_key=$1 AND state='dispatched'`, key, result)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("cleanup dispatch is not active")
	}
	return nil
}

// Clear only after an explicit Telegram rejection, or after a confirmed
// fallback restriction which is not the final deferred cleanup action.
func (q *Queries) ClearConfirmedFallbackDispatch(ctx context.Context, key string) error {
	tag, err := q.db.Exec(ctx, `DELETE FROM verification_cleanup_dispatch WHERE operation_key=$1 AND state='completed' AND result='fallback_mute'`, key)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("fallback dispatch not confirmed")
	}
	return nil
}
func (q *Queries) ClearCleanupDispatch(ctx context.Context, key string) error {
	_, err := q.db.Exec(ctx, `DELETE FROM verification_cleanup_dispatch WHERE operation_key=$1 AND state='dispatched'`, key)
	return err
}
