package api

import (
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	"net/http"
)

func (s *Server) reviewAIBanFalsePositive(c echo.Context, id int64) error {
	ctx := c.Request().Context()
	var decision store.AIDecision
	warnings := []string{}
	unbanned := false
	changed := 0.0
	err := s.botService.Queries().Transact(ctx, func(q *store.Queries) error {
		if err := q.LockAIDecisionReview(ctx, id); err != nil {
			return err
		}
		previous, err := q.GetAIDecisionByID(ctx, id)
		if err != nil {
			return err
		}
		owned, status, err := q.LockAIBanOwnership(ctx, previous.ChatID, previous.UserID, id)
		if err != nil {
			return err
		}
		if owned {
			if err = s.botService.UnbanChatUser(ctx, previous.ChatID, previous.UserID); err != nil {
				return err
			}
			if _, err = q.UnbanUserTrust(ctx, store.UnbanUserTrustParams{ChatID: previous.ChatID, UserID: previous.UserID, Score: 0.5}); err != nil {
				return err
			}
			unbanned = true
		}
		first, err := q.InsertFalsePositiveReview(ctx, id)
		if err != nil {
			return err
		}
		if !owned && (first || status == "banned") {
			warnings = append(warnings, "无法确认当前封禁归属，请使用手动解封")
		}
		if first && owned && (previous.AdminOverride == nil || *previous.AdminOverride != "false_positive") {
			trust, err := q.AdjustUserTrustScore(ctx, store.AdjustUserTrustScoreParams{ChatID: previous.ChatID, UserID: previous.UserID, Delta: 0.1})
			if err != nil {
				return err
			}
			changed = trust.Score - 0.5
		}
		value := "false_positive"
		decision, err = q.UpdateAIDecisionOverride(ctx, id, &value)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "decision not found"})
	}
	if err != nil {
		s.logger.Warn("AI false positive review failed; retry required", zap.Error(err))
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "复核或解封失败，请重试"})
	}
	return c.JSON(http.StatusOK, map[string]any{"decision": serializeAIDecision(decision), "unban_performed": unbanned, "unmute_performed": false, "trust_score_changed": changed, "side_effect_warnings": warnings})
}
