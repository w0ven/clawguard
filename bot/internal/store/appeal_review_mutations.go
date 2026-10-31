package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// LockAppealReview must be called within a transaction and enforces scope at the row lock.
func (q *Queries) LockAppealReview(ctx context.Context, id int64, global bool, scope []int64) (AppealReview, error) {
	return scanAppealReview(q.db.QueryRow(ctx, appealReviewSelect+` WHERE a.id=$1 AND ($2 OR a.chat_id=ANY($3::bigint[])) FOR UPDATE OF a`, id, global, scope))
}
func (q *Queries) SetAppealDecision(ctx context.Context, id, admin int64, decision, note string) error {
	status := "executing"
	result := "审核通过，正在确认原处理"
	if decision == "uphold" {
		status = "upheld"
		result = "审核后维持原处理"
	}
	tag, err := q.db.Exec(ctx, `UPDATE appeals SET decision=$2,public_note=$3,reviewer_id=$4,decision_at=now(),status=$5,execution_result=$6,review_version=review_version+1,updated_at=now() WHERE id=$1 AND status='pending' AND decision IS NULL`, id, decision, note, admin, status, result)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}
func (q *Queries) SetAppealExecutionResult(ctx context.Context, id int64, status, result string) error {
	switch status {
	case "completed", "manual_review", "failed", "executing":
	default:
		return fmt.Errorf("invalid review status")
	}
	tag, err := q.db.Exec(ctx, `UPDATE appeals SET status=$2,execution_result=$3,review_version=review_version+1,updated_at=now() WHERE id=$1 AND decision='approve' AND status IN ('executing','failed')`, id, status, result)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}
func (q *Queries) ConsumeAppealWarning(ctx context.Context, appeal, warning, chat, user int64) error {
	return q.Transact(ctx, func(tx *Queries) error {
		var found bool
		if err := tx.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM appeal_warning_reversals WHERE appeal_id=$1 AND warning_id=$2)`, appeal, warning).Scan(&found); err != nil {
			return err
		}
		if found {
			return nil
		}
		tag, err := tx.db.Exec(ctx, `UPDATE warnings SET consumed_at=now() WHERE id=$1 AND chat_id=$2 AND user_id=$3 AND consumed_at IS NULL`, warning, chat, user)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return pgx.ErrNoRows
		}
		_, err = tx.db.Exec(ctx, `INSERT INTO appeal_warning_reversals(appeal_id,warning_id) VALUES($1,$2)`, appeal, warning)
		return err
	})
}
func (q *Queries) QueueAppealResult(ctx context.Context, id int64) error {
	_, err := q.db.Exec(ctx, `INSERT INTO moderation_notifications(case_id,chat_id,user_id,state,session_epoch,event_kind,appeal_id,event_version,event_text)
 SELECT a.case_id,a.chat_id,a.user_id,CASE WHEN s.is_reachable THEN 'pending' ELSE 'unreachable' END,s.notification_epoch,'appeal_result',a.id,a.review_version,
 '申诉审核结果 · APL-'||a.id::text||E'\n关联处理：CASE-'||a.case_id::text||E'\n审核结果：'||a.execution_result||E'\n处理说明：'||a.public_note
 FROM appeals a LEFT JOIN telegram_private_sessions s ON s.user_id=a.user_id WHERE a.id=$1 AND a.status IN ('upheld','completed','manual_review') ON CONFLICT DO NOTHING`, id)
	return err
}
