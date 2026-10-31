package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ClaimScheduledDispatch durably reserves a single automatic slot. Manual
// invocations have no slot and each represents a separate user intention.
func (q *Queries) ClaimScheduledDispatch(ctx context.Context, id, chatID int64, slot *time.Time) (int64, bool, error) {
	var attempt int64
	err := q.db.QueryRow(ctx, `INSERT INTO scheduled_dispatch_attempts (scheduled_message_id,chat_id,scheduled_at,state) VALUES ($1,$2,$3,'dispatching') ON CONFLICT DO NOTHING RETURNING id`, id, chatID, slot).Scan(&attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return attempt, err == nil, err
}

// FinishScheduledDispatch commits the confirmed Telegram ID and its deletion
// together. If commit fails, dispatching remains and is never replayed.
func (q *Queries) FinishScheduledDispatch(ctx context.Context, attempt, chatID int64, messageID *int64, deleteSeconds int32, state, detail string) error {
	return q.Transact(ctx, func(tx *Queries) error {
		tag, err := tx.db.Exec(ctx, `UPDATE scheduled_dispatch_attempts SET state=$2,telegram_message_id=$3,error=NULLIF($4,''),updated_at=now() WHERE id=$1 AND chat_id=$5 AND state='dispatching'`, attempt, state, messageID, detail, chatID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("dispatch attempt no longer dispatching")
		}
		if state == "sent" && messageID != nil && deleteSeconds > 0 {
			_, err = tx.db.Exec(ctx, `INSERT INTO scheduled_delete_jobs(attempt_id,chat_id,telegram_message_id,delete_at) VALUES($1,$2,$3,now()+($4 * interval '1 second'))`, attempt, chatID, *messageID, deleteSeconds)
		}
		return err
	})
}

type ScheduledDeleteJob struct {
	ID, ChatID, MessageID int64
	Attempts              int32
	Token                 string
}

// Claim one job at a time, fencing completion by a random lease token. A
// crashed final attempt becomes failed instead of being retried indefinitely.
func (q *Queries) ClaimScheduledDelete(ctx context.Context, token string) (*ScheduledDeleteJob, error) {
	if _, err := q.db.Exec(ctx, `UPDATE scheduled_delete_jobs SET state='failed',lease_token=NULL,lease_until=NULL,last_error='lease expired after final attempt',updated_at=now() WHERE state='leased' AND lease_until<=now() AND attempts>=5`); err != nil {
		return nil, err
	}
	var j ScheduledDeleteJob
	err := q.db.QueryRow(ctx, `WITH candidate AS (SELECT id FROM scheduled_delete_jobs WHERE delete_at<=now() AND attempts<5 AND (state='pending' OR (state='leased' AND lease_until<=now())) ORDER BY delete_at,id FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE scheduled_delete_jobs d SET state='leased', attempts=d.attempts+1,lease_token=$1,lease_until=now()+interval '3 minutes',updated_at=now() FROM candidate c WHERE d.id=c.id RETURNING d.id,d.chat_id,d.telegram_message_id,d.attempts`, token).Scan(&j.ID, &j.ChatID, &j.MessageID, &j.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j.Token = token
	return &j, nil
}

// ReleaseScheduledDeleteBeforeCall returns a claimed job without consuming an
// attempt only when the caller has not started any Telegram request.
func (q *Queries) ReleaseScheduledDeleteBeforeCall(ctx context.Context, j ScheduledDeleteJob) error {
	tag, err := q.db.Exec(ctx, `UPDATE scheduled_delete_jobs SET state='pending', attempts=attempts-1, lease_token=NULL, lease_until=NULL,updated_at=now() WHERE id=$1 AND state='leased' AND lease_token=$2 AND lease_until>now()`, j.ID, j.Token)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("delete lease lost")
	}
	return nil
}

func (q *Queries) SettleScheduledDelete(ctx context.Context, j ScheduledDeleteJob, success, permanent bool, detail string) error {
	tag, err := q.db.Exec(ctx, `UPDATE scheduled_delete_jobs SET state=CASE WHEN $3 THEN 'done' WHEN $4 OR attempts>=5 THEN 'failed' ELSE 'pending' END,delete_at=CASE WHEN $3 OR $4 OR attempts>=5 THEN delete_at ELSE now()+interval '30 seconds' * attempts END,lease_token=NULL,lease_until=NULL,last_error=NULLIF($5,''),updated_at=now() WHERE id=$1 AND state='leased' AND lease_token=$2 AND lease_until>now()`, j.ID, j.Token, success, permanent, detail)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("delete lease lost")
	}
	return nil
}
