package store

import "context"

const createScheduledMessage = `-- name: CreateScheduledMessage :one
INSERT INTO scheduled_messages (
    chat_id,
    name,
    schedule_type,
    interval_minutes,
    daily_times,
    timezone,
    content,
    buttons,
    auto_delete_seconds,
    enabled,
    status
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING id, chat_id, name, schedule_type, interval_minutes, daily_times, timezone, content, buttons, auto_delete_seconds, enabled, status, last_run_at, last_message_id, last_error, last_skip_reason, created_at, updated_at
`

const getScheduledMessage = `-- name: GetScheduledMessage :one
SELECT id, chat_id, name, schedule_type, interval_minutes, daily_times, timezone, content, buttons, auto_delete_seconds, enabled, status, last_run_at, last_message_id, last_error, last_skip_reason, created_at, updated_at
FROM scheduled_messages
WHERE id = $1
`

const listScheduledMessagesByChat = `-- name: ListScheduledMessagesByChat :many
SELECT id, chat_id, name, schedule_type, interval_minutes, daily_times, timezone, content, buttons, auto_delete_seconds, enabled, status, last_run_at, last_message_id, last_error, last_skip_reason, created_at, updated_at
FROM scheduled_messages
WHERE chat_id = $1
ORDER BY created_at DESC, id DESC
`

const listActiveScheduledMessages = `-- name: ListActiveScheduledMessages :many
SELECT id, chat_id, name, schedule_type, interval_minutes, daily_times, timezone, content, buttons, auto_delete_seconds, enabled, status, last_run_at, last_message_id, last_error, last_skip_reason, created_at, updated_at
FROM scheduled_messages
WHERE enabled = true AND status = 'active'
ORDER BY id ASC
`

const countScheduledMessagesByChat = `-- name: CountScheduledMessagesByChat :one
SELECT count(*)
FROM scheduled_messages
WHERE chat_id = $1
`

const updateScheduledMessage = `-- name: UpdateScheduledMessage :one
UPDATE scheduled_messages
SET name = $3,
    schedule_type = $4,
    interval_minutes = $5,
    daily_times = $6,
    timezone = $7,
    content = $8,
    buttons = $9,
    auto_delete_seconds = $10,
    enabled = $11,
    status = $12,
    last_error = CASE WHEN $12 = 'active' THEN NULL ELSE last_error END,
    updated_at = now()
WHERE id = $1 AND chat_id = $2
RETURNING id, chat_id, name, schedule_type, interval_minutes, daily_times, timezone, content, buttons, auto_delete_seconds, enabled, status, last_run_at, last_message_id, last_error, last_skip_reason, created_at, updated_at
`

const deleteScheduledMessage = `-- name: DeleteScheduledMessage :exec
DELETE FROM scheduled_messages
WHERE id = $1 AND chat_id = $2
`

const markScheduledMessageSent = `-- name: MarkScheduledMessageSent :exec
UPDATE scheduled_messages
SET last_run_at = now(),
    last_message_id = $2,
    last_error = NULL,
    last_skip_reason = NULL,
    updated_at = now()
WHERE id = $1
`

const markScheduledMessageFailed = `-- name: MarkScheduledMessageFailed :exec
UPDATE scheduled_messages
SET enabled = false,
    status = 'failed',
    last_error = $2,
    updated_at = now()
WHERE id = $1
`

const markScheduledMessageSkipped = `-- name: MarkScheduledMessageSkipped :exec
UPDATE scheduled_messages
SET last_skip_reason = $2,
    updated_at = now()
WHERE id = $1
`

const insertScheduledMessageRun = `-- name: InsertScheduledMessageRun :one
WITH inserted AS (
    INSERT INTO scheduled_message_runs (
        scheduled_message_id,
        success,
        tg_message_id,
        rendered_preview,
        error,
        duration_ms
    ) VALUES ($1, $2, $3, $4, $5, $6)
    RETURNING id, scheduled_message_id, ran_at, success, tg_message_id, rendered_preview, error, duration_ms
), pruned AS (
    DELETE FROM scheduled_message_runs
    WHERE scheduled_message_id = $1
      AND id NOT IN (
        SELECT id
        FROM scheduled_message_runs
        WHERE scheduled_message_id = $1
        ORDER BY ran_at DESC, id DESC
        LIMIT 50
      )
)
SELECT id, scheduled_message_id, ran_at, success, tg_message_id, rendered_preview, error, duration_ms
FROM inserted
`

const listScheduledMessageRuns = `-- name: ListScheduledMessageRuns :many
SELECT id, scheduled_message_id, ran_at, success, tg_message_id, rendered_preview, error, duration_ms
FROM scheduled_message_runs
WHERE scheduled_message_id = $1
ORDER BY ran_at DESC, id DESC
LIMIT 50
`

type CreateScheduledMessageParams struct {
	ChatID            int64
	Name              string
	ScheduleType      string
	IntervalMinutes   *int32
	DailyTimes        []string
	Timezone          string
	Content           string
	Buttons           []byte
	AutoDeleteSeconds int32
	Enabled           bool
	Status            string
}

type UpdateScheduledMessageParams struct {
	ID                int64
	ChatID            int64
	Name              string
	ScheduleType      string
	IntervalMinutes   *int32
	DailyTimes        []string
	Timezone          string
	Content           string
	Buttons           []byte
	AutoDeleteSeconds int32
	Enabled           bool
	Status            string
}

type DeleteScheduledMessageParams struct {
	ID     int64
	ChatID int64
}

type InsertScheduledMessageRunParams struct {
	ScheduledMessageID int64
	Success            bool
	TgMessageID        *int64
	RenderedPreview    *string
	Error              *string
	DurationMs         *int32
}

func scanScheduledMessage(row interface{ Scan(...any) error }) (ScheduledMessage, error) {
	var i ScheduledMessage
	err := row.Scan(
		&i.ID,
		&i.ChatID,
		&i.Name,
		&i.ScheduleType,
		&i.IntervalMinutes,
		&i.DailyTimes,
		&i.Timezone,
		&i.Content,
		&i.Buttons,
		&i.AutoDeleteSeconds,
		&i.Enabled,
		&i.Status,
		&i.LastRunAt,
		&i.LastMessageID,
		&i.LastError,
		&i.LastSkipReason,
		&i.CreatedAt,
		&i.UpdatedAt,
	)
	return i, err
}

func scanScheduledMessageRun(row interface{ Scan(...any) error }) (ScheduledMessageRun, error) {
	var i ScheduledMessageRun
	err := row.Scan(
		&i.ID,
		&i.ScheduledMessageID,
		&i.RanAt,
		&i.Success,
		&i.TgMessageID,
		&i.RenderedPreview,
		&i.Error,
		&i.DurationMs,
	)
	return i, err
}

func (q *Queries) CreateScheduledMessage(ctx context.Context, arg CreateScheduledMessageParams) (ScheduledMessage, error) {
	row := q.db.QueryRow(ctx, createScheduledMessage, arg.ChatID, arg.Name, arg.ScheduleType, arg.IntervalMinutes, arg.DailyTimes, arg.Timezone, arg.Content, arg.Buttons, arg.AutoDeleteSeconds, arg.Enabled, arg.Status)
	return scanScheduledMessage(row)
}

func (q *Queries) GetScheduledMessage(ctx context.Context, id int64) (ScheduledMessage, error) {
	row := q.db.QueryRow(ctx, getScheduledMessage, id)
	return scanScheduledMessage(row)
}

func (q *Queries) ListScheduledMessagesByChat(ctx context.Context, chatID int64) ([]ScheduledMessage, error) {
	rows, err := q.db.Query(ctx, listScheduledMessagesByChat, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []ScheduledMessage
	for rows.Next() {
		item, err := scanScheduledMessage(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (q *Queries) ListActiveScheduledMessages(ctx context.Context) ([]ScheduledMessage, error) {
	rows, err := q.db.Query(ctx, listActiveScheduledMessages)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []ScheduledMessage
	for rows.Next() {
		item, err := scanScheduledMessage(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (q *Queries) CountScheduledMessagesByChat(ctx context.Context, chatID int64) (int64, error) {
	row := q.db.QueryRow(ctx, countScheduledMessagesByChat, chatID)
	var count int64
	err := row.Scan(&count)
	return count, err
}

func (q *Queries) UpdateScheduledMessage(ctx context.Context, arg UpdateScheduledMessageParams) (ScheduledMessage, error) {
	row := q.db.QueryRow(ctx, updateScheduledMessage, arg.ID, arg.ChatID, arg.Name, arg.ScheduleType, arg.IntervalMinutes, arg.DailyTimes, arg.Timezone, arg.Content, arg.Buttons, arg.AutoDeleteSeconds, arg.Enabled, arg.Status)
	return scanScheduledMessage(row)
}

func (q *Queries) DeleteScheduledMessage(ctx context.Context, arg DeleteScheduledMessageParams) error {
	_, err := q.db.Exec(ctx, deleteScheduledMessage, arg.ID, arg.ChatID)
	return err
}

func (q *Queries) MarkScheduledMessageSent(ctx context.Context, id int64, messageID int64) error {
	_, err := q.db.Exec(ctx, markScheduledMessageSent, id, messageID)
	return err
}

func (q *Queries) MarkScheduledMessageFailed(ctx context.Context, id int64, lastError string) error {
	_, err := q.db.Exec(ctx, markScheduledMessageFailed, id, lastError)
	return err
}

func (q *Queries) MarkScheduledMessageSkipped(ctx context.Context, id int64, reason string) error {
	_, err := q.db.Exec(ctx, markScheduledMessageSkipped, id, reason)
	return err
}

func (q *Queries) InsertScheduledMessageRun(ctx context.Context, arg InsertScheduledMessageRunParams) (ScheduledMessageRun, error) {
	row := q.db.QueryRow(ctx, insertScheduledMessageRun, arg.ScheduledMessageID, arg.Success, arg.TgMessageID, arg.RenderedPreview, arg.Error, arg.DurationMs)
	return scanScheduledMessageRun(row)
}

func (q *Queries) ListScheduledMessageRuns(ctx context.Context, scheduledMessageID int64) ([]ScheduledMessageRun, error) {
	rows, err := q.db.Query(ctx, listScheduledMessageRuns, scheduledMessageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []ScheduledMessageRun
	for rows.Next() {
		item, err := scanScheduledMessageRun(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
