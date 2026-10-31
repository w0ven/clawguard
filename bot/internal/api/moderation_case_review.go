package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openclaw/clawguard/internal/store"
)

func recordAIReviewReversal(ctx context.Context, tx *store.Queries, d store.AIDecision, action string) error {
	id := fmt.Sprintf("%d:%s", d.ID, action)
	ownership, _ := json.Marshal(map[string]any{"ai_decision_id": d.ID, "automatic_reversal": "manual_review"})
	_, err := tx.RecordModerationCase(ctx, store.ModerationCaseInput{SourceKey: "ai_review:" + id, SourceKind: "ai_review", SourceID: id, ChatID: d.ChatID, UserID: d.UserID, Action: action, Result: "reversed", Explanation: "原处理经复核后已撤销，其他限制不受影响。", Ownership: ownership})
	return err
}
