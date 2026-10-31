package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
)

// An operation identity is created before calling Telegram and reused for every
// local outcome of that request. It must not be based on a user or timestamp.
func manualModerationOperationID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (s *Server) recordManualModerationCase(ctx context.Context, operation string, chatID, userID int64, action, result, explanation string, adminID int64) {
	q := s.botService.Queries()
	if b := s.botService.TelegramBot(); b != nil && b.Me != nil && b.Me.ID == userID {
		return
	}
	trust, err := q.GetUserTrust(ctx, chatID, userID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.logger.Warn("manual moderation target lookup failed", zap.Error(err))
		return
	}
	if err == nil && trust.IsBot {
		return
	}
	ownership, _ := json.Marshal(map[string]any{"operation_id": operation, "admin_id": adminID, "automatic_reversal": "manual_review"})
	_, err = q.RecordModerationCase(ctx, store.ModerationCaseInput{
		SourceKey: "admin_manual:" + operation, SourceKind: "admin_manual", SourceID: operation,
		ChatID: chatID, UserID: userID, Action: action, Result: result,
		Explanation: explanation, Ownership: ownership,
	})
	if err != nil {
		s.logger.Warn("manual moderation case write failed", zap.Error(err))
	}
}
