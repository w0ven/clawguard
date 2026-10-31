package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
)

// reviewAIMute uses a durable claim and never keeps a DB transaction over Telegram.
// Mismatched or historical restrictions are deliberately left to an administrator.
func (s *Server) reviewAIMute(ctx context.Context, d store.AIDecision) (bool, string) {
	release, err := s.botService.AcquireModerationReview(ctx, d.ChatID, d.UserID)
	if err != nil {
		return false, "处理正在进行，请稍后重试"
	}
	defer release()
	return s.reviewAIMuteLocked(ctx, d, true)
}

// reviewAIMuteLocked requires the subject gate to remain held through settlement.
func (s *Server) reviewAIMuteLocked(ctx context.Context, d store.AIDecision, notify bool) (bool, string) {
	q := s.botService.Queries()
	raw := make([]byte, 16)
	if _, e := rand.Read(raw); e != nil {
		return false, "无法创建禁言复核，请重试"
	}
	token := hex.EncodeToString(raw)
	var owner store.AIMuteOwner
	var claim store.AIMuteClaim
	var settle, dispatch bool
	warning := ""
	err := q.Transact(ctx, func(tx *store.Queries) error {
		if e := tx.LockAIDecisionReview(ctx, d.ID); e != nil {
			return e
		}
		current, e := tx.GetAIDecisionByID(ctx, d.ID)
		if e != nil {
			return e
		}
		if current.AdminOverride == nil || *current.AdminOverride != "false_positive" {
			return fmt.Errorf("conclusion changed")
		}
		owner, e = tx.LockAIMuteOwner(ctx, d.ChatID, d.UserID)
		if errors.Is(e, pgx.ErrNoRows) {
			warning = "无法确认当前禁言归属，请人工核查并解除"
			return nil
		}
		if e != nil {
			return e
		}
		claim, e = tx.LockAIMuteClaim(ctx, d.ID)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		hasClaim := e == nil
		if hasClaim && claim.State == "completed" {
			warning = "原禁言复核已完成，本次仅保存结论；后续限制不会自动解除，如存在请人工核查"
			return nil
		}
		if hasClaim && claim.State == "dispatching" {
			warning = "Telegram 操作结果不明，请人工核查，系统不会自动重试"
			return nil
		}
		if hasClaim && claim.State == "conflict" {
			warning = "禁言复核与后续限制冲突，请人工核查"
			return nil
		}
		if owner.DecisionID != d.ID || (owner.State != "active" && owner.State != "reviewing") {
			warning = "无法确认当前禁言归属，请人工核查并解除"
			return nil
		}
		if hasClaim && claim.State == "telegram_done" {
			token = claim.Token
			settle = true
			return nil
		}
		if hasClaim && claim.State == "claimed" && claim.LeaseActive {
			warning = "禁言复核正在执行，请稍后重试"
			return nil
		}
		if e = tx.ClaimAIMuteReview(ctx, d.ChatID, d.UserID, d.ID, token, notify); e != nil {
			return e
		}
		dispatch = true
		return nil
	})
	if err != nil {
		return false, "保存禁言复核状态失败，请重试"
	}
	if warning != "" {
		return false, warning
	}
	if dispatch {
		// Telegram exposes no provenance token; require the exact restriction we issued.
		member, e := s.botService.TelegramBot().ChatMemberOf(&tele.Chat{ID: d.ChatID}, &tele.User{ID: d.UserID})
		if e != nil {
			_ = q.ReleaseAIMuteClaim(ctx, d.ID, token)
			return false, "核查 Telegram 禁言失败，请重试"
		}
		if member == nil || member.User == nil || member.User.ID != d.UserID || member.Role != tele.Restricted || member.RestrictedUntil != owner.Until || member.Rights != tele.NoRights() || owner.Until <= time.Now().Unix() {
			_ = q.Transact(ctx, func(tx *store.Queries) error {
				if e := tx.LockAIDecisionReview(ctx, d.ID); e != nil {
					return e
				}
				_, e := tx.UpdateAIMuteClaim(ctx, d.ID, token, "conflict")
				if e != nil {
					return e
				}
				_, e = tx.SetAIMuteOwnerState(ctx, d.ChatID, d.UserID, d.ID, "conflict")
				return e
			})
			return false, "当前禁言与原 AI 限制不一致，请人工核查"
		}
		valid := false
		e = q.Transact(ctx, func(tx *store.Queries) error {
			if e := tx.LockAIDecisionReview(ctx, d.ID); e != nil {
				return e
			}
			current, e := tx.GetAIDecisionByID(ctx, d.ID)
			if e != nil {
				return e
			}
			o, e := tx.LockAIMuteOwner(ctx, d.ChatID, d.UserID)
			if e != nil {
				return e
			}
			cl, e := tx.LockAIMuteClaim(ctx, d.ID)
			if e != nil {
				return e
			}
			valid = current.AdminOverride != nil && *current.AdminOverride == "false_positive" && o.DecisionID == d.ID && o.Token == owner.Token && o.Until == owner.Until && o.State == "reviewing" && cl.Token == token && cl.State == "claimed"
			return nil
		})
		if e != nil {
			return false, "复核状态检查失败，请重试"
		}
		if !valid {
			return false, "禁言复核与后续操作冲突，请人工核查"
		}
		marked, e := q.UpdateAIMuteClaim(ctx, d.ID, token, "dispatching")
		if e != nil || !marked {
			return false, "保存操作状态失败，请重试"
		}
		if e = s.botService.ReviewUnmuteNoLock(ctx, d.ChatID, d.UserID); e != nil {
			if telegramDefinitivelyRejected(e) {
				_ = q.ReleaseAIMuteClaim(ctx, d.ID, token)
				return false, "解除禁言失败，请重试"
			}
			return false, "Telegram 操作结果不明，请人工核查"
		}
		ok, e := q.UpdateAIMuteClaim(ctx, d.ID, token, "telegram_done")
		if e != nil {
			return false, "Telegram 已解除禁言，但记录失败，请核查后重试"
		}
		if !ok {
			return false, "Telegram 解禁与后续限制冲突，请人工核查"
		}
		settle = true
	}
	if !settle {
		return false, warning
	}
	performed := false
	err = q.Transact(ctx, func(tx *store.Queries) error {
		if e := tx.LockAIDecisionReview(ctx, d.ID); e != nil {
			return e
		}
		o, e := tx.LockAIMuteOwner(ctx, d.ChatID, d.UserID)
		if e != nil {
			return e
		}
		cl, e := tx.LockAIMuteClaim(ctx, d.ID)
		if e != nil {
			return e
		}
		if cl.State == "completed" {
			return nil
		}
		if o.DecisionID != d.ID || o.Token != owner.Token || o.Until != owner.Until || o.State != "reviewing" || cl.Token != token || cl.State != "telegram_done" {
			warning = "Telegram 解禁与后续限制冲突，请人工核查"
			return nil
		}
		ok, e := tx.SetAIMuteOwnerState(ctx, d.ChatID, d.UserID, d.ID, "done")
		if e != nil || !ok {
			return fmt.Errorf("settle mute owner: %w", e)
		}
		ok, e = tx.UpdateAIMuteClaim(ctx, d.ID, token, "completed")
		if e != nil || !ok {
			return fmt.Errorf("settle mute claim: %w", e)
		}
		if cl.NotifyEligible {
			if e = recordAIReviewReversal(ctx, tx, d, "unmute"); e != nil {
				return e
			}
		}
		performed = true
		return nil
	})
	if err != nil {
		return false, "禁言已解除但结算失败，请重试"
	}
	return performed, warning
}
