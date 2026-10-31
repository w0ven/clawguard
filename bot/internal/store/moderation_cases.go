package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/openclaw/clawguard/internal/redact"
)

type ModerationCaseInput struct {
	SourceKey, SourceKind, SourceID      string
	ChatID, UserID                       int64
	Action, Result, Explanation, Summary string
	Ownership                            json.RawMessage
}

type ModerationNotification struct {
	ID, CaseID, UserID, ChatID                       int64
	GroupTitle, Action, Result, Explanation, Summary string
	EventKind, EventText                             string
	Attempts                                         int32
	LeaseToken                                       string
	ExpiresAt                                        time.Time
}

var caseEmail = regexp.MustCompile(`(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`)
var casePhone = regexp.MustCompile(`\b\+?[0-9][0-9 -]{7,}[0-9]\b`)
var caseURLQuery = regexp.MustCompile(`(https?://[^\s?#]+)[?#][^\s]*`)
var caseMention = regexp.MustCompile(`@[A-Za-z0-9_]{5,}`)

func caseSummary(s string, limit int) string {
	s = redact.Text(strings.Join(strings.Fields(s), " "))
	s = caseEmail.ReplaceAllString(s, "[邮箱]")
	s = casePhone.ReplaceAllString(s, "[号码]")
	s = caseURLQuery.ReplaceAllString(s, "$1")
	s = caseMention.ReplaceAllString(s, "[用户]")
	if utf8.RuneCountInString(s) > limit {
		s = string([]rune(s)[:limit]) + "…"
	}
	return s
}

// RecordModerationCase is a single atomic insert; duplicates never re-enqueue.
// It must only be called with a stable source id and an identified human.
func (q *Queries) RecordModerationCase(ctx context.Context, v ModerationCaseInput) (int64, error) {
	if v.SourceKey == "" || v.SourceKind == "" || v.SourceID == "" || v.ChatID == 0 || v.UserID <= 0 {
		return 0, errors.New("missing moderation case identity")
	}
	switch v.Result {
	case "executed", "partial", "failed", "reversed":
	default:
		return 0, errors.New("invalid moderation case result")
	}
	if len(v.Ownership) == 0 {
		v.Ownership = json.RawMessage(`{}`)
	}
	if !json.Valid(v.Ownership) {
		return 0, errors.New("invalid ownership")
	}
	var id int64
	err := q.db.QueryRow(ctx, `WITH inserted AS (
  INSERT INTO moderation_cases(source_key,source_kind,source_id,chat_id,user_id,action,result,explanation,summary,ownership)
  VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb) ON CONFLICT(source_key) DO NOTHING RETURNING id,user_id,chat_id
 ), updated AS (
  UPDATE moderation_cases SET
   explanation=CASE WHEN moderation_cases.result NOT IN ('executed','reversed') AND ($7 IN ('executed','reversed') OR moderation_cases.result='failed') THEN $8 ELSE moderation_cases.explanation END,
   ownership=CASE WHEN moderation_cases.result NOT IN ('executed','reversed') AND ($7 IN ('executed','reversed') OR moderation_cases.result='failed') THEN $10::jsonb ELSE moderation_cases.ownership END,
   result=CASE WHEN moderation_cases.result IN ('executed','reversed') THEN moderation_cases.result WHEN $7 IN ('executed','reversed') THEN $7 WHEN moderation_cases.result='partial' THEN 'partial' ELSE $7 END
  WHERE source_key=$1 AND source_kind=$2 AND source_id=$3 AND chat_id=$4 AND user_id=$5 AND action=$6 AND NOT EXISTS (SELECT 1 FROM inserted) RETURNING id
 ), notification AS (
  INSERT INTO moderation_notifications(case_id,chat_id,user_id,state,session_epoch)
  SELECT inserted.id,inserted.chat_id,inserted.user_id,CASE WHEN s.is_reachable THEN 'pending' ELSE 'unreachable' END,s.notification_epoch FROM inserted LEFT JOIN telegram_private_sessions s ON s.user_id=inserted.user_id
 ) SELECT id FROM inserted`, v.SourceKey, v.SourceKind, v.SourceID, v.ChatID, v.UserID, v.Action, v.Result, caseSummary(v.Explanation, 160), caseSummary(v.Summary, 240), []byte(v.Ownership)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// Expired leases which were already sent to Telegram are uncertain, not retried.
func (q *Queries) RecoverModerationNotifications(ctx context.Context) error {
	_, err := q.db.Exec(ctx, `WITH due AS (SELECT id FROM moderation_notifications WHERE (state IN ('leased','sending') AND lease_until<=now()) OR (state='pending' AND (expires_at<=now() OR NOT EXISTS (SELECT 1 FROM telegram_private_sessions s WHERE s.user_id=moderation_notifications.user_id AND s.is_reachable AND s.notification_epoch=moderation_notifications.session_epoch))) ORDER BY updated_at,id LIMIT 100 FOR UPDATE SKIP LOCKED) UPDATE moderation_notifications n SET state=CASE WHEN n.state='sending' THEN 'uncertain' WHEN n.expires_at<=now() THEN 'expired' WHEN EXISTS (SELECT 1 FROM telegram_private_sessions s WHERE s.user_id=n.user_id AND s.is_reachable AND s.notification_epoch<>n.session_epoch) THEN 'expired' WHEN NOT EXISTS (SELECT 1 FROM telegram_private_sessions s WHERE s.user_id=n.user_id AND s.is_reachable AND s.notification_epoch=n.session_epoch) THEN 'unreachable' ELSE 'pending' END,lease_token=NULL,lease_until=NULL,updated_at=now() FROM due WHERE n.id=due.id`)
	return err
}

func (q *Queries) ClaimModerationNotifications(ctx context.Context, token string, limit int32) ([]ModerationNotification, error) {
	if token == "" || limit < 1 || limit > 100 {
		return nil, fmt.Errorf("invalid notification claim")
	}
	rows, err := q.db.Query(ctx, `WITH picked AS (
 SELECT n.id FROM moderation_notifications n
 WHERE n.state='pending' AND n.next_attempt_at<=now() AND n.expires_at>now()
 AND EXISTS (SELECT 1 FROM telegram_private_sessions s WHERE s.user_id=n.user_id AND s.is_reachable AND s.notification_epoch=n.session_epoch)
 AND NOT EXISTS (SELECT 1 FROM moderation_notifications earlier WHERE earlier.user_id=n.user_id AND earlier.state='pending' AND earlier.session_epoch=n.session_epoch AND earlier.expires_at>now() AND (earlier.next_attempt_at,earlier.id)<(n.next_attempt_at,n.id))
 AND NOT EXISTS (SELECT 1 FROM moderation_notifications active WHERE active.user_id=n.user_id AND active.state IN ('leased','sending') AND active.lease_until>now())
 ORDER BY n.next_attempt_at,n.id LIMIT $2 FOR UPDATE OF n SKIP LOCKED
 ), claimed AS (
 UPDATE moderation_notifications n SET state='leased',lease_token=$1,lease_until=now()+interval '1 minute',updated_at=now() FROM picked p WHERE n.id=p.id RETURNING n.*
 ) SELECT n.id,n.case_id,n.user_id,c.chat_id,COALESCE(g.title,''),c.action,c.result,c.explanation,c.summary,n.attempts,n.lease_token,n.expires_at,n.event_kind,n.event_text FROM claimed n JOIN moderation_cases c ON c.id=n.case_id LEFT JOIN groups g ON g.chat_id=c.chat_id`, token, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModerationNotification
	for rows.Next() {
		var n ModerationNotification
		if err = rows.Scan(&n.ID, &n.CaseID, &n.UserID, &n.ChatID, &n.GroupTitle, &n.Action, &n.Result, &n.Explanation, &n.Summary, &n.Attempts, &n.LeaseToken, &n.ExpiresAt, &n.EventKind, &n.EventText); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (q *Queries) BeginModerationNotification(ctx context.Context, id int64, token string) (bool, error) {
	tag, err := q.db.Exec(ctx, `UPDATE moderation_notifications SET state='sending',attempts=attempts+1,session_version=(SELECT s.updated_at FROM telegram_private_sessions s WHERE s.user_id=moderation_notifications.user_id),updated_at=now() WHERE id=$1 AND state='leased' AND lease_token=$2 AND lease_until>now() AND expires_at>now() AND EXISTS (SELECT 1 FROM telegram_private_sessions s WHERE s.user_id=moderation_notifications.user_id AND s.is_reachable AND s.notification_epoch=moderation_notifications.session_epoch)`, id, token)
	return tag.RowsAffected() == 1, err
}
func (q *Queries) SettleModerationNotification(ctx context.Context, id int64, token, state string, retryAt time.Time) (bool, error) {
	switch state {
	case "sent", "unreachable", "failed", "pending":
	default:
		return false, errors.New("invalid notification outcome")
	}
	var settled bool
	err := q.db.QueryRow(ctx, `WITH updated AS (
 UPDATE moderation_notifications SET state=CASE WHEN $3='pending' AND (attempts>=5 OR $4>=expires_at) THEN 'failed' ELSE $3 END,next_attempt_at=$4,lease_token=NULL,lease_until=NULL,updated_at=now() WHERE id=$1 AND state='sending' AND lease_token=$2 AND lease_until>now() RETURNING user_id,state,session_version
 ), blocked AS (
 UPDATE telegram_private_sessions s SET is_reachable=false,updated_at=now() FROM updated u WHERE u.state='unreachable' AND s.user_id=u.user_id AND s.updated_at=u.session_version RETURNING s.user_id
 ) SELECT EXISTS(SELECT 1 FROM updated)`, id, token, state, retryAt).Scan(&settled)
	return settled, err
}
