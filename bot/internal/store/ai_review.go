package store

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

func (q *Queries) LockAIDecisionReview(ctx context.Context, id int64) error {
	_, err := q.db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('ai_review:' || $1::bigint::text, 0))`, id)
	return err
}
func (q *Queries) LockAIBanOwnership(ctx context.Context, chatID, userID, id int64) (bool, string, error) {
	var owned bool
	var status string
	err := q.db.QueryRow(ctx, `SELECT status, status='banned' AND COALESCE(banned_reason->>'source','')='ai' AND COALESCE(banned_reason->>'decision_id','')=$3::bigint::text FROM user_trust WHERE chat_id=$1 AND user_id=$2 FOR UPDATE`, chatID, userID, id).Scan(&status, &owned)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", nil
	}
	return owned, status, err
}
func (q *Queries) ClearAIBanOwnership(ctx context.Context, chatID, userID int64) error {
	_, err := q.db.Exec(ctx, `UPDATE user_trust SET banned_reason=banned_reason - 'decision_id' WHERE chat_id=$1 AND user_id=$2 AND banned_reason ? 'decision_id'`, chatID, userID)
	return err
}
func (q *Queries) InsertFalsePositiveReview(ctx context.Context, id int64) (bool, error) {
	var inserted int64
	err := q.db.QueryRow(ctx, `INSERT INTO ai_false_positive_reviews(decision_id) VALUES($1) ON CONFLICT DO NOTHING RETURNING decision_id`, id).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
