package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// AppealReview is an administrative projection, scoped in SQL by the caller's group grants.
type AppealReview struct {
	ID                      int64           `json:"id"`
	CaseID                  int64           `json:"case_id"`
	ChatID                  int64           `json:"chat_id"`
	UserID                  int64           `json:"user_id"`
	GroupTitle              string          `json:"group_title"`
	Status                  string          `json:"status"`
	Decision                *string         `json:"decision"`
	PublicNote              string          `json:"public_note"`
	ExecutionResult         string          `json:"execution_result"`
	CaseNotificationState   string          `json:"case_notification_state"`
	ResultNotificationState string          `json:"result_notification_state"`
	ReviewerID              *int64          `json:"reviewer_id"`
	Reason                  string          `json:"reason"`
	ReferenceURL            string          `json:"reference_url"`
	TurnstileVerified       bool            `json:"turnstile_verified"`
	HcaptchaVerified        bool            `json:"hcaptcha_verified"`
	Action                  string          `json:"action"`
	CaseResult              string          `json:"case_result"`
	Explanation             string          `json:"explanation"`
	Summary                 string          `json:"summary"`
	Ownership               json.RawMessage `json:"-"`
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
	Version                 int             `json:"version"`
}

const appealReviewSelect = `SELECT a.id,a.case_id,a.chat_id,a.user_id,COALESCE(g.title,''),a.status,a.decision,a.public_note,a.execution_result,a.reviewer_id,a.reason,a.reference_url,a.turnstile_verified,a.hcaptcha_verified,c.action,c.result,c.explanation,c.summary,c.ownership,a.created_at,a.updated_at,a.review_version,COALESCE((SELECT n.state FROM moderation_notifications n WHERE n.case_id=a.case_id AND n.event_kind='moderation_case' LIMIT 1),''),COALESCE((SELECT n.state FROM moderation_notifications n WHERE n.appeal_id=a.id AND n.event_kind='appeal_result' ORDER BY n.event_version DESC LIMIT 1),'') FROM appeals a JOIN moderation_cases c ON c.id=a.case_id LEFT JOIN groups g ON g.chat_id=a.chat_id `

func scanAppealReview(r pgx.Row) (v AppealReview, err error) {
	err = r.Scan(&v.ID, &v.CaseID, &v.ChatID, &v.UserID, &v.GroupTitle, &v.Status, &v.Decision, &v.PublicNote, &v.ExecutionResult, &v.ReviewerID, &v.Reason, &v.ReferenceURL, &v.TurnstileVerified, &v.HcaptchaVerified, &v.Action, &v.CaseResult, &v.Explanation, &v.Summary, &v.Ownership, &v.CreatedAt, &v.UpdatedAt, &v.Version, &v.CaseNotificationState, &v.ResultNotificationState)
	return
}
func (q *Queries) GetAppealReview(ctx context.Context, id int64, global bool, scope []int64) (AppealReview, error) {
	return scanAppealReview(q.db.QueryRow(ctx, appealReviewSelect+` WHERE a.id=$1 AND ($2 OR a.chat_id=ANY($3::bigint[]))`, id, global, scope))
}
func (q *Queries) ListAppealReviews(ctx context.Context, global bool, scope []int64, chat int64, status string, from, to *time.Time, limit, offset int) ([]AppealReview, error) {
	rows, err := q.db.Query(ctx, appealReviewSelect+` WHERE ($1 OR a.chat_id=ANY($2::bigint[])) AND ($3=0 OR a.chat_id=$3) AND ($4='' OR a.status=$4) AND ($5::timestamptz IS NULL OR a.created_at >= $5) AND ($6::timestamptz IS NULL OR a.created_at < $6) ORDER BY a.created_at DESC,a.id DESC LIMIT $7 OFFSET $8`, global, scope, chat, status, from, to, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AppealReview, 0, limit)
	for rows.Next() {
		v, e := scanAppealReview(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (q *Queries) CountPendingAppealReviews(ctx context.Context, global bool, scope []int64) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, `SELECT count(*) FROM appeals WHERE status='pending' AND ($1 OR chat_id=ANY($2::bigint[]))`, global, scope).Scan(&n)
	return n, err
}
