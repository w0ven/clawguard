package store

import "context"

// GetGroupByChatIDForUpdate must be called within a transaction. It serializes
// a read-modify-write with every SQL writer of the same group row.
func (q *Queries) GetGroupByChatIDForUpdate(ctx context.Context, chatID int64) (Group, error) {
	var g Group
	err := q.db.QueryRow(ctx, `SELECT id,chat_id,title,type,member_count,enabled,joined_at,config FROM groups WHERE chat_id=$1 FOR UPDATE`, chatID).Scan(&g.ID, &g.ChatID, &g.Title, &g.Type, &g.MemberCount, &g.Enabled, &g.JoinedAt, &g.Config)
	return g, err
}
