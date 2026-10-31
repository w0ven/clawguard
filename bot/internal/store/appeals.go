package store

import (
	"context"
	"crypto/sha256"
	"time"

	"github.com/jackc/pgx/v5"
)

// AppealCase is intentionally a public projection: source identities, ownership,
// provider details and AI data are never sent to the affected user.
type AppealCase struct {
	ID           int64     `json:"id"`
	GroupTitle   string    `json:"group_title"`
	Action       string    `json:"action"`
	Result       string    `json:"result"`
	Explanation  string    `json:"explanation"`
	Summary      string    `json:"summary"`
	CreatedAt    time.Time `json:"created_at"`
	AppealID     *int64    `json:"appeal_id"`
	AppealStatus *string   `json:"appeal_status"`
}
type Appeal struct {
	ID              int64     `json:"id"`
	CaseID          int64     `json:"case_id"`
	Status          string    `json:"status"`
	PublicNote      string    `json:"public_note"`
	ExecutionResult string    `json:"execution_result"`
	Reason          string    `json:"reason"`
	ReferenceURL    string    `json:"reference_url"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

const appealCaseSelect = `SELECT c.id,COALESCE(g.title,''),c.action,c.result,c.explanation,c.summary,c.created_at,a.id,a.status FROM moderation_cases c LEFT JOIN groups g ON g.chat_id=c.chat_id LEFT JOIN appeals a ON a.case_id=c.id AND a.user_id=c.user_id `

func scanAppealCase(row pgx.Row) (AppealCase, error) {
	var c AppealCase
	err := row.Scan(&c.ID, &c.GroupTitle, &c.Action, &c.Result, &c.Explanation, &c.Summary, &c.CreatedAt, &c.AppealID, &c.AppealStatus)
	return c, err
}
func (q *Queries) AppealCaseForUser(ctx context.Context, caseID, userID int64) (AppealCase, error) {
	return scanAppealCase(q.db.QueryRow(ctx, appealCaseSelect+`WHERE c.id=$1 AND c.user_id=$2`, caseID, userID))
}
func (q *Queries) ListAppealCases(ctx context.Context, userID int64, limit, offset int) ([]AppealCase, error) {
	rows, err := q.db.Query(ctx, appealCaseSelect+`WHERE c.user_id=$1 ORDER BY c.created_at DESC,c.id DESC LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AppealCase, 0, limit)
	for rows.Next() {
		c, e := scanAppealCase(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (q *Queries) AppealForUserCase(ctx context.Context, caseID, userID int64) (Appeal, error) {
	var a Appeal
	err := q.db.QueryRow(ctx, `SELECT id,case_id,status,reason,reference_url,created_at,updated_at,public_note,execution_result FROM appeals WHERE case_id=$1 AND user_id=$2`, caseID, userID).Scan(&a.ID, &a.CaseID, &a.Status, &a.Reason, &a.ReferenceURL, &a.CreatedAt, &a.UpdatedAt, &a.PublicNote, &a.ExecutionResult)
	return a, err
}
func (q *Queries) CreateAppealChallenge(ctx context.Context, token string, caseID, userID int64) error {
	tag, err := q.db.Exec(ctx, `WITH cleanup AS (DELETE FROM appeal_challenges WHERE token IN (SELECT token FROM appeal_challenges WHERE expires_at < now() ORDER BY expires_at LIMIT 100 FOR UPDATE SKIP LOCKED)), cleanup_tokens AS (DELETE FROM appeal_provider_tokens WHERE (provider,token_hash) IN (SELECT provider,token_hash FROM appeal_provider_tokens WHERE expires_at < now() ORDER BY expires_at LIMIT 100 FOR UPDATE SKIP LOCKED)) INSERT INTO appeal_challenges(token,case_id,user_id) SELECT $1,id,user_id FROM moderation_cases WHERE id=$2 AND user_id=$3 AND NOT EXISTS (SELECT 1 FROM appeals WHERE case_id=$2)`, token, caseID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

// Claim burns the challenge before contacting external providers. A failed or
// interrupted verification requires a new challenge, never a provider replay.
func (q *Queries) ClaimAppealChallenge(ctx context.Context, token string, caseID, userID int64, cf, hc string) error {
	cfHash, hcHash := sha256.Sum256([]byte(cf)), sha256.Sum256([]byte(hc))
	return q.Transact(ctx, func(tx *Queries) error {
		tag, err := tx.db.Exec(ctx, `UPDATE appeal_challenges SET state='claimed' WHERE token=$1 AND case_id=$2 AND user_id=$3 AND state='open' AND expires_at>now()`, token, caseID, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return pgx.ErrNoRows
		}
		tag, err = tx.db.Exec(ctx, `INSERT INTO appeal_provider_tokens(provider,token_hash) VALUES ('turnstile',$1),('hcaptcha',$2) ON CONFLICT DO NOTHING`, cfHash[:], hcHash[:])
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 2 {
			return pgx.ErrNoRows
		}
		return nil
	})
}
func (q *Queries) SubmitAppeal(ctx context.Context, token string, caseID, userID int64, reason, link string) (Appeal, error) {
	var a Appeal
	err := q.Transact(ctx, func(tx *Queries) error {
		tag, e := tx.db.Exec(ctx, `UPDATE appeal_challenges SET state='used' WHERE token=$1 AND case_id=$2 AND user_id=$3 AND state='claimed' AND expires_at>now()`, token, caseID, userID)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return pgx.ErrNoRows
		}
		_, e = tx.db.Exec(ctx, `INSERT INTO appeals(case_id,chat_id,user_id,reason,reference_url) SELECT id,chat_id,user_id,$3,$4 FROM moderation_cases WHERE id=$1 AND user_id=$2 ON CONFLICT(case_id) DO NOTHING`, caseID, userID, reason, link)
		if e != nil {
			return e
		}
		a, e = tx.AppealForUserCase(ctx, caseID, userID)
		return e
	})
	return a, err
}
