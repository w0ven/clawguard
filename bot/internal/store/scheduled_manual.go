package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrScheduledManualConflict = errors.New("scheduled manual request conflicts with its original intent")

type ScheduledManualResult struct {
	AttemptID int64
	State     string
	MessageID *int64
	Claimed   bool
	Message   ScheduledMessage
}

// ClaimScheduledManual binds a globally unique request key to the administrator,
// task, group and exact send configuration under the same transaction as the
// attempt. A row share lock prevents editing or deleting the snapshot mid-claim.
func (q *Queries) ClaimScheduledManual(ctx context.Context, key string, adminID, chatID, taskID int64) (ScheduledManualResult, error) {
	hash := sha256.Sum256([]byte(key))
	keyHash := hex.EncodeToString(hash[:])
	var result ScheduledManualResult
	err := q.Transact(ctx, func(tx *Queries) error {
		if _, err := tx.db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 774489))`, keyHash); err != nil {
			return err
		}
		var oldAdmin, oldChat, oldTask, attempt int64
		var oldSnapshot []byte
		var state string
		var mid *int64
		err := tx.db.QueryRow(ctx, `SELECT r.admin_id,r.chat_id,r.task_id,r.config_snapshot,r.attempt_id,a.state,a.telegram_message_id FROM scheduled_manual_requests r JOIN scheduled_dispatch_attempts a ON a.id=r.attempt_id WHERE r.key_hash=$1`, keyHash).Scan(&oldAdmin, &oldChat, &oldTask, &oldSnapshot, &attempt, &state, &mid)
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if exists && (oldAdmin != adminID || oldChat != chatID || oldTask != taskID) {
			return ErrScheduledManualConflict
		}
		var msg ScheduledMessage
		err = tx.db.QueryRow(ctx, `SELECT id,chat_id,name,schedule_type,interval_minutes,daily_times,timezone,content,buttons,auto_delete_seconds,enabled,status,last_run_at,last_message_id,last_error,last_skip_reason,created_at,updated_at FROM scheduled_messages WHERE id=$1 AND chat_id=$2 FOR SHARE`, taskID, chatID).Scan(&msg.ID, &msg.ChatID, &msg.Name, &msg.ScheduleType, &msg.IntervalMinutes, &msg.DailyTimes, &msg.Timezone, &msg.Content, &msg.Buttons, &msg.AutoDeleteSeconds, &msg.Enabled, &msg.Status, &msg.LastRunAt, &msg.LastMessageID, &msg.LastError, &msg.LastSkipReason, &msg.CreatedAt, &msg.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) && exists {
			result = ScheduledManualResult{AttemptID: attempt, State: state, MessageID: mid}
			return nil
		}
		if err != nil {
			return err
		}
		snapshot, err := json.Marshal(struct {
			Content    string          `json:"content"`
			Buttons    json.RawMessage `json:"buttons"`
			AutoDelete int32           `json:"auto_delete_seconds"`
		}{msg.Content, normalButtons(msg.Buttons), msg.AutoDeleteSeconds})
		if err != nil {
			return err
		}
		if exists {
			var equal bool
			if err = tx.db.QueryRow(ctx, `SELECT $1::jsonb = $2::jsonb`, oldSnapshot, snapshot).Scan(&equal); err != nil {
				return err
			}
			if !equal {
				return ErrScheduledManualConflict
			}
			result = ScheduledManualResult{AttemptID: attempt, State: state, MessageID: mid}
			return nil
		}
		if msg.Status == "deleted" || msg.Status == "archived" {
			return errors.New("scheduled message is archived/deleted, cannot run")
		}
		attempt, _, err = tx.ClaimScheduledDispatch(ctx, msg.ID, msg.ChatID, nil)
		if err != nil {
			return err
		}
		_, err = tx.db.Exec(ctx, `INSERT INTO scheduled_manual_requests(key_hash,admin_id,chat_id,task_id,config_snapshot,attempt_id) VALUES($1,$2,$3,$4,$5::jsonb,$6)`, keyHash, adminID, chatID, taskID, snapshot, attempt)
		if err != nil {
			return err
		}
		result = ScheduledManualResult{AttemptID: attempt, State: "dispatching", Claimed: true, Message: msg}
		return nil
	})
	return result, err
}
func normalButtons(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(b)
}
