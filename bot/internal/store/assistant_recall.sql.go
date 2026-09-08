package store

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
)

// Every index path rechecks the authoritative source and forgotten-source
// tombstones. An index is never a second source of truth for validity.
const assistantRecallValidity = `m.approved AND m.delivered AND m.expires_at>NOW()
 AND NOT EXISTS (SELECT 1 FROM group_assistant_memories f WHERE f.chat_id=m.chat_id AND f.forgotten_at IS NOT NULL
 AND COALESCE(f.source_chat_id,f.chat_id)=m.chat_id AND f.source_message_id=m.telegram_message_id)`

func assistantRecallColumns() string {
	columns := strings.Split(groupAssistantMessageColumns, ",")
	for i, c := range columns {
		columns[i] = "m." + strings.TrimSpace(c)
	}
	return strings.Join(columns, ",")
}
func scanAssistantRecallRows(rows pgx.Rows) ([]GroupAssistantMessage, error) {
	defer rows.Close()
	out := []GroupAssistantMessage{}
	for rows.Next() {
		v, err := scanGroupAssistantMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (q *Queries) SearchAssistantLexical(ctx context.Context, chatID int64, threadID *int32, query string, limit int) ([]GroupAssistantMessage, error) {
	rows, err := q.db.Query(ctx, `WITH terms AS (SELECT assistant_lexical_tokens($3) AS tokens)
 SELECT `+assistantRecallColumns()+` FROM group_assistant_messages m CROSS JOIN terms
 WHERE m.chat_id=$1 AND ($2::INT IS NULL OR m.thread_id=$2) AND `+assistantRecallValidity+`
 AND (m.lexical_tokens && terms.tokens OR strpos(lower(m.text),lower($3))>0)
 ORDER BY (CASE WHEN strpos(lower(m.text),lower($3))>0 THEN 4 ELSE 0 END +
 (SELECT COUNT(*)::FLOAT FROM unnest(terms.tokens) t WHERE t=ANY(m.lexical_tokens)) / greatest(1,cardinality(terms.tokens))) DESC,
 m.created_at DESC,m.id DESC LIMIT $4`, chatID, threadID, query, limit)
	if err != nil {
		return nil, err
	}
	return scanAssistantRecallRows(rows)
}
func (q *Queries) AssistantRecallByIDs(ctx context.Context, chatID int64, threadID *int32, ids []int64) ([]GroupAssistantMessage, error) {
	rows, err := q.db.Query(ctx, `SELECT `+assistantRecallColumns()+` FROM group_assistant_messages m
 WHERE m.chat_id=$1 AND ($2::INT IS NULL OR m.thread_id=$2) AND m.id=ANY($3::BIGINT[]) AND `+assistantRecallValidity+` ORDER BY array_position($3::BIGINT[],m.id)`, chatID, threadID, ids)
	if err != nil {
		return nil, err
	}
	return scanAssistantRecallRows(rows)
}
func (q *Queries) AssistantRecallNeighbors(ctx context.Context, anchor GroupAssistantMessage, radius int) ([]GroupAssistantMessage, error) {
	if radius <= 0 {
		return nil, nil
	}
	if radius > 4 {
		radius = 4
	}
	columns := assistantRecallColumns()
	base := ` FROM group_assistant_messages m WHERE m.chat_id=$1 AND m.thread_id=$2 AND ` + assistantRecallValidity
	query := `(SELECT ` + columns + base + ` AND (m.created_at,m.id)<($3,$4) ORDER BY m.created_at DESC,m.id DESC LIMIT $5)
 UNION (SELECT ` + columns + base + ` AND (m.created_at,m.id)>($3,$4) ORDER BY m.created_at,m.id LIMIT $5)
 UNION (SELECT ` + columns + base + ` AND (m.telegram_message_id::TEXT=$6 OR m.source_id=$7 OR ($6<>'' AND m.source_id=$6)) ORDER BY m.created_at,m.id LIMIT $8)`
	rows, err := q.db.Query(ctx, query, anchor.ChatID, anchor.ThreadID, anchor.CreatedAt, anchor.ID, radius, anchor.SourceID, fmt.Sprint(anchor.TelegramMessageID), radius*4+4)
	if err != nil {
		return nil, err
	}
	return scanAssistantRecallRows(rows)
}
func (q *Queries) PutAssistantMessageVector(ctx context.Context, message GroupAssistantMessage, modelRef string, vector []float64) error {
	// INSERT SELECT + source row lock serializes with concurrent edit/revocation;
	// its trigger removes any earlier vector in the same source transaction.
	return q.Transact(ctx, func(tx *Queries) error {
		var hash string
		err := tx.db.QueryRow(ctx, `SELECT m.content_hash FROM group_assistant_messages m WHERE m.id=$1 AND m.chat_id=$2 AND m.content_hash=$3 AND `+assistantRecallValidity+` FOR UPDATE`, message.ID, message.ChatID, message.ContentHash).Scan(&hash)
		if err != nil {
			return err
		}
		_, err = tx.db.Exec(ctx, `INSERT INTO group_assistant_message_vectors(message_id,model_ref,content_hash,embedding) VALUES($1,$2,$3,$4)
 ON CONFLICT(message_id,model_ref) DO UPDATE SET content_hash=EXCLUDED.content_hash,embedding=EXCLUDED.embedding,indexed_at=NOW()`, message.ID, modelRef, hash, vector)
		return err
	})
}
func (q *Queries) SearchAssistantSemantic(ctx context.Context, chatID int64, threadID *int32, modelRef string, vector []float64, limit int) ([]GroupAssistantMessage, error) {
	rows, err := q.db.Query(ctx, `SELECT `+assistantRecallColumns()+` FROM group_assistant_messages m
 JOIN group_assistant_message_vectors v ON v.message_id=m.id AND v.content_hash=m.content_hash
 CROSS JOIN LATERAL (SELECT SUM(x*y)/NULLIF(sqrt(SUM(x*x)*SUM(y*y)),0) AS score FROM unnest(v.embedding,$4::FLOAT8[]) AS pair(x,y)) similarity
 WHERE m.chat_id=$1 AND ($2::INT IS NULL OR m.thread_id=$2) AND v.model_ref=$3 AND cardinality(v.embedding)=cardinality($4::FLOAT8[])
 AND `+assistantRecallValidity+` AND similarity.score>=0.35 ORDER BY similarity.score DESC,m.id DESC LIMIT $5`, chatID, threadID, modelRef, vector, limit)
	if err != nil {
		return nil, err
	}
	return scanAssistantRecallRows(rows)
}
func (q *Queries) PendingAssistantVectors(ctx context.Context, chatID int64, modelRef string, limit int) ([]GroupAssistantMessage, error) {
	rows, err := q.db.Query(ctx, `SELECT `+assistantRecallColumns()+` FROM group_assistant_messages m WHERE m.chat_id=$1 AND `+assistantRecallValidity+`
 AND NOT EXISTS(SELECT 1 FROM group_assistant_message_vectors v WHERE v.message_id=m.id AND v.model_ref=$2 AND v.content_hash=m.content_hash)
 ORDER BY m.created_at,m.id LIMIT $3`, chatID, modelRef, limit)
	if err != nil {
		return nil, err
	}
	return scanAssistantRecallRows(rows)
}
func (q *Queries) AssistantRecallSourceValid(ctx context.Context, chatID, messageID int64, hash string) (bool, error) {
	var valid bool
	err := q.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM group_assistant_messages m WHERE m.chat_id=$1 AND m.telegram_message_id=$2 AND m.role='user' AND m.content_hash=$3 AND `+assistantRecallValidity+`)`, chatID, messageID, hash).Scan(&valid)
	return valid, err
}

func (q *Queries) LatestAssistantUserInThread(ctx context.Context, chatID int64, threadID int32) (GroupAssistantMessage, error) {
	return scanGroupAssistantMessage(q.db.QueryRow(ctx, `SELECT `+assistantRecallColumns()+` FROM group_assistant_messages m WHERE m.chat_id=$1 AND m.thread_id=$2 AND m.role='user' AND `+assistantRecallValidity+` ORDER BY m.created_at DESC,m.id DESC LIMIT 1`, chatID, threadID))
}

func (q *Queries) AssistantIndexChatIDs(ctx context.Context) ([]int64, error) {
	rows, err := q.db.Query(ctx, `SELECT p.chat_id FROM group_assistant_policies p JOIN authorized_groups a ON a.chat_id=p.chat_id AND a.enabled WHERE p.chat_enabled OR p.learning_enabled ORDER BY p.chat_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
