package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
)

var errReviewBusy = errors.New("review already in progress")

func (s *Server) reviewAIBanFalsePositive(c echo.Context, id int64) error {
	return s.reviewAIBanFalsePositiveAttempt(c, id, 0)
}
func (s *Server) reviewAIBanFalsePositiveAttempt(c echo.Context, id int64, retries int) error {
	ctx := c.Request().Context()
	var decision store.AIDecision
	warnings := []string{}
	var token string
	var needsTelegram, needsSettle, unbanned bool
	var changed float64
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "无法创建复核操作，请重试"})
	}
	candidate := hex.EncodeToString(random)
	q := s.botService.Queries()
	saveCompleted := func(tx *store.Queries) error {
		value := "false_positive"
		var e error
		decision, e = tx.UpdateAIDecisionOverride(ctx, id, &value)
		if e == nil {
			warnings = append(warnings, "此复核的解封已完成，本次仅保存结论；后续封禁不会自动解除，如存在请人工核查处理")
		}
		return e
	}
	err := q.Transact(ctx, func(tx *store.Queries) error {
		if e := tx.LockAIDecisionReview(ctx, id); e != nil {
			return e
		}
		var e error
		decision, e = tx.GetAIDecisionByID(ctx, id)
		if e != nil {
			return e
		}
		owned, status, e := tx.LockAIBanOwnership(ctx, decision.ChatID, decision.UserID, id)
		if e != nil {
			return e
		}
		claim, e := tx.GetAIReviewClaim(ctx, id)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		hasClaim := e == nil
		if hasClaim {
			switch claim.State {
			case "completed":
				return saveCompleted(tx)
			case "conflict":
				if _, e = tx.InsertFalsePositiveReview(ctx, id); e != nil {
					return e
				}
				value := "false_positive"
				decision, e = tx.UpdateAIDecisionOverride(ctx, id, &value)
				if e != nil {
					return e
				}
				warnings = append(warnings, "复核与后续封禁发生冲突，请核查 Telegram 并手动处理")
				return nil
			case "telegram_done":
				token = claim.Token
				needsSettle = true
				return nil
			case "claimed":
				if claim.LeaseActive {
					return errReviewBusy
				}
			}
		}
		previouslyReviewed, e := tx.HasFalsePositiveReview(ctx, id)
		if e != nil {
			return e
		}
		if !owned || previouslyReviewed {
			if !owned && (!previouslyReviewed || status == "banned") || owned && previouslyReviewed {
				warnings = append(warnings, "无法确认当前封禁归属，请使用手动解封")
			}
			if _, e = tx.InsertFalsePositiveReview(ctx, id); e != nil {
				return e
			}
			value := "false_positive"
			decision, e = tx.UpdateAIDecisionOverride(ctx, id, &value)
			return e
		}
		token = candidate
		if e = tx.ClaimAIReview(ctx, id, token); e != nil {
			return e
		}
		needsTelegram = true
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "decision not found"})
	}
	if errors.Is(err, errReviewBusy) {
		return c.JSON(http.StatusConflict, map[string]string{"error": "复核正在执行，请稍后重试"})
	}
	if err != nil {
		return s.aiReviewFailure(c, err)
	}
	if needsTelegram {
		// A short, separate recheck reduces the window between claiming and dispatch.
		valid := false
		err = q.Transact(ctx, func(tx *store.Queries) error {
			if e := tx.LockAIDecisionReview(ctx, id); e != nil {
				return e
			}
			owned, _, e := tx.LockAIBanOwnership(ctx, decision.ChatID, decision.UserID, id)
			if e != nil {
				return e
			}
			claim, e := tx.GetAIReviewClaim(ctx, id)
			if e != nil {
				return e
			}
			if claim.Token != token || claim.State != "claimed" {
				return nil
			}
			valid = owned
			if !owned {
				_, e = tx.SetAIReviewClaimState(ctx, id, token, "conflict")
			}
			return e
		})
		if err != nil {
			return s.aiReviewFailure(c, err)
		}
		if !valid {
			if retries > 0 {
				return c.JSON(http.StatusConflict, map[string]string{"error": "复核状态已变化，请核查后重试"})
			}
			return s.reviewAIBanFalsePositiveAttempt(c, id, retries+1)
		}
		if err = s.botService.UnbanChatUser(ctx, decision.ChatID, decision.UserID); err != nil {
			// A failed Telegram request is not assumed successful; release if possible.
			if e := q.DeleteAIReviewClaim(ctx, id, token); e != nil {
				s.logger.Warn("release AI review claim failed", zap.Error(e))
			}
			return s.aiReviewFailure(c, err)
		}
		ok, e := q.SetAIReviewClaimState(ctx, id, token, "telegram_done")
		if e != nil {
			return s.aiReviewFailure(c, e)
		}
		if !ok {
			if retries > 0 {
				return c.JSON(http.StatusConflict, map[string]string{"error": "复核状态已变化，请核查后重试"})
			}
			return s.reviewAIBanFalsePositiveAttempt(c, id, retries+1)
		}
		needsSettle = true
	}
	if needsSettle {
		err = q.Transact(ctx, func(tx *store.Queries) error {
			if e := tx.LockAIDecisionReview(ctx, id); e != nil {
				return e
			}
			var e error
			decision, e = tx.GetAIDecisionByID(ctx, id)
			if e != nil {
				return e
			}
			owned, _, e := tx.LockAIBanOwnership(ctx, decision.ChatID, decision.UserID, id)
			if e != nil {
				return e
			}
			claim, e := tx.GetAIReviewClaim(ctx, id)
			if e != nil {
				return e
			}
			if claim.State == "completed" {
				return saveCompleted(tx)
			}
			if claim.Token != token || claim.State == "conflict" {
				warnings = append(warnings, "Telegram 解封与后续封禁发生冲突，请核查 Telegram 并手动恢复")
				return nil
			}
			if claim.State != "telegram_done" {
				return errReviewBusy
			}
			if !owned {
				if _, e = tx.SetAIReviewClaimState(ctx, id, token, "conflict"); e != nil {
					return e
				}
				warnings = append(warnings, "Telegram 解封与后续封禁发生冲突，请核查 Telegram 并手动恢复")
			} else {
				if _, e = tx.UnbanUserTrust(ctx, store.UnbanUserTrustParams{ChatID: decision.ChatID, UserID: decision.UserID, Score: 0.5}); e != nil {
					return e
				}
				unbanned = true
			}
			first, e := tx.InsertFalsePositiveReview(ctx, id)
			if e != nil {
				return e
			}
			if first && owned && (decision.AdminOverride == nil || *decision.AdminOverride != "false_positive") {
				trust, e := tx.AdjustUserTrustScore(ctx, store.AdjustUserTrustScoreParams{ChatID: decision.ChatID, UserID: decision.UserID, Delta: 0.1})
				if e != nil {
					return e
				}
				changed = trust.Score - 0.5
			}
			value := "false_positive"
			decision, e = tx.UpdateAIDecisionOverride(ctx, id, &value)
			if e != nil {
				return e
			}
			if owned {
				_, e = tx.SetAIReviewClaimState(ctx, id, token, "completed")
			}
			return e
		})
		if errors.Is(err, errReviewBusy) {
			return c.JSON(http.StatusConflict, map[string]string{"error": "复核正在执行，请稍后重试"})
		}
		if err != nil {
			return s.aiReviewFailure(c, err)
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"decision": serializeAIDecision(decision), "unban_performed": unbanned, "unmute_performed": false, "trust_score_changed": changed, "side_effect_warnings": warnings})
}

func (s *Server) aiReviewFailure(c echo.Context, err error) error {
	s.logger.Warn("AI false positive review failed; retry required", zap.Error(err))
	return c.JSON(http.StatusInternalServerError, map[string]string{"error": "复核或解封失败，请重试"})
}
