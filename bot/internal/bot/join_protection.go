package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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
	currentTrigger   string
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
		state.currentTrigger = ""
		state.lastNotification = time.Time{}
		state.intercepted = 0
		state.joins = nil
	}

	decision := joinProtectionDecision{}
	if now.Before(state.protectionUntil) {
		decision.Protect = true
		decision.Trigger = state.currentTrigger
		if decision.Trigger == "" {
			decision.Trigger = "active"
		}
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
			state.currentTrigger = decision.Trigger
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

type joinProtectionMemorySnapshot struct {
	Joins            []time.Time
	ProtectionUntil  time.Time
	CurrentTrigger   string
	LastNotification time.Time
	Intercepted      int
}

func (p *joinProtector) Snapshot(chatID int64, policy config.JoinProtectionPolicy, now time.Time) (joinProtectionMemorySnapshot, bool) {
	value, ok := p.groups.Load(chatID)
	if !ok {
		return joinProtectionMemorySnapshot{}, false
	}
	state := value.(*joinProtectionGroupState)
	state.mu.Lock()
	defer state.mu.Unlock()

	if !state.protectionUntil.IsZero() && !now.Before(state.protectionUntil) {
		state.protectionUntil = time.Time{}
		state.currentTrigger = ""
		state.lastNotification = time.Time{}
		state.intercepted = 0
	}
	windowStart := now.Add(-time.Duration(policy.JoinWindowSeconds) * time.Second)
	firstCurrent := 0
	for firstCurrent < len(state.joins) && state.joins[firstCurrent].Before(windowStart) {
		firstCurrent++
	}
	if firstCurrent > 0 {
		state.joins = append([]time.Time(nil), state.joins[firstCurrent:]...)
	}
	snapshot := joinProtectionMemorySnapshot{
		Joins:            append([]time.Time(nil), state.joins...),
		ProtectionUntil:  state.protectionUntil,
		CurrentTrigger:   state.currentTrigger,
		LastNotification: state.lastNotification,
		Intercepted:      state.intercepted,
	}
	return snapshot, !snapshot.ProtectionUntil.IsZero() || len(snapshot.Joins) > 0
}

func (p *joinProtector) MirrorRedisObservation(chatID int64, observation joinProtectionRedisObservation, databasePending *int, now time.Time) {
	state := p.group(chatID)
	state.mu.Lock()
	defer state.mu.Unlock()

	decision := observation.Decision
	if decision.Protect {
		state.protectionUntil = decision.ProtectionUntil
		if decision.Trigger != "" && decision.Trigger != "active" {
			state.currentTrigger = decision.Trigger
		}
		state.intercepted = decision.Intercepted
		state.joins = nil
		if decision.Notify {
			state.lastNotification = now
		}
		return
	}

	state.protectionUntil = time.Time{}
	state.currentTrigger = ""
	state.intercepted = 0
	state.lastNotification = time.Time{}
	state.joins = make([]time.Time, observation.JoinCount)
	for i := range state.joins {
		state.joins[i] = now
	}
	if databasePending != nil {
		state.estimatedPending = *databasePending
	}
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
	state.currentTrigger = ""
	state.lastNotification = time.Time{}
	state.intercepted = 0
	state.joins = nil
	return summary, true
}

func (p *joinProtector) Clear(chatID int64) {
	p.groups.Delete(chatID)
}

func (p *joinProtector) Status(chatID int64, now time.Time) JoinProtectionRuntimeStatus {
	status := JoinProtectionRuntimeStatus{State: "normal", Source: "memory", Degraded: true}
	value, ok := p.groups.Load(chatID)
	if !ok {
		return status
	}
	state := value.(*joinProtectionGroupState)
	state.mu.Lock()
	defer state.mu.Unlock()
	status.Intercepted = state.intercepted
	status.RecentJoins = len(state.joins)
	if !state.protectionUntil.IsZero() && now.Before(state.protectionUntil) {
		status.State = "protecting"
		status.Trigger = state.currentTrigger
		until := state.protectionUntil
		status.ProtectionUntil = &until
	}
	if !state.lastNotification.IsZero() {
		lastNotification := state.lastNotification
		status.LastNotificationAt = &lastNotification
	}
	return status
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
	allow            func() bool
	temporaryBan     func() error
	fallbackRestrict func() error
	persistCleanup   func(string) error
	success          func()
	fail             func(error)
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
		restrictErr := error(nil)
		if ops.fallbackRestrict != nil {
			restrictErr = ops.fallbackRestrict()
		}
		persistErr := ops.persistCleanup("join_protection_cleanup_deferred_during_cooldown")
		return errors.Join(
			wrapOptionalError("fallback restrict join protection user during cooldown", restrictErr),
			wrapOptionalError("persist deferred join protection cleanup", persistErr),
		)
	}

	err := ops.temporaryBan()
	if err == nil || isTerminalTelegramCleanupError(err) {
		ops.success()
		return nil
	}

	restrictErr := error(nil)
	if ops.fallbackRestrict != nil {
		restrictErr = ops.fallbackRestrict()
	}
	persistErr := ops.persistCleanup("join_protection_temporary_ban_failed")
	ops.fail(err)
	if persistErr != nil || restrictErr != nil {
		return errors.Join(
			err,
			wrapOptionalError("fallback restrict join protection user", restrictErr),
			wrapOptionalError("persist join protection cleanup", persistErr),
		)
	}
	return err
}

func wrapOptionalError(message string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
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

func (b *telegramCleanupBreaker) Status(chatID int64, now time.Time) (time.Time, int, bool) {
	value, ok := b.groups.Load(chatID)
	if !ok {
		return time.Time{}, 0, false
	}
	state := value.(*telegramCleanupGroupState)
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.cooldownUntil, state.failures, now.Before(state.cooldownUntil)
}

func isTerminalTelegramCleanupError(err error) bool {
	if err == nil {
		return false
	}
	message := telegramCleanupErrorText(err)
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
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func isAlreadyBannedTelegramError(err error) bool {
	message := telegramCleanupErrorText(err)
	return strings.Contains(message, "already banned") || strings.Contains(message, "already kicked")
}

func telegramCleanupErrorText(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(err.Error())
	var telegramError *tele.Error
	if errors.As(err, &telegramError) && telegramError != nil {
		message += " " + strings.ToLower(telegramError.Description+" "+telegramError.Message)
	}
	return message
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

func (s *Service) evaluateJoinProtection(ctx context.Context, chat *tele.Chat, policy config.JoinProtectionPolicy, now time.Time) joinProtectionDecision {
	if chat == nil || !policy.Enabled {
		return joinProtectionDecision{}
	}

	s.ensureRuntimeGuards()
	if s.redis != nil {
		if snapshot, ok := s.joinProtector.Snapshot(chat.ID, policy, now); ok {
			if now.Before(snapshot.ProtectionUntil) {
				if err := restoreJoinProtectionRedis(ctx, s.redis, chat.ID, snapshot, now); err != nil {
					s.logger.Warn("restore active join protection redis shadow failed", zap.Error(err), zap.Int64("chat_id", chat.ID))
				}
			} else if len(snapshot.Joins) > 0 {
				if err := restoreJoinProtectionWindowRedis(ctx, s.redis, chat.ID, snapshot.Joins, policy, now); err != nil {
					s.logger.Warn("restore join protection window redis shadow failed", zap.Error(err), zap.Int64("chat_id", chat.ID))
				}
			}
		}

		observation, err := observeJoinProtectionRedis(ctx, s.redis, chat.ID, policy, nil, now)
		if err == nil {
			if observation.Recovered != nil {
				s.notifyJoinProtectionRecovery(ctx, chat, *observation.Recovered)
			}
			if !observation.NeedsData {
				s.joinProtector.MirrorRedisObservation(chat.ID, observation, nil, now)
				return observation.Decision
			}
			databasePending := s.countPendingForJoinProtection(ctx, chat.ID, policy)
			observation, err = observeJoinProtectionRedis(ctx, s.redis, chat.ID, policy, &databasePending, now)
			if err == nil {
				s.joinProtector.MirrorRedisObservation(chat.ID, observation, &databasePending, now)
				return observation.Decision
			}
		}
		s.logger.Error(
			"join protection redis state unavailable; using single-process fallback",
			zap.Error(err),
			zap.Int64("chat_id", chat.ID),
			zap.String("event", "join_protection_redis_fallback"),
		)
	}

	databasePending := s.countPendingForJoinProtection(ctx, chat.ID, policy)
	return s.joinProtector.ObserveJoin(chat.ID, policy, databasePending, now)
}

func (s *Service) countPendingForJoinProtection(ctx context.Context, chatID int64, policy config.JoinProtectionPolicy) int {
	databasePending, err := s.queries.CountActivePendingVerificationsByChat(ctx, chatID)
	if err != nil {
		s.logger.Warn(
			"count active pending verifications failed, enter protection fail-safe",
			zap.Error(err),
			zap.Int64("chat_id", chatID),
		)
		return policy.MaxPendingVerifications
	}
	return int(databasePending)
}

func (s *Service) finishJoinProtection(ctx context.Context, chatID int64, expectedUntil, now time.Time) (joinProtectionSummary, bool) {
	if s.redis != nil {
		summary, ok, err := finishJoinProtectionRedis(ctx, s.redis, chatID, expectedUntil, now)
		if err == nil {
			return summary, ok
		}
		s.logger.Error("finish redis join protection failed", zap.Error(err), zap.Int64("chat_id", chatID))
	}
	s.ensureRuntimeGuards()
	return s.joinProtector.FinishProtection(chatID, expectedUntil, now)
}

func (s *Service) scheduleJoinProtectionRecovery(chat *tele.Chat, decision joinProtectionDecision) {
	if chat == nil || !decision.Entered || decision.ProtectionUntil.IsZero() {
		return
	}
	expectedUntil := decision.ProtectionUntil
	s.runDelayed(time.Until(expectedUntil), func() {
		if summary, ok := s.finishJoinProtection(context.Background(), chat.ID, expectedUntil, time.Now()); ok {
			s.notifyJoinProtectionRecovery(context.Background(), chat, summary)
		}
	})
}

func (s *Service) ProcessJoinProtectionRecoveries(ctx context.Context, limit int64) error {
	if s.redis == nil {
		return nil
	}
	items, err := listExpiredJoinProtectionsRedis(ctx, s.redis, time.Now(), limit)
	if err != nil {
		return err
	}
	for _, item := range items {
		chatID, parseErr := strconv.ParseInt(fmt.Sprint(item.Member), 10, 64)
		if parseErr != nil {
			s.logger.Warn("discard invalid join protection active member", zap.String("member", fmt.Sprint(item.Member)))
			_ = s.redis.ZRem(ctx, joinProtectionActiveKey, item.Member).Err()
			continue
		}
		expectedUntil := time.UnixMilli(int64(item.Score))
		summary, ok := s.finishJoinProtection(ctx, chatID, expectedUntil, time.Now())
		if !ok {
			continue
		}
		chat := &tele.Chat{ID: chatID}
		if group, groupErr := s.queries.GetGroupByChatID(ctx, chatID); groupErr == nil {
			chat.Title = group.Title
		}
		s.notifyJoinProtectionRecovery(ctx, chat, summary)
	}
	return nil
}

func (s *Service) ClearJoinProtectionState(ctx context.Context, chatID int64) error {
	s.ensureRuntimeGuards()
	s.joinProtector.Clear(chatID)
	if s.redis == nil {
		return nil
	}
	return clearJoinProtectionRedis(ctx, s.redis, chatID)
}

func (s *Service) JoinProtectionStatus(ctx context.Context, chatID int64, enabled bool) JoinProtectionRuntimeStatus {
	status := JoinProtectionRuntimeStatus{State: "normal", Source: "memory", Degraded: true}
	if s.redis != nil {
		redisStatus, err := loadJoinProtectionRuntimeStatusRedis(ctx, s.redis, chatID, time.Now())
		if err == nil {
			status = redisStatus
		} else {
			s.logger.Warn("load join protection runtime status from redis failed", zap.Error(err), zap.Int64("chat_id", chatID))
			s.ensureRuntimeGuards()
			status = s.joinProtector.Status(chatID, time.Now())
		}
	} else {
		s.ensureRuntimeGuards()
		status = s.joinProtector.Status(chatID, time.Now())
	}
	if s.queries != nil {
		if count, err := s.queries.CountActivePendingVerificationsByChat(ctx, chatID); err == nil {
			status.PendingVerifications = count
		}
	}
	if !enabled {
		status.State = "disabled"
	}
	return status
}

func (s *Service) allowTelegramCleanup(ctx context.Context, chatID int64, now time.Time) (bool, time.Time) {
	if s.redis != nil {
		allowed, until, err := allowTelegramCleanupRedis(ctx, s.redis, chatID, now)
		if err == nil {
			return allowed, until
		}
		s.logger.Error("load telegram cleanup cooldown from redis failed", zap.Error(err), zap.Int64("chat_id", chatID))
	}
	s.ensureRuntimeGuards()
	return s.cleanupBreaker.Allow(chatID, now)
}

func (s *Service) markTelegramCleanupSuccess(ctx context.Context, chatID int64) {
	s.ensureRuntimeGuards()
	s.cleanupBreaker.Success(chatID)
	if s.redis != nil {
		if err := markTelegramCleanupSuccessRedis(ctx, s.redis, chatID, time.Now()); err != nil {
			s.logger.Error("persist telegram cleanup recovery failed", zap.Error(err), zap.Int64("chat_id", chatID))
		}
	}
}

func (s *Service) markTelegramCleanupFailure(ctx context.Context, chatID int64, baseCooldown time.Duration, err error, now time.Time) (time.Time, bool) {
	if s.redis != nil {
		retryMinimum := time.Duration(0)
		if retryAfter, ok := floodRetryAfter(err); ok {
			retryMinimum = time.Duration(retryAfter+1) * time.Second
		}
		until, notify, _, redisErr := markTelegramCleanupFailureRedis(
			ctx,
			s.redis,
			chatID,
			baseCooldown,
			retryMinimum,
			redact.ErrorString(err),
			now,
		)
		if redisErr == nil {
			return until, notify
		}
		s.logger.Error("persist telegram cleanup cooldown failed", zap.Error(redisErr), zap.Int64("chat_id", chatID))
	}
	s.ensureRuntimeGuards()
	return s.cleanupBreaker.Fail(chatID, baseCooldown, err, now)
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

func (s *Service) restrictJoinFloodUserFallback(chat *tele.Chat, user *tele.User) error {
	if chat == nil || user == nil {
		return nil
	}
	member := &tele.ChatMember{User: user, Rights: tele.NoRights()}
	if err := s.bot.Restrict(chat, member); err != nil {
		return normalizeTelegramActionError("restrict", err)
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
		if err == nil {
			return nil
		}

		queueErr := error(nil)
		if s.redis != nil {
			queueErr = enqueueJoinProtectionCleanupRedis(ctx, s.redis, joinProtectionDeferredCleanup{
				ChatID:              chat.ID,
				UserID:              user.ID,
				Username:            user.Username,
				FirstName:           user.FirstName,
				TemporaryBanSeconds: policy.TemporaryBanSeconds,
				Reason:              reason,
			}, time.Now())
		} else {
			queueErr = errors.New("redis unavailable")
		}
		if queueErr != nil {
			s.logger.Error(
				"join protection cleanup could not be persisted",
				zap.String("event", "join_protection_cleanup_persistence_failed"),
				zap.Error(errors.Join(err, queueErr)),
				zap.Int64("chat_id", chat.ID),
				zap.Int64("user_id", user.ID),
			)
			s.notifyAdminsForChat(
				ctx,
				chat,
				"<b>入群防护清理任务未能持久化</b>\nTelegram 处理与数据库、Redis 兜底同时失败，请人工检查最近入群成员。",
				"join protection cleanup persistence failure",
			)
			return errors.Join(err, queueErr)
		}

		s.logger.Warn(
			"join protection cleanup queued in redis after database failure",
			zap.String("event", "join_protection_cleanup_queued_redis"),
			zap.Error(err),
			zap.Int64("chat_id", chat.ID),
			zap.Int64("user_id", user.ID),
		)
		return nil
	}

	s.ensureRuntimeGuards()
	return runJoinProtectionBan(joinProtectionBanOps{
		allow: func() bool {
			allowed, _ := s.allowTelegramCleanup(ctx, chat.ID, time.Now())
			return allowed
		},
		temporaryBan: func() error {
			return s.temporaryBanJoinFloodUser(chat, user, policy.TemporaryBanSeconds)
		},
		fallbackRestrict: func() error {
			return s.restrictJoinFloodUserFallback(chat, user)
		},
		persistCleanup: persistCleanup,
		success: func() {
			s.markTelegramCleanupSuccess(ctx, chat.ID)
		},
		fail: func(err error) {
			s.openTelegramCleanupCooldown(ctx, chat, policy, err)
		},
	})
}

func (s *Service) ProcessJoinProtectionCleanupFallbacks(ctx context.Context, limit int64) error {
	if s.redis == nil {
		return nil
	}
	state, err := s.GetSystemState(ctx)
	if err != nil {
		return fmt.Errorf("load system state before join protection cleanup: %w", err)
	}
	if state.ActionsPaused {
		return nil
	}
	for index := int64(0); index < limit; index++ {
		task, ok, err := claimDueJoinProtectionCleanupRedis(ctx, s.redis, time.Now(), time.Minute)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		state, stateErr := s.GetSystemState(ctx)
		if stateErr != nil || state.ActionsPaused {
			if err := rescheduleJoinProtectionCleanupRedis(ctx, s.redis, task.Member, time.Now().Add(time.Minute)); err != nil {
				s.logger.Error("reschedule join protection cleanup while actions paused", zap.Error(err), zap.Int64("chat_id", task.ChatID), zap.Int64("user_id", task.UserID))
			}
			if stateErr != nil {
				return fmt.Errorf("reload system state before join protection cleanup: %w", stateErr)
			}
			return nil
		}
		allowed, cooldownUntil := s.allowTelegramCleanup(ctx, task.ChatID, time.Now())
		if !allowed {
			if err := rescheduleJoinProtectionCleanupRedis(ctx, s.redis, task.Member, cooldownUntil); err != nil {
				s.logger.Error("reschedule join protection cleanup during cooldown", zap.Error(err), zap.Int64("chat_id", task.ChatID), zap.Int64("user_id", task.UserID))
			}
			continue
		}

		chat := &tele.Chat{ID: task.ChatID}
		if group, groupErr := s.queries.GetGroupByChatID(ctx, task.ChatID); groupErr == nil {
			chat.Title = group.Title
		}
		user := &tele.User{ID: task.UserID, Username: task.Username, FirstName: task.FirstName}
		action := task.Action
		var actionErr error
		if task.Action == "temporary_ban" {
			actionErr = s.temporaryBanJoinFloodUser(chat, user, task.TemporaryBanSeconds)
		} else {
			action, actionErr = s.applyVerificationFailAction(chat, user, task.Action)
		}
		if actionErr != nil && !isTerminalTelegramCleanupError(actionErr) {
			_ = s.restrictJoinFloodUserFallback(chat, user)
			cleanupPolicy := config.DefaultJoinProtectionPolicy
			if loadedPolicy, policyErr := s.LoadGuardPolicy(ctx, task.ChatID); policyErr == nil {
				cleanupPolicy = loadedPolicy.JoinProtection
			}
			s.openTelegramCleanupCooldown(ctx, chat, cleanupPolicy, actionErr)
			_, retryAt := s.allowTelegramCleanup(ctx, task.ChatID, time.Now())
			if retryAt.IsZero() {
				retryAt = time.Now().Add(5 * time.Minute)
			}
			if err := rescheduleJoinProtectionCleanupRedis(ctx, s.redis, task.Member, retryAt); err != nil {
				s.logger.Error("reschedule failed join protection cleanup", zap.Error(err), zap.Int64("chat_id", task.ChatID), zap.Int64("user_id", task.UserID))
			}
			continue
		}
		if actionErr != nil {
			action = "already_absent"
		}
		s.markTelegramCleanupSuccess(ctx, task.ChatID)
		if err := removeJoinProtectionCleanupRedis(ctx, s.redis, task.Member, task.ChatID); err != nil {
			s.logger.Error("remove completed redis join protection cleanup", zap.Error(err), zap.Int64("chat_id", task.ChatID), zap.Int64("user_id", task.UserID))
			continue
		}
		rule := strings.TrimSpace(task.Rule)
		if rule == "" {
			rule = "join_protection_redis_fallback_cleanup"
		}
		if _, err := s.queries.InsertViolation(ctx, store.InsertViolationParams{
			ChatID:   task.ChatID,
			UserID:   task.UserID,
			Username: stringPtr(task.Username),
			Rule:     rule,
			Matched:  stringPtr(task.Reason),
			Action:   action,
		}); err != nil {
			s.logger.Warn("record redis join protection cleanup result failed", zap.Error(err), zap.Int64("chat_id", task.ChatID), zap.Int64("user_id", task.UserID))
		}
		s.logger.Info(
			"redis join protection cleanup completed",
			zap.String("event", "join_protection_redis_cleanup_completed"),
			zap.Int64("chat_id", task.ChatID),
			zap.Int64("user_id", task.UserID),
			zap.String("action", action),
		)
	}
	return nil
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

type joinProtectionCleanupPayload struct {
	Reason              string `json:"reason"`
	TemporaryBanSeconds int    `json:"temporary_ban_seconds"`
}

func decodeJoinProtectionCleanupPayload(raw []byte, fallbackSeconds int) joinProtectionCleanupPayload {
	payload := joinProtectionCleanupPayload{TemporaryBanSeconds: fallbackSeconds}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.TemporaryBanSeconds < 60 || payload.TemporaryBanSeconds > 604800 {
		payload.TemporaryBanSeconds = fallbackSeconds
	}
	return payload
}

func (s *Service) handleJoinProtectionCleanupExpiry(
	ctx context.Context,
	pending store.PendingVerification,
	policy config.GuardPolicy,
	chat *tele.Chat,
	user *tele.User,
) (VerificationExpiryResult, error) {
	payload := decodeJoinProtectionCleanupPayload(pending.Payload, policy.JoinProtection.TemporaryBanSeconds)
	allowed, retryAt := s.allowTelegramCleanup(ctx, pending.ChatID, time.Now())
	if !allowed {
		return verificationExpiryRetry(retryAt, "telegram cleanup cooldown"), nil
	}

	action := "temporary_ban"
	if err := s.temporaryBanJoinFloodUser(chat, user, payload.TemporaryBanSeconds); err != nil {
		if isTerminalTelegramCleanupError(err) {
			action = "already_absent"
		} else {
			retryAt = s.openTelegramCleanupCooldown(ctx, chat, policy.JoinProtection, err)
			return verificationExpiryRetry(retryAt, redact.ErrorString(err)), nil
		}
	}
	s.markTelegramCleanupSuccess(ctx, pending.ChatID)

	deleted, err := s.deleteProcessedPendingVerification(ctx, pending)
	if err != nil {
		return VerificationExpiryResult{}, fmt.Errorf("delete completed join protection cleanup: %w", err)
	}
	if deleted == 0 {
		return VerificationExpiryResult{}, nil
	}

	if _, err := s.queries.InsertViolation(ctx, store.InsertViolationParams{
		ChatID:   pending.ChatID,
		UserID:   pending.UserID,
		Username: pending.Username,
		Rule:     "join_protection_temporary_ban_failed_cleanup",
		Matched:  stringPtr(payload.Reason),
		Action:   action,
	}); err != nil {
		s.logger.Warn("record join protection cleanup result failed after cleanup", zap.Error(err), zap.Int64("chat_id", pending.ChatID), zap.Int64("user_id", pending.UserID))
	}

	s.logger.Info(
		"join protection deferred cleanup completed",
		zap.String("event", "join_protection_cleanup_completed"),
		zap.Int64("chat_id", pending.ChatID),
		zap.Int64("user_id", pending.UserID),
		zap.String("action", action),
		zap.Int("temporary_ban_seconds", payload.TemporaryBanSeconds),
	)
	return VerificationExpiryResult{}, nil
}

func (s *Service) handleVerificationPromptFailure(ctx context.Context, chat *tele.Chat, user *tele.User, policy config.GuardPolicy, promptErr error) error {
	if chat == nil || user == nil {
		return nil
	}
	payload, _ := json.Marshal(map[string]string{"reason": "verification_prompt_failed"})
	payload, snapshotErr := withVerificationFailActionSnapshot(payload, policy.Verify.FailAction)
	if snapshotErr != nil {
		s.logger.Error("snapshot prompt failure action failed", zap.Error(snapshotErr), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return snapshotErr
	}
	cleanupPersisted := false
	persistCleanup := func() error {
		_, databaseErr := s.queries.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{
			ChatID:    chat.ID,
			UserID:    user.ID,
			Username:  stringPtr(user.Username),
			FirstName: stringPtr(user.FirstName),
			Method:    "cleanup",
			Payload:   payload,
			ExpiresAt: time.Now().Add(-time.Second),
		})
		if databaseErr == nil {
			cleanupPersisted = true
			return nil
		}

		queueErr := errors.New("redis unavailable")
		if s.redis != nil {
			queueErr = enqueueJoinProtectionCleanupRedis(ctx, s.redis, joinProtectionDeferredCleanup{
				ChatID:    chat.ID,
				UserID:    user.ID,
				Username:  user.Username,
				FirstName: user.FirstName,
				Reason:    redact.ErrorString(promptErr),
				Action:    normalizeVerificationFailAction(policy.Verify.FailAction),
				Rule:      "verification_prompt_failed_redis_cleanup",
			}, time.Now())
		}
		if queueErr != nil {
			s.logger.Error(
				"verification prompt cleanup could not be persisted",
				zap.String("event", "verification_prompt_cleanup_persistence_failed"),
				zap.Error(errors.Join(databaseErr, queueErr)),
				zap.Int64("chat_id", chat.ID),
				zap.Int64("user_id", user.ID),
			)
			s.notifyAdminsForChat(
				ctx,
				chat,
				"<b>验证清理任务未能持久化</b>\nTelegram、PostgreSQL 与 Redis 清理链路同时失败，请人工检查最近入群成员。",
				"verification prompt cleanup persistence failure",
			)
			return errors.Join(databaseErr, queueErr)
		}
		s.logger.Warn(
			"verification prompt cleanup queued in redis after database failure",
			zap.String("event", "verification_prompt_cleanup_queued_redis"),
			zap.Error(databaseErr),
			zap.Int64("chat_id", chat.ID),
			zap.Int64("user_id", user.ID),
		)
		cleanupPersisted = true
		return nil
	}
	s.ensureRuntimeGuards()
	state, stateErr := s.GetSystemState(ctx)
	if stateErr != nil || state.ActionsPaused {
		if err := persistCleanup(); err != nil {
			s.logger.Error("persist prompt failure while telegram actions are unavailable failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
			return err
		}
		return nil
	}
	if allowed, _ := s.allowTelegramCleanup(ctx, chat.ID, time.Now()); !allowed {
		if err := persistCleanup(); err != nil {
			s.logger.Error("persist prompt failure during telegram cooldown failed; member remains restricted", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
			return err
		}
		return nil
	}
	action, cleanupErr := runPromptFailureCleanup(promptFailureCleanupOps{
		applyAction: func() (string, error) {
			return s.applyVerificationFailAction(chat, user, policy.Verify.FailAction)
		},
		persist: persistCleanup,
	})
	if cleanupErr == nil {
		s.markTelegramCleanupSuccess(ctx, chat.ID)
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
		return nil
	}

	s.openTelegramCleanupCooldown(ctx, chat, policy.JoinProtection, cleanupErr)
	if cleanupPersisted {
		return nil
	}
	return cleanupErr
}

func (s *Service) openTelegramCleanupCooldown(ctx context.Context, chat *tele.Chat, policy config.JoinProtectionPolicy, err error) time.Time {
	s.ensureRuntimeGuards()
	chatID := int64(0)
	if chat != nil {
		chatID = chat.ID
	}
	until, notify := s.markTelegramCleanupFailure(ctx, chatID, time.Duration(policy.TelegramFailureCooldownSeconds)*time.Second, err, time.Now())
	if !notify {
		return until
	}
	message := fmt.Sprintf("⚠️ <b>Telegram 清理暂时失败</b>\n该群已暂停重复清理请求，%s 后自动重试。期间相关账号保持受限。", htmlEscape(formatRemaining(until, time.Now())))
	s.notifyAdminsForChat(ctx, chat, message, "telegram cleanup cooldown")
	return until
}
