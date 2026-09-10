package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"strings"
)

type NativeAssistantScope struct {
	ChatID      int64
	ThreadID    int32
	Generation  int64
	ChatEnabled bool
}

func (q *Queries) NativeAssistantScopes(ctx context.Context) ([]NativeAssistantScope, error) {
	rows, err := q.db.Query(ctx, `SELECT a.chat_id,COALESCE(s.thread_id,0),COALESCE(s.generation,0),COALESCE(p.chat_enabled,false)
 FROM authorized_groups a LEFT JOIN group_assistant_policies p ON a.chat_id=p.chat_id
 LEFT JOIN assistant_native_scopes s ON s.chat_id=a.chat_id
 WHERE a.enabled AND (p.chat_enabled OR p.mimic_target_user_id<>0 OR s.chat_id IS NOT NULL) ORDER BY a.chat_id,COALESCE(s.thread_id,0)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NativeAssistantScope{}
	for rows.Next() {
		var s NativeAssistantScope
		if err := rows.Scan(&s.ChatID, &s.ThreadID, &s.Generation, &s.ChatEnabled); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (q *Queries) PendingNativeAssistantScopes(ctx context.Context) ([]NativeAssistantScope, error) {
	rows, err := q.db.Query(ctx, `SELECT DISTINCT e.chat_id,e.thread_id FROM assistant_native_events e JOIN authorized_groups a ON a.chat_id=e.chat_id WHERE e.status IN ('pending','accepted') AND a.enabled ORDER BY e.chat_id,e.thread_id LIMIT 64`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NativeAssistantScope{}
	for rows.Next() {
		var scope NativeAssistantScope
		if err := rows.Scan(&scope.ChatID, &scope.ThreadID); err != nil {
			return nil, err
		}
		out = append(out, scope)
	}
	return out, rows.Err()
}

func (q *Queries) EnsureNativeAssistantPolicy(ctx context.Context, chatID int64) error {
	_, err := q.db.Exec(ctx, `INSERT INTO group_assistant_policies(chat_id) VALUES ($1) ON CONFLICT DO NOTHING`, chatID)
	return err
}
func (q *Queries) NativeScopeExists(ctx context.Context, chatID int64) (bool, error) {
	var exists bool
	err := q.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assistant_native_scopes WHERE chat_id=$1)`, chatID).Scan(&exists)
	return exists, err
}
func (q *Queries) NativeScopeGeneration(ctx context.Context, chatID int64, threadID int32) (int64, error) {
	_, err := q.db.Exec(ctx, `INSERT INTO assistant_native_scopes(chat_id,thread_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, chatID, threadID)
	if err != nil {
		return 0, err
	}
	var generation int64
	err = q.db.QueryRow(ctx, `SELECT generation FROM assistant_native_scopes WHERE chat_id=$1 AND thread_id=$2`, chatID, threadID).Scan(&generation)
	return generation, err
}

type NativeAssistantEvent struct {
	ID         int64
	ChatID     int64
	ThreadID   int32
	MessageID  int64
	Revision   string
	Kind       string
	Generation int64
	Payload    json.RawMessage
	Status     string
}

func (q *Queries) EnqueueNativeAssistantEvent(ctx context.Context, event NativeAssistantEvent) (NativeAssistantEvent, error) {
	err := q.Transact(ctx, func(tx *Queries) error {
		_, err := tx.db.Exec(ctx, `INSERT INTO assistant_native_scopes(chat_id,thread_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, event.ChatID, event.ThreadID)
		if err != nil {
			return err
		}
		err = tx.db.QueryRow(ctx, `SELECT generation FROM assistant_native_scopes WHERE chat_id=$1 AND thread_id=$2 FOR UPDATE`, event.ChatID, event.ThreadID).Scan(&event.Generation)
		if err != nil {
			return err
		}
		err = tx.db.QueryRow(ctx, `SELECT id,generation,status FROM assistant_native_events WHERE chat_id=$1 AND thread_id=$2 AND telegram_message_id=$3 AND revision=$4 AND kind=$5`, event.ChatID, event.ThreadID, event.MessageID, event.Revision, event.Kind).Scan(&event.ID, &event.Generation, &event.Status)
		if err == nil {
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		if event.Kind != "message" {
			err = tx.db.QueryRow(ctx, `UPDATE assistant_native_scopes SET generation=generation+1 WHERE chat_id=$1 AND thread_id=$2 RETURNING generation`, event.ChatID, event.ThreadID).Scan(&event.Generation)
			if err != nil {
				return err
			}
		}
		return tx.db.QueryRow(ctx, `INSERT INTO assistant_native_events(chat_id,thread_id,telegram_message_id,revision,kind,generation,payload) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id,status`, event.ChatID, event.ThreadID, event.MessageID, event.Revision, event.Kind, event.Generation, event.Payload).Scan(&event.ID, &event.Status)
	})
	return event, err
}

func (q *Queries) NativeAssistantEvents(ctx context.Context, chatID int64, threadID int32) ([]NativeAssistantEvent, error) {
	rows, err := q.db.Query(ctx, `SELECT id,chat_id,thread_id,telegram_message_id,revision,kind,generation,payload,status FROM assistant_native_events WHERE chat_id=$1 AND thread_id=$2 AND status IN ('pending','accepted') ORDER BY CASE WHEN kind IN ('edit','invalidate','forget') THEN 0 ELSE 1 END,id`, chatID, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NativeAssistantEvent{}
	for rows.Next() {
		var e NativeAssistantEvent
		if err := rows.Scan(&e.ID, &e.ChatID, &e.ThreadID, &e.MessageID, &e.Revision, &e.Kind, &e.Generation, &e.Payload, &e.Status); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (q *Queries) SetNativeAssistantEventStatus(ctx context.Context, id int64, status string) error {
	_, err := q.db.Exec(ctx, `UPDATE assistant_native_events SET status=$2 WHERE id=$1 AND status NOT IN ('done','uncertain')`, id, status)
	return err
}
func (q *Queries) NativeAssistantGenerationValid(ctx context.Context, chatID int64, threadID int32, generation int64) (bool, error) {
	var valid bool
	err := q.db.QueryRow(ctx, `SELECT generation=$3 AND NOT EXISTS (SELECT 1 FROM assistant_native_events WHERE chat_id=$1 AND thread_id=$2 AND kind<>'message' AND status='pending') FROM assistant_native_scopes WHERE chat_id=$1 AND thread_id=$2`, chatID, threadID, generation).Scan(&valid)
	return valid, err
}
func (q *Queries) NativeEventIsCurrent(ctx context.Context, event NativeAssistantEvent) (bool, error) {
	var current bool
	err := q.db.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM assistant_native_events WHERE chat_id=$1 AND thread_id=$2 AND telegram_message_id=$3 AND id>$4)`, event.ChatID, event.ThreadID, event.MessageID, event.ID).Scan(&current)
	return current, err
}
func (q *Queries) NativeOriginApproved(ctx context.Context, chatID int64, threadID int32, messageID int64) (bool, error) {
	var known bool
	err := q.db.QueryRow(ctx, `SELECT (EXISTS(SELECT 1 FROM assistant_native_events WHERE chat_id=$1 AND thread_id=$2 AND telegram_message_id=$3 AND kind='message' AND NOT COALESCE((payload->>'style_only')::boolean,false)) OR EXISTS(SELECT 1 FROM group_assistant_messages WHERE chat_id=$1 AND thread_id=$2 AND telegram_message_id=$3 AND approved AND delivered AND expires_at>NOW())) AND NOT EXISTS(SELECT 1 FROM group_assistant_memories WHERE chat_id=$1 AND source_message_id=$3 AND forgotten_at IS NOT NULL)`, chatID, threadID, messageID).Scan(&known)
	return known, err
}
func (q *Queries) NativeAssistantSourceVisible(ctx context.Context, chatID int64, threadID int32, messageID int64) (bool, error) {
	var visible bool
	err := q.db.QueryRow(ctx, `SELECT COALESCE((SELECT kind IN ('message','edit') AND NOT COALESCE((payload->>'style_only')::boolean,false) FROM assistant_native_events WHERE chat_id=$1 AND thread_id=$2 AND telegram_message_id=$3 ORDER BY id DESC LIMIT 1),EXISTS(SELECT 1 FROM group_assistant_messages WHERE chat_id=$1 AND thread_id=$2 AND telegram_message_id=$3 AND approved AND delivered AND expires_at>NOW()))
 AND NOT EXISTS (SELECT 1 FROM group_assistant_memories WHERE chat_id=$1 AND source_message_id=$3 AND forgotten_at IS NOT NULL)`, chatID, threadID, messageID).Scan(&visible)
	return visible, err
}
func (q *Queries) BeginNativeDelivery(ctx context.Context, turn string, chatID int64, threadID int32, method string, purposes ...string) (int64, error) {
	purpose := "reply"
	if len(purposes) > 0 {
		purpose = purposes[0]
	}
	var id int64
	err := q.Transact(ctx, func(tx *Queries) error {
		var generation int64
		if err := tx.db.QueryRow(ctx, `SELECT generation FROM assistant_native_scopes WHERE chat_id=$1 AND thread_id=$2 FOR UPDATE`, chatID, threadID).Scan(&generation); err != nil {
			return err
		}
		if strings.HasPrefix(method, "send") {
			var uncertain bool
			if err := tx.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assistant_native_deliveries WHERE chat_id=$1 AND thread_id=$2 AND turn_id=$3 AND purpose=$4 AND method<>'deleteMessage' AND status IN ('attempting','uncertain'))`, chatID, threadID, turn, purpose).Scan(&uncertain); err != nil {
				return err
			}
			if uncertain {
				return errors.New("earlier delivery outcome uncertain; no replay")
			}
		}
		return tx.db.QueryRow(ctx, `INSERT INTO assistant_native_deliveries(turn_id,chat_id,thread_id,method,purpose,status) VALUES ($1,$2,$3,$4,$5,'attempting') RETURNING id`, turn, chatID, threadID, method, purpose).Scan(&id)
	})
	return id, err
}
func (q *Queries) FinishNativeDelivery(ctx context.Context, id int64, messageID *int64, status string) error {
	_, err := q.db.Exec(ctx, `UPDATE assistant_native_deliveries SET telegram_message_id=$2,status=$3 WHERE id=$1`, id, messageID, status)
	return err
}
func (q *Queries) NativeDeliveryOwned(ctx context.Context, chatID int64, threadID int32, messageID int64) (bool, error) {
	var owned bool
	err := q.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assistant_native_deliveries WHERE chat_id=$1 AND thread_id=$2 AND telegram_message_id=$3 AND status='delivered')`, chatID, threadID, messageID).Scan(&owned)
	return owned, err
}
func (q *Queries) NativeMediaAuthorized(ctx context.Context, chatID int64, threadID int32, fileID string, eventID int64) (bool, error) {
	var allowed bool
	err := q.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM (SELECT DISTINCT ON (telegram_message_id) id,kind,payload,telegram_message_id FROM assistant_native_events WHERE chat_id=$1 AND thread_id=$2 ORDER BY telegram_message_id,id DESC) current_source
 WHERE id=$4 AND kind IN ('message','edit') AND jsonb_path_exists(payload,'$.**.file_id ? (@ == $file)',jsonb_build_object('file',$3::text))
 AND NOT EXISTS (SELECT 1 FROM group_assistant_memories WHERE chat_id=$1 AND source_message_id=current_source.telegram_message_id AND forgotten_at IS NOT NULL))`, chatID, threadID, fileID, eventID).Scan(&allowed)
	return allowed, err
}
func (q *Queries) NativeTurnConsumed(ctx context.Context, turn string) (bool, error) {
	var consumed bool
	err := q.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assistant_native_deliveries WHERE turn_id=$1 AND status IN ('attempting','delivered','uncertain') AND purpose='reply' AND method NOT IN ('sendChatAction','deleteMessage'))`, turn).Scan(&consumed)
	return consumed, err
}
