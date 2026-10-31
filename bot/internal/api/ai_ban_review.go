package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

var errReviewBusy = errors.New("review already in progress")

type banReviewOutcome struct {
	Decision store.AIDecision
	State    string // completed, manual_review, uncertain
	Unbanned bool
	Changed  float64
	Warnings []string
}
type banReviewError struct {
	status  int
	message string
}

func (e *banReviewError) Error() string { return e.message }
func banReviewFail(status int, message string) (banReviewOutcome, error) {
	return banReviewOutcome{}, &banReviewError{status, message}
}

func (s *Server) reviewAIBanFalsePositive(c echo.Context, id int64) error {
	ctx := c.Request().Context()
	d, err := s.botService.Queries().GetAIDecisionByID(ctx, id)
	if err != nil {
		return s.aiReviewFailure(c, err)
	}
	release, err := s.botService.AcquireModerationReview(ctx, d.ChatID, d.UserID)
	if err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": "处理正在进行，请稍后重试"})
	}
	defer release()
	out, err := s.reviewAIBanFalsePositiveLocked(ctx, id, 0, true)
	if err != nil {
		var re *banReviewError
		if errors.As(err, &re) {
			return c.JSON(re.status, map[string]string{"error": re.message})
		}
		return s.aiReviewFailure(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"decision": serializeAIDecision(out.Decision), "unban_performed": out.Unbanned, "unmute_performed": false, "trust_score_changed": out.Changed, "side_effect_warnings": out.Warnings})
}

// Caller must hold the same subject gate across dispatch and settlement.
func (s *Server) reviewAIBanFalsePositiveLocked(ctx context.Context, id int64, retries int, notify bool) (banReviewOutcome, error) {
	var decision store.AIDecision
	warnings := []string{}
	state := "manual_review"
	var token string
	var needsTelegram, needsSettle, unbanned bool
	var changed float64
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return banReviewFail(http.StatusInternalServerError, "无法创建复核操作，请重试")
	}
	candidate := hex.EncodeToString(random)
	q := s.botService.Queries()
	saveCompleted := func(tx *store.Queries) error {
		value := "false_positive"
		var e error
		decision, e = tx.UpdateAIDecisionOverride(ctx, id, &value)
		if e == nil {
			warnings = append(warnings, "此复核的解封已完成，本次仅保存结论；后续封禁不会自动解除，如存在请人工核查处理")
			state = "completed"
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
			case "dispatching":
				warnings = append(warnings, "Telegram 操作结果不明，请人工核查，系统不会自动重试")
				state = "uncertain"
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
		if e = tx.ClaimAIReview(ctx, id, token, notify); e != nil {
			return e
		}
		needsTelegram = true
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return banReviewFail(http.StatusNotFound, "decision not found")
	}
	if errors.Is(err, errReviewBusy) {
		return banReviewFail(http.StatusConflict, "复核正在执行，请稍后重试")
	}
	if err != nil {
		return banReviewOutcome{}, err
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
			return banReviewOutcome{}, err
		}
		if !valid {
			if retries > 0 {
				return banReviewFail(http.StatusConflict, "复核状态已变化，请核查后重试")
			}
			return s.reviewAIBanFalsePositiveLocked(ctx, id, retries+1, notify)
		}
		member, e := s.botService.TelegramBot().ChatMemberOf(&tele.Chat{ID: decision.ChatID}, &tele.User{ID: decision.UserID})
		if e != nil {
			_ = q.DeleteAIReviewClaim(ctx, id, token)
			return banReviewOutcome{}, e
		}
		if member == nil || member.User == nil || member.User.ID != decision.UserID || member.Role != tele.Kicked {
			_, _ = q.SetAIReviewClaimState(ctx, id, token, "conflict")
			return banReviewOutcome{Decision: decision, State: "manual_review", Warnings: []string{"Telegram 当前封禁状态与原处理不一致，请人工核查"}}, nil
		}
		marked, e := q.SetAIReviewClaimState(ctx, id, token, "dispatching")
		if e != nil || !marked {
			return banReviewOutcome{}, fmt.Errorf("record dispatch: %v", e)
		}
		err := s.botService.ReviewUnbanNoLock(ctx, decision.ChatID, decision.UserID)
		if err != nil {
			if telegramDefinitivelyRejected(err) {
				if e := q.DeleteAIReviewClaim(ctx, id, token); e != nil {
					s.logger.Warn("release AI review claim failed", zap.Error(e))
				}
			}
			return banReviewOutcome{}, err
		}
		ok, e := q.SetAIReviewClaimState(ctx, id, token, "telegram_done")
		if e != nil {
			return banReviewOutcome{}, e
		}
		if !ok {
			if retries > 0 {
				return banReviewFail(http.StatusConflict, "复核状态已变化，请核查后重试")
			}
			return s.reviewAIBanFalsePositiveLocked(ctx, id, retries+1, notify)
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
				if claim.NotifyEligible {
					if e = recordAIReviewReversal(ctx, tx, decision, "unban"); e != nil {
						return e
					}
				}
				unbanned = true
				state = "completed"
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
			return banReviewFail(http.StatusConflict, "复核正在执行，请稍后重试")
		}
		if err != nil {
			return banReviewOutcome{}, err
		}
	}
	return banReviewOutcome{Decision: decision, State: state, Unbanned: unbanned, Changed: changed, Warnings: warnings}, nil
}

func (s *Server) aiReviewFailure(c echo.Context, err error) error {
	s.logger.Warn("AI false positive review failed; retry required", zap.Error(err))
	return c.JSON(http.StatusInternalServerError, map[string]string{"error": "复核或解封失败，请重试"})
}
