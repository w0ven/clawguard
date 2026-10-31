package store

import "context"

type AIMuteOwner struct {
	DecisionID, Until int64
	Token, State      string
}
type AIMuteClaim struct {
	Token, State string
	LeaseActive  bool
}

func (q *Queries) ReserveAIMute(ctx context.Context, chat, user, decision, until int64, token string) error {
	_, err := q.db.Exec(ctx, `WITH dual AS (UPDATE dual_captcha_claims AS claim SET state='conflict',updated_at=now() FROM pending_verifications AS pending WHERE claim.pending_id=pending.id AND pending.chat_id=$1 AND pending.user_id=$2 AND claim.state IN ('validating','verified','applying')), old AS (UPDATE ai_mute_review_claims SET state='conflict' WHERE decision_id IN (SELECT decision_id FROM ai_mute_ownership WHERE chat_id=$1 AND user_id=$2) AND state IN ('claimed','telegram_done')) INSERT INTO ai_mute_ownership(chat_id,user_id,decision_id,until_date,token,state) VALUES($1,$2,$3,$4,$5,'pending') ON CONFLICT(chat_id,user_id) DO UPDATE SET decision_id=excluded.decision_id,until_date=excluded.until_date,token=excluded.token,state='pending',lease_until=NULL`, chat, user, decision, until, token)
	return err
}
func (q *Queries) InvalidateAIMute(ctx context.Context, chat, user int64) error {
	_, err := q.db.Exec(ctx, `WITH old AS (UPDATE ai_mute_review_claims SET state='conflict' WHERE decision_id IN (SELECT decision_id FROM ai_mute_ownership WHERE chat_id=$1 AND user_id=$2) AND state IN ('claimed','telegram_done')) UPDATE ai_mute_ownership SET state='conflict' WHERE chat_id=$1 AND user_id=$2 AND state IN ('pending','active','reviewing')`, chat, user)
	return err
}
func (q *Queries) ActivateAIMute(ctx context.Context, chat, user int64, token string) (bool, error) {
	tag, err := q.db.Exec(ctx, `UPDATE ai_mute_ownership SET state='active' WHERE chat_id=$1 AND user_id=$2 AND token=$3 AND state='pending'`, chat, user, token)
	return tag.RowsAffected() == 1, err
}
func (q *Queries) LockAIMuteOwner(ctx context.Context, chat, user int64) (AIMuteOwner, error) {
	var v AIMuteOwner
	err := q.db.QueryRow(ctx, `SELECT decision_id,until_date,token,state FROM ai_mute_ownership WHERE chat_id=$1 AND user_id=$2 FOR UPDATE`, chat, user).Scan(&v.DecisionID, &v.Until, &v.Token, &v.State)
	return v, err
}
func (q *Queries) LockAIMuteClaim(ctx context.Context, id int64) (AIMuteClaim, error) {
	var v AIMuteClaim
	err := q.db.QueryRow(ctx, `SELECT token,state,lease_until>now() FROM ai_mute_review_claims WHERE decision_id=$1 FOR UPDATE`, id).Scan(&v.Token, &v.State, &v.LeaseActive)
	return v, err
}
func (q *Queries) ClaimAIMuteReview(ctx context.Context, chat, user, id int64, token string) error {
	_, err := q.db.Exec(ctx, `INSERT INTO ai_mute_review_claims(decision_id,token,state,lease_until) VALUES($1,$2,'claimed',now()+interval '5 minutes') ON CONFLICT(decision_id) DO UPDATE SET token=excluded.token,state='claimed',lease_until=excluded.lease_until`, id, token)
	if err != nil {
		return err
	}
	_, err = q.db.Exec(ctx, `UPDATE ai_mute_ownership SET state='reviewing',lease_until=now()+interval '5 minutes' WHERE chat_id=$1 AND user_id=$2 AND decision_id=$3 AND state IN ('active','reviewing')`, chat, user, id)
	return err
}
func (q *Queries) UpdateAIMuteClaim(ctx context.Context, id int64, token, state string) (bool, error) {
	tag, err := q.db.Exec(ctx, `UPDATE ai_mute_review_claims SET state=$3 WHERE decision_id=$1 AND token=$2 AND state IN ('claimed','telegram_done')`, id, token, state)
	return tag.RowsAffected() == 1, err
}
func (q *Queries) SetAIMuteOwnerState(ctx context.Context, chat, user, id int64, state string) (bool, error) {
	tag, err := q.db.Exec(ctx, `UPDATE ai_mute_ownership SET state=$4 WHERE chat_id=$1 AND user_id=$2 AND decision_id=$3 AND state='reviewing'`, chat, user, id, state)
	return tag.RowsAffected() == 1, err
}
func (q *Queries) ReleaseAIMuteClaim(ctx context.Context, id int64, token string) error {
	_, err := q.db.Exec(ctx, `UPDATE ai_mute_review_claims SET lease_until=now()-interval '1 second' WHERE decision_id=$1 AND token=$2 AND state='claimed'`, id, token)
	return err
}
