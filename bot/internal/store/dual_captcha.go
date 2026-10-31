package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrDualCaptchaBusy = errors.New("verification already in progress")
var ErrDualCaptchaConflict = errors.New("verification requires manual review")

func (q *Queries) DualCaptchaPassed(ctx context.Context, pendingID int64) (bool, error) {
	var passed bool
	err := q.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM dual_captcha_claims WHERE pending_id=$1 AND state IN ('verified','applying','conflict','completed'))`, pendingID).Scan(&passed)
	return passed, err
}

func (q *Queries) GetActiveDualCaptcha(ctx context.Context, token string) (PendingVerification, error) {
	row := q.db.QueryRow(ctx, `SELECT id,chat_id,user_id,username,first_name,method,payload,join_message_id,expires_at,created_at,next_attempt_at,lease_until,lease_owner,attempt_count,last_error FROM pending_verifications WHERE method='turnstile_hcaptcha' AND payload->>'token'=$1 AND (expires_at>now() OR EXISTS (SELECT 1 FROM dual_captcha_claims WHERE pending_id=pending_verifications.id AND state IN ('verified','applying','conflict')))`, token)
	return scanPendingVerification(row)
}

// ClaimDualCaptcha atomically leases the current active instance, sharing the
// pending lease with the expiry worker. No transaction is held over network IO.
func (q *Queries) ClaimDualCaptcha(ctx context.Context, pendingToken, claimToken string) (PendingVerification, string, error) {
	var pending PendingVerification
	var state string
	err := q.Transact(ctx, func(tx *Queries) error {
		row := tx.db.QueryRow(ctx, `SELECT id,chat_id,user_id,username,first_name,method,payload,join_message_id,expires_at,created_at,next_attempt_at,lease_until,lease_owner,attempt_count,last_error FROM pending_verifications WHERE method='turnstile_hcaptcha' AND payload->>'token'=$1 AND (expires_at>now() OR EXISTS (SELECT 1 FROM dual_captcha_claims WHERE pending_id=pending_verifications.id AND state IN ('verified','applying','conflict'))) FOR UPDATE`, pendingToken)
		var err error
		pending, err = scanPendingVerification(row)
		if err != nil {
			return err
		}
		if pending.LeaseUntil != nil {
			var active bool
			if err = tx.db.QueryRow(ctx, `SELECT $1::timestamptz > now()`, *pending.LeaseUntil).Scan(&active); err != nil {
				return err
			}
			if active {
				return ErrDualCaptchaBusy
			}
		}
		var existing string
		err = tx.db.QueryRow(ctx, `SELECT state FROM dual_captcha_claims WHERE pending_id=$1 FOR UPDATE`, pending.ID).Scan(&existing)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if existing == "conflict" {
			return ErrDualCaptchaConflict
		}
		if existing == "completed" {
			return ErrDualCaptchaBusy
		}
		state = "validating"
		if existing == "verified" || existing == "applying" {
			state = existing
		}
		_, err = tx.db.Exec(ctx, `INSERT INTO dual_captcha_claims(pending_id,token,state,lease_until) VALUES($1,$2,$3,now()+interval '5 minutes') ON CONFLICT(pending_id) DO UPDATE SET token=excluded.token,state=excluded.state,lease_until=excluded.lease_until,updated_at=now()`, pending.ID, claimToken, state)
		if err != nil {
			return err
		}
		_, err = tx.db.Exec(ctx, `UPDATE pending_verifications SET lease_owner=$2,lease_until=now()+interval '5 minutes' WHERE id=$1`, pending.ID, claimToken)
		return err
	})
	return pending, state, err
}

func (q *Queries) UpdateDualCaptchaClaim(ctx context.Context, id int64, claim, from, to string) (bool, error) {
	tag, err := q.db.Exec(ctx, `UPDATE dual_captcha_claims SET state=$4,updated_at=now() WHERE pending_id=$1 AND token=$2 AND state=$3 AND lease_until>now() AND EXISTS (SELECT 1 FROM pending_verifications WHERE id=$1 AND lease_owner=$2 AND lease_until>now() AND ($3<>'validating' OR expires_at>now()))`, id, claim, from, to)
	return tag.RowsAffected() == 1, err
}

func (q *Queries) BeginDualCaptchaApply(ctx context.Context, id int64, claim string, rights []byte) (bool, error) {
	tag, err := q.db.Exec(ctx, `UPDATE dual_captcha_claims SET state='applying',target_rights=$3,updated_at=now() WHERE pending_id=$1 AND token=$2 AND state='verified' AND lease_until>now() AND EXISTS (SELECT 1 FROM pending_verifications WHERE id=$1 AND lease_owner=$2 AND lease_until>now())`, id, claim, rights)
	return tag.RowsAffected() == 1, err
}

func (q *Queries) DualCaptchaTargetRights(ctx context.Context, id int64, claim string) ([]byte, error) {
	var rights []byte
	err := q.db.QueryRow(ctx, `SELECT target_rights FROM dual_captcha_claims WHERE pending_id=$1 AND token=$2 AND state='applying' AND lease_until>now()`, id, claim).Scan(&rights)
	return rights, err
}

func (q *Queries) MarkDualCaptchaConflict(ctx context.Context, id int64, claim string) error {
	_, err := q.db.Exec(ctx, `UPDATE dual_captcha_claims SET state='conflict', updated_at=now() WHERE pending_id=$1 AND token=$2 AND state IN ('verified','applying','conflict')`, id, claim)
	return err
}

func (q *Queries) ReleaseDualCaptchaClaim(ctx context.Context, id int64, claim string) error {
	return q.Transact(ctx, func(tx *Queries) error {
		if _, err := tx.db.Exec(ctx, `UPDATE dual_captcha_claims SET lease_until=now(),updated_at=now() WHERE pending_id=$1 AND token=$2 AND state IN ('validating','verified','applying')`, id, claim); err != nil {
			return err
		}
		_, err := tx.db.Exec(ctx, `UPDATE pending_verifications SET lease_until=NULL,lease_owner=NULL WHERE id=$1 AND lease_owner=$2`, id, claim)
		return err
	})
}

func (q *Queries) FinishDualCaptcha(ctx context.Context, id int64, claim string) (bool, error) {
	finished := false
	err := q.Transact(ctx, func(tx *Queries) error {
		tag, err := tx.db.Exec(ctx, `UPDATE dual_captcha_claims SET state='completed',updated_at=now() WHERE pending_id=$1 AND token=$2 AND state='applying' AND lease_until>now()`, id, claim)
		if err != nil || tag.RowsAffected() != 1 {
			return err
		}
		tag, err = tx.db.Exec(ctx, `DELETE FROM pending_verifications WHERE id=$1 AND lease_owner=$2 AND lease_until>now()`, id, claim)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrDualCaptchaBusy
		}
		finished = true
		return nil
	})
	return finished, err
}
