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
	_, err := q.db.Exec(ctx, `WITH cleared AS (UPDATE user_trust SET banned_reason=banned_reason - 'decision_id' WHERE chat_id=$1 AND user_id=$2 AND banned_reason ? 'decision_id' RETURNING chat_id,user_id), invalidated_mute_claim AS (UPDATE ai_mute_review_claims SET state='conflict' WHERE decision_id IN (SELECT decision_id FROM ai_mute_ownership WHERE chat_id=$1 AND user_id=$2) AND state IN ('claimed','telegram_done')), invalidated_mute AS (UPDATE ai_mute_ownership SET state='conflict' WHERE chat_id=$1 AND user_id=$2 AND state IN ('pending','active','reviewing')), invalidated AS (UPDATE user_trust_sync SET state='conflict' WHERE chat_id=$1 AND user_id=$2 AND target_status<>'banned' AND state IN ('claimed','retry')) UPDATE ai_unban_review_claims SET state='conflict',updated_at=now() WHERE state IN ('claimed','telegram_done') AND decision_id IN (SELECT d.id FROM ai_decisions d JOIN cleared c ON d.chat_id=c.chat_id AND d.user_id=c.user_id)`, chatID, userID)
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

type AIReviewClaim struct {
	Token, State string
	LeaseActive  bool
}

func (q *Queries) GetAIReviewClaim(ctx context.Context, id int64) (AIReviewClaim, error) {
	var v AIReviewClaim
	err := q.db.QueryRow(ctx, `SELECT token,state,lease_until>now() FROM ai_unban_review_claims WHERE decision_id=$1 FOR UPDATE`, id).Scan(&v.Token, &v.State, &v.LeaseActive)
	return v, err
}
func (q *Queries) ClaimAIReview(ctx context.Context, id int64, token string) error {
	_, err := q.db.Exec(ctx, `INSERT INTO ai_unban_review_claims(decision_id,token,state,lease_until) VALUES($1,$2,'claimed',now()+interval '2 minutes') ON CONFLICT(decision_id) DO UPDATE SET token=excluded.token,state='claimed',lease_until=excluded.lease_until,updated_at=now()`, id, token)
	return err
}
func (q *Queries) SetAIReviewClaimState(ctx context.Context, id int64, token, state string) (bool, error) {
	tag, err := q.db.Exec(ctx, `UPDATE ai_unban_review_claims SET state=$3,updated_at=now() WHERE decision_id=$1 AND token=$2 AND state IN ('claimed','telegram_done')`, id, token, state)
	return tag.RowsAffected() == 1, err
}
func (q *Queries) DeleteAIReviewClaim(ctx context.Context, id int64, token string) error {
	_, err := q.db.Exec(ctx, `DELETE FROM ai_unban_review_claims WHERE decision_id=$1 AND token=$2 AND state='claimed'`, id, token)
	return err
}
func (q *Queries) HasFalsePositiveReview(ctx context.Context, id int64) (bool, error) {
	var found bool
	err := q.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_false_positive_reviews WHERE decision_id=$1)`, id).Scan(&found)
	return found, err
}
