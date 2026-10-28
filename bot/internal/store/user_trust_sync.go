package store

import (
	"context"
	"time"
)

type TrustSync struct {
	Token, Target, Base, Action, State      string
	BaseChanged                             time.Time
	ActionDone, PermissionDone, LeaseActive bool
}

func (q *Queries) LockTrustForSync(ctx context.Context, chatID, userID int64) (UserTrust, error) {
	var u UserTrust
	err := q.db.QueryRow(ctx, getUserTrust+` FOR UPDATE`, chatID, userID).Scan(&u.ChatID, &u.UserID, &u.Username, &u.FirstName, &u.LastName, &u.JoinedAt, &u.UpdatedAt, &u.StatusChangedAt, &u.Status, &u.Score, &u.MessagesChecked, &u.MessagesClean, &u.GraduatedAt, &u.BannedAt, &u.BannedReason, &u.Notes, &u.IsBot)
	return u, err
}
func (q *Queries) GetTrustSync(ctx context.Context, chatID, userID int64) (TrustSync, error) {
	var v TrustSync
	err := q.db.QueryRow(ctx, `SELECT token,target_status,base_status,base_changed_at,action,action_done,permission_done,state,lease_until>now() FROM user_trust_sync WHERE chat_id=$1 AND user_id=$2 FOR UPDATE`, chatID, userID).Scan(&v.Token, &v.Target, &v.Base, &v.BaseChanged, &v.Action, &v.ActionDone, &v.PermissionDone, &v.State, &v.LeaseActive)
	return v, err
}
func (q *Queries) ClaimTrustSync(ctx context.Context, chatID, userID int64, v TrustSync) error {
	_, err := q.db.Exec(ctx, `INSERT INTO user_trust_sync(chat_id,user_id,token,target_status,base_status,base_changed_at,action,action_done,permission_done,state,lease_until) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'claimed',now()+interval '2 minutes') ON CONFLICT(chat_id,user_id) DO UPDATE SET token=EXCLUDED.token,target_status=EXCLUDED.target_status,base_status=EXCLUDED.base_status,base_changed_at=EXCLUDED.base_changed_at,action=EXCLUDED.action,action_done=EXCLUDED.action_done,permission_done=EXCLUDED.permission_done,state='claimed',lease_until=EXCLUDED.lease_until`, chatID, userID, v.Token, v.Target, v.Base, v.BaseChanged, v.Action, v.ActionDone, v.PermissionDone)
	return err
}
func (q *Queries) AdvanceTrustSync(ctx context.Context, chatID, userID int64, token, stage string) (bool, error) {
	tag, err := q.db.Exec(ctx, `UPDATE user_trust_sync SET action_done=action_done OR $4='action',permission_done=permission_done OR $4='permission',state=CASE WHEN $4='retry' THEN 'retry' WHEN $4='complete' THEN 'completed' WHEN $4='conflict' THEN 'conflict' ELSE state END WHERE chat_id=$1 AND user_id=$2 AND token=$3 AND state='claimed'`, chatID, userID, token, stage)
	return tag.RowsAffected() == 1, err
}
func (q *Queries) SetTrustStatusAfterSync(ctx context.Context, chatID, userID int64, status string, score float64, notes *string) (UserTrust, error) {
	_, err := q.db.Exec(ctx, `UPDATE user_trust SET status=$3,score=$4,notes=$5,status_changed_at=CASE WHEN status IS DISTINCT FROM $3 THEN now() ELSE status_changed_at END,graduated_at=CASE WHEN $3='trusted' THEN now() ELSE NULL END,banned_at=CASE WHEN $3='banned' THEN now() ELSE NULL END,banned_reason=CASE WHEN $3='banned' THEN jsonb_build_object('source','manual_admin','reason',COALESCE($5::text,'')) ELSE NULL END,updated_at=now() WHERE chat_id=$1 AND user_id=$2`, chatID, userID, status, score, notes)
	if err != nil {
		return UserTrust{}, err
	}
	return q.GetUserTrust(ctx, chatID, userID)
}

func (q *Queries) CompleteTrustSync(ctx context.Context, chatID, userID int64, token string, trust UserTrust) (bool, error) {
	tag, err := q.db.Exec(ctx, `UPDATE user_trust_sync SET state='completed',base_status=$4,base_changed_at=$5 WHERE chat_id=$1 AND user_id=$2 AND token=$3 AND state='claimed'`, chatID, userID, token, trust.Status, trust.StatusChangedAt)
	return tag.RowsAffected() == 1, err
}

func (q *Queries) ConflictTrustSync(ctx context.Context, chatID, userID int64, token string) error {
	_, err := q.db.Exec(ctx, `UPDATE user_trust_sync SET state='conflict' WHERE chat_id=$1 AND user_id=$2 AND token=$3 AND state IN ('claimed','retry')`, chatID, userID, token)
	return err
}
