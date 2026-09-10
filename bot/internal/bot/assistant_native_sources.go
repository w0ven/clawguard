package bot

import (
	"context"
	"encoding/json"
)

// PG proves approval/ownership; native's source archive proves current source
// retention and tombstones. Transport receipts alone never extend recall TTL.
func (n *NativeAssistant) sourceVisible(ctx context.Context, groupID int64, topicID int32, messageID int64) (bool, error) {
	approved, err := n.a.queries.NativeAssistantSourceVisible(ctx, groupID, topicID, messageID)
	if err != nil {
		return false, err
	}
	owned, err := n.a.queries.NativeDeliveryOwned(ctx, groupID, topicID, messageID)
	if err != nil || (!approved && !owned) {
		return false, err
	}
	grant, err := n.backgroundGrant(ctx, groupID, topicID)
	if err != nil {
		return false, err
	}
	raw, err := n.request(ctx, "/sources/check", map[string]any{"group_id": groupID, "topic_id": topicID, "background_grant": grant, "message_id": messageID})
	if err != nil {
		return false, err
	}
	var value struct {
		Visible bool `json:"visible"`
	}
	if err = json.Unmarshal(raw, &value); err != nil {
		return false, err
	}
	return value.Visible, nil
}
