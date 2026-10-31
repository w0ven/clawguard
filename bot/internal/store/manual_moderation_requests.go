package store

import "context"

type ManualModerationRequest struct {
	AdminID, ChatID, UserID int64
	Action                  string
	ResponseCode            *int
	ResponseBody            string
}

func (q *Queries) ClaimManualModerationRequest(ctx context.Context, key string, v ManualModerationRequest) (bool, error) {
	tag, err := q.db.Exec(ctx, `INSERT INTO manual_moderation_requests(request_key,admin_id,chat_id,user_id,action) VALUES($1,$2,$3,$4,$5) ON CONFLICT(request_key) DO NOTHING`, key, v.AdminID, v.ChatID, v.UserID, v.Action)
	return tag.RowsAffected() == 1, err
}

func (q *Queries) GetManualModerationRequest(ctx context.Context, key string) (ManualModerationRequest, error) {
	var v ManualModerationRequest
	err := q.db.QueryRow(ctx, `SELECT admin_id,chat_id,user_id,action,response_code,response_body FROM manual_moderation_requests WHERE request_key=$1`, key).Scan(&v.AdminID, &v.ChatID, &v.UserID, &v.Action, &v.ResponseCode, &v.ResponseBody)
	return v, err
}

func (q *Queries) CompleteManualModerationRequest(ctx context.Context, key string, code int, body string) error {
	_, err := q.db.Exec(ctx, `UPDATE manual_moderation_requests SET response_code=$2,response_body=$3,completed_at=now() WHERE request_key=$1 AND response_code IS NULL`, key, code, body)
	return err
}
