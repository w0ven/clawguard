package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/redact"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

type joinProtectionDecision struct {
	Protect         bool
	Entered         bool
	Notify          bool
	Trigger         string
	ProtectionUntil time.Time
	Intercepted     int
}

type joinProtectionSummary struct {
	ProtectionUntil time.Time
	Intercepted     int
}

type joinProtectionGroupState struct {
	mu               sync.Mutex
	joins            []time.Time
	protectionUntil  time.Time
	lastNotification time.Time
	intercepted      int
	estimatedPending int
}

type joinProtector struct {
	groups sync.Map
}

func newJoinProtector() *joinProtector {
	return &joinProtector{}
}

func (p *joinProtector) group(chatID int64) *joinProtectionGroupState {
	state, _ := p.groups.LoadOrStore(chatID, &joinProtectionGroupState{})
	return state.(*joinProtectionGroupState)
}

func (p *joinProtector) ObserveJoin(chatID int64, policy config.JoinProtectionPolicy, databasePending int, now time.Time) joinProtectionDecision {
	if !policy.Enabled {
		p.groups.Delete(chatID)
		return joinProtectionDecision{}
	}

	state := p.group(chatID)
	state.mu.Lock()
	defer state.mu.Unlock()

	if databasePending > state.estimatedPending {
		state.estimatedPending = databasePending
	}
	if !state.protectionUntil.IsZero() && !now.Before(state.protectionUntil) {
		state.protectionUntil = time.Time{}
		state.lastNotification = time.Time{}
		state.intercepted = 0
		state.joins = nil
	}

	decision := joinProtectionDecision{}
	if now.Before(state.protectionUntil) {
		decision.Protect = true
		decision.Trigger = "active"
	} else {
		windowStart := now.Add(-time.Duration(policy.JoinWindowSeconds) * time.Second)
		firstCurrent := 0
		for firstCurrent < len(state.joins) && state.joins[firstCurrent].Before(windowStart) {
			firstCurrent++
		}
		if firstCurrent > 0 {
			state.joins = append([]time.Time(nil), state.joins[firstCurrent:]...)
		}
		state.joins = append(state.joins, now)
	}

	if !decision.Protect && len(state.joins) >= policy.JoinThreshold {
		decision.Protect = true
		decision.Entered = true
		decision.Trigger = "join_threshold"
	} else if !decision.Protect && state.estimatedPending+1 >= policy.MaxPendingVerifications {
		decision.Protect = true
		decision.Entered = true
		decision.Trigger = "pending_limit"
	}

	if decision.Protect {
		if decision.Entered {
			state.protectionUntil = now.Add(time.Duration(policy.ProtectionDurationSeconds) * time.Second)
			state.intercepted = 0
			state.joins = nil
		}
		state.intercepted++
		decision.ProtectionUntil = state.protectionUntil
		decision.Intercepted = state.intercepted
		if decision.Entered || state.lastNotification.IsZero() || now.Sub(state.lastNotification) >= time.Duration(policy.AdminNotifyIntervalSeconds)*time.Second {
			decision.Notify = true
			state.lastNotification = now
		}
		return decision
	}

	state.estimatedPending++
	return decision
}

func pendingReservesJoinProtectionSlot(method string) bool {
	switch method {
	case "cleanup", "join_protection_cleanup":
		return false
	default:
		return true
	}
}

func (p *joinProtector) ReleasePending(chatID int64) {
	value, ok := p.groups.Load(chatID)
	if !ok {
		return
	}
	state := value.(*joinProtectionGroupState)
	state.mu.Lock()
	if state.estimatedPending > 0 {
		state.estimatedPending--
	}
	state.mu.Unlock()
}

func (p *joinProtector) FinishProtection(chatID int64, expectedUntil, now time.Time) (joinProtectionSummary, bool) {
	value, ok := p.groups.Load(chatID)
	if !ok {
		return joinProtectionSummary{}, false
	}
	state := value.(*joinProtectionGroupState)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.protectionUntil.IsZero() || !state.protectionUntil.Equal(expectedUntil) || now.Before(state.protectionUntil) {
		return joinProtectionSummary{}, false
	}
	summary := joinProtectionSummary{ProtectionUntil: state.protectionUntil, Intercepted: state.intercepted}
	state.protectionUntil = time.Time{}
	state.lastNotification = time.Time{}
	state.intercepted = 0
	state.joins = nil
	return summary, true
}

type telegramCleanupGroupState struct {
	mu            sync.Mutex
	cooldownUntil time.Time
	failures      int
}

type telegramCleanupBreaker struct {
	groups sync.Map
}

type promptFailureCleanupOps struct {
	applyAction func() (string, error)
	persist     func() error
}

type joinProtectionBanOps struct {
	allow          func() bool
	temporaryBan   func() error
	persistCleanup func(string) error
	success        func()
	fail           func(error)
}

func runPromptFailureCleanup(ops promptFailureCleanupOps) (string, error) {
	action, err := ops.applyAction()
	if err == nil || isTerminalTelegramCleanupError(err) {
		if err != nil {
			return "already_absent", nil
		}
		return action, nil
	}
	if persistErr := ops.persist(); persistErr != nil {
		return "", errors.Join(err, fmt.Errorf("persist cleanup after telegram failure: %w", persistErr))
	}
	return "", err
}

func runJoinProtectionBan(ops joinProtectionBanOps) error {
	if !ops.allow() {
		return ops.persistCleanup("join_protection_cleanup_deferred_during_cooldown")
	}

	err := ops.temporaryBan()
	if err == nil || isTerminalTelegramCleanupError(err) {
		ops.success()
		return nil
	}

	persistErr := ops.persistCleanup("join_protection_temporary_ban_failed")
	ops.fail(err)
	if persistErr != nil {
		return errors.Join(err, fmt.Errorf("persist join protection cleanup: %w", persistErr))
	}
	return err
}

func newTelegramCleanupBreaker() *telegramCleanupBreaker {
	return &telegramCleanupBreaker{}
}

func (b *telegramCleanupBreaker) group(chatID int64) *telegramCleanupGroupState {
	state, _ := b.groups.LoadOrStore(chatID, &telegramCleanupGroupState{})
	return state.(*telegramCleanupGroupState)
}

func (b *telegramCleanupBreaker) Allow(chatID int64, now time.Time) (bool, time.Time) {
	state := b.group(chatID)
	state.mu.Lock()
	defer state.mu.Unlock()
	return !now.Before(state.cooldownUntil), state.cooldownUntil
}

func (b *telegramCleanupBreaker) Success(chatID int64) {
	value, ok := b.groups.Load(chatID)
	if !ok {
		return
	}
	state := value.(*telegramCleanupGroupState)
	state.mu.Lock()
	state.cooldownUntil = time.Time{}
	state.failures = 0
	state.mu.Unlock()
}

func (b *telegramCleanupBreaker) Fail(chatID int64, baseCooldown time.Duration, err error, now time.Time) (time.Time, bool) {
	state := b.group(chatID)
	state.mu.Lock()
	defer state.mu.Unlock()
	if now.Before(state.cooldownUntil) {
		return state.cooldownUntil, false
	}
	state.failures++
	delay := baseCooldown
	for attempt := 1; attempt < state.failures && delay < time.Hour; attempt++ {
		delay *= 2
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	if retryAfter, ok := floodRetryAfter(err); ok {
		retryDelay := time.Duration(retryAfter+1) * time.Second
		if retryDelay > delay {
			delay = retryDelay
		}
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	state.cooldownUntil = now.Add(delay)
	return state.cooldownUntil, true
}

func isTerminalTelegramCleanupError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	var telegramError *tele.Error
	if errors.As(err, &telegramError) && telegramError != nil {
		message += " " + strings.ToLower(telegramError.Description+" "+telegramError.Message)
	}
	for _, marker := range []string{
		"user not found",
		"participant_id_invalid",
		"user_id_invalid",
		"member not found",
		"not a member",
		"user not participant",
		"user_not_participant",
		"participant not found",
		"member has left",
		"already kicked",
		"already banned",
		"chat not found",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func formatJoinProtectionTrigger(trigger string) string {
	switch trigger {
	case "pending_limit":
		return "待验证人数达到上限"
	case "join_threshold":
		return "短时间入群人数达到阈值"
	default:
		return "防护仍在持续"
	}
}

func formatRemaining(until, now time.Time) string {
	remaining := until.Sub(now)
	if remaining < 0 {
		remaining = 0
	}
	minutes := int((remaining + time.Minute - 1) / time.Minute)
	return fmt.Sprintf("约 %d 分钟", minutes)
}

func (s *Service) ensureRuntimeGuards() {
	s.runtimeGuardsOnce.Do(func() {
		if s.joinProtector == nil {
			s.joinProtector = newJoinProtector()
		}
		if s.cleanupBreaker == nil {
			s.cleanupBreaker = newTelegramCleanupBreaker()
		}
	})
}

func (s *Service) temporaryBanJoinFloodUser(chat *tele.Chat, user *tele.User, seconds int) error {
	if chat == nil || user == nil {
		return nil
	}
	member := &tele.ChatMember{
		User:            user,
		RestrictedUntil: time.Now().Add(time.Duration(seconds) * time.Second).Unix(),
	}
	if err := s.bot.Ban(chat, member, true); err != nil {
		return normalizeTelegramActionError("ban", err)
	}
	return nil
}

func (s *Service) handleJoinProtectionBan(ctx context.Context, chat *tele.Chat, user *tele.User, policy config.JoinProtectionPolicy) error {
	persistCleanup := func(reason string) error {
		payload, err := json.Marshal(map[string]any{
			"reason":                reason,
			"temporary_ban_seconds": policy.TemporaryBanSeconds,
		})
		if err != nil {
			return fmt.Errorf("marshal join protection cleanup: %w", err)
		}
		_, err = s.queries.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{
			ChatID:    chat.ID,
			UserID:    user.ID,
			Username:  stringPtr(user.Username),
			FirstName: stringPtr(user.FirstName),
			Method:    "join_protection_cleanup",
			Payload:   payload,
			ExpiresAt: time.Now().Add(-time.Second),
		})
		return err
	}

	s.ensureRuntimeGuards()
	return runJoinProtectionBan(joinProtectionBanOps{
		allow: func() bool {
			allowed, _ := s.cleanupBreaker.Allow(chat.ID, time.Now())
			return allowed
		},
		temporaryBan: func() error {
			return s.temporaryBanJoinFloodUser(chat, user, policy.TemporaryBanSeconds)
		},
		persistCleanup: persistCleanup,
		success: func() {
			s.cleanupBreaker.Success(chat.ID)
		},
		fail: func(err error) {
			s.openTelegramCleanupCooldown(ctx, chat, policy, err)
		},
	})
}

func (s *Service) notifyJoinProtectionAdmins(ctx context.Context, chat *tele.Chat, decision joinProtectionDecision, now time.Time) {
	message := fmt.Sprintf(
		"🛡️ <b>入群防护已触发</b>\n原因：%s\n已拦截：%d 人\n预计剩余：%s\n\n防护期间不发送验证图、不调用 CAS/Bio/AI，也不会逐人刷屏；后续新人将被临时封禁。",
		htmlEscape(formatJoinProtectionTrigger(decision.Trigger)),
		decision.Intercepted,
		htmlEscape(formatRemaining(decision.ProtectionUntil, now)),
	)
	s.notifyAdminsForChat(ctx, chat, message, "join protection notification")
}

func (s *Service) notifyJoinProtectionRecovery(ctx context.Context, chat *tele.Chat, summary joinProtectionSummary) {
	message := fmt.Sprintf("✅ <b>入群防护已自动结束</b>\n本次共拦截 %d 人，现已恢复正常入群验证。", summary.Intercepted)
	s.notifyAdminsForChat(ctx, chat, message, "join protection recovery")
}

func (s *Service) notifyAdminsForChat(ctx context.Context, chat *tele.Chat, message, event string) {
	recipients := map[int64]struct{}{}
	if s.queries != nil {
		admins, err := s.queries.ListAdmins(ctx)
		if err != nil {
			s.logger.Warn("list admins for telegram notification failed", zap.Error(err), zap.String("event", event))
		} else {
			chatID := int64(0)
			if chat != nil {
				chatID = chat.ID
			}
			for _, admin := range admins {
				if admin.TelegramID != 0 && botAdminCanAccessChat(admin, chatID) {
					recipients[admin.TelegramID] = struct{}{}
				}
			}
		}
	}
	for _, telegramID := range s.cfg.AdminTelegramIDs {
		if telegramID != 0 {
			recipients[telegramID] = struct{}{}
		}
	}
	text := fmt.Sprintf("%s\n群：%s", message, formatOwnerWarnChatLabel(chat))
	for telegramID := range recipients {
		if err := s.SendHTMLPrivateMessage(telegramID, text); err != nil {
			s.logger.Warn("send admin telegram notification failed", zap.Error(err), zap.Int64("telegram_id", telegramID), zap.String("event", event))
		}
	}
}

func (s *Service) handleVerificationPromptFailure(ctx context.Context, chat *tele.Chat, user *tele.User, policy config.GuardPolicy, promptErr error) {
	if chat == nil || user == nil {
		return
	}
	payload, _ := json.Marshal(map[string]string{"reason": "verification_prompt_failed"})
	persistCleanup := func() error {
		_, err := s.queries.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{
			ChatID:    chat.ID,
			UserID:    user.ID,
			Username:  stringPtr(user.Username),
			FirstName: stringPtr(user.FirstName),
			Method:    "cleanup",
			Payload:   payload,
			ExpiresAt: time.Now().Add(-time.Second),
		})
		return err
	}
	s.ensureRuntimeGuards()
	if allowed, _ := s.cleanupBreaker.Allow(chat.ID, time.Now()); !allowed {
		if err := persistCleanup(); err != nil {
			s.logger.Error("persist prompt failure during telegram cooldown failed; member remains restricted", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		}
		return
	}
	action, cleanupErr := runPromptFailureCleanup(promptFailureCleanupOps{
		applyAction: func() (string, error) {
			return s.applyVerificationFailAction(chat, user, policy.Verify.FailAction)
		},
		persist: persistCleanup,
	})
	if cleanupErr == nil {
		s.cleanupBreaker.Success(chat.ID)
		if _, err := s.queries.InsertViolation(ctx, store.InsertViolationParams{
			ChatID:   chat.ID,
			UserID:   user.ID,
			Username: stringPtr(user.Username),
			Rule:     "verification_prompt_failed",
			Matched:  stringPtr(redact.ErrorString(promptErr)),
			Action:   action,
		}); err != nil {
			s.logger.Warn("record verification prompt failure cleanup failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		}
		return
	}

	s.openTelegramCleanupCooldown(ctx, chat, policy.JoinProtection, cleanupErr)
}

func (s *Service) openTelegramCleanupCooldown(ctx context.Context, chat *tele.Chat, policy config.JoinProtectionPolicy, err error) {
	s.ensureRuntimeGuards()
	chatID := int64(0)
	if chat != nil {
		chatID = chat.ID
	}
	until, notify := s.cleanupBreaker.Fail(chatID, time.Duration(policy.TelegramFailureCooldownSeconds)*time.Second, err, time.Now())
	if !notify {
		return
	}
	message := fmt.Sprintf("⚠️ <b>Telegram 清理暂时失败</b>\n该群已暂停重复清理请求，%s 后自动重试。期间相关账号保持受限。", htmlEscape(formatRemaining(until, time.Now())))
	s.notifyAdminsForChat(ctx, chat, message, "telegram cleanup cooldown")
}
