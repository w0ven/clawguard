package bot

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/config"
	tele "gopkg.in/telebot.v3"
)

func testJoinProtectionPolicy() config.JoinProtectionPolicy {
	policy := config.DefaultJoinProtectionPolicy
	policy.JoinThreshold = 3
	policy.JoinWindowSeconds = 60
	policy.ProtectionDurationSeconds = 120
	policy.AdminNotifyIntervalSeconds = 30
	policy.MaxPendingVerifications = 1000
	return policy
}

func TestJoinProtectorDisabled(t *testing.T) {
	protector := newJoinProtector()
	policy := testJoinProtectionPolicy()
	policy.Enabled = false
	if decision := protector.ObserveJoin(1, policy, 999, time.Now()); decision.Protect {
		t.Fatal("disabled protection should not intercept")
	}
}

func TestJoinProtectorThresholdIncludesTriggeringJoin(t *testing.T) {
	protector := newJoinProtector()
	policy := testJoinProtectionPolicy()
	now := time.Now()
	protector.ObserveJoin(1, policy, 0, now)
	protector.ObserveJoin(1, policy, 0, now.Add(time.Second))
	decision := protector.ObserveJoin(1, policy, 0, now.Add(2*time.Second))
	if !decision.Protect || !decision.Entered || decision.Trigger != "join_threshold" {
		t.Fatalf("decision = %+v, want threshold protection", decision)
	}
	if decision.Intercepted != 1 {
		t.Fatalf("intercepted = %d, want triggering user counted", decision.Intercepted)
	}
}

func TestJoinProtectorWindowExpires(t *testing.T) {
	protector := newJoinProtector()
	policy := testJoinProtectionPolicy()
	now := time.Now()
	protector.ObserveJoin(1, policy, 0, now)
	protector.ObserveJoin(1, policy, 0, now.Add(time.Second))
	decision := protector.ObserveJoin(1, policy, 0, now.Add(61*time.Second))
	if decision.Protect {
		t.Fatalf("expired joins should not trigger protection: %+v", decision)
	}
}

func TestJoinProtectorProtectionExpires(t *testing.T) {
	protector := newJoinProtector()
	policy := testJoinProtectionPolicy()
	policy.JoinThreshold = 2
	now := time.Now()
	protector.ObserveJoin(1, policy, 0, now)
	decision := protector.ObserveJoin(1, policy, 0, now.Add(time.Second))
	if _, ok := protector.FinishProtection(1, decision.ProtectionUntil, decision.ProtectionUntil.Add(-time.Second)); ok {
		t.Fatal("protection ended before deadline")
	}
	summary, ok := protector.FinishProtection(1, decision.ProtectionUntil, decision.ProtectionUntil)
	if !ok || summary.Intercepted != 1 {
		t.Fatalf("summary = %+v, ok = %t", summary, ok)
	}
}

func TestJoinProtectorGroupsAreIsolated(t *testing.T) {
	protector := newJoinProtector()
	policy := testJoinProtectionPolicy()
	now := time.Now()
	for index := 0; index < 3; index++ {
		protector.ObserveJoin(1, policy, 0, now.Add(time.Duration(index)*time.Second))
	}
	if decision := protector.ObserveJoin(2, policy, 0, now); decision.Protect {
		t.Fatalf("group 2 affected by group 1: %+v", decision)
	}
}

func TestJoinProtectorConcurrentThreshold(t *testing.T) {
	protector := newJoinProtector()
	policy := testJoinProtectionPolicy()
	policy.JoinThreshold = 50
	policy.MaxPendingVerifications = 10000
	now := time.Now()
	var protected atomic.Int64
	var wait sync.WaitGroup
	for index := 0; index < 100; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if protector.ObserveJoin(1, policy, 0, now).Protect {
				protected.Add(1)
			}
		}()
	}
	wait.Wait()
	if got := protected.Load(); got != 51 {
		t.Fatalf("protected = %d, want 51", got)
	}
}

func TestJoinProtectorPendingLimit(t *testing.T) {
	protector := newJoinProtector()
	policy := testJoinProtectionPolicy()
	policy.MaxPendingVerifications = 3
	decision := protector.ObserveJoin(1, policy, 2, time.Now())
	if !decision.Protect || decision.Trigger != "pending_limit" {
		t.Fatalf("decision = %+v, want pending limit protection", decision)
	}
}

func TestSyntheticCleanupDoesNotReservePendingSlot(t *testing.T) {
	for _, method := range []string{"cleanup", "join_protection_cleanup"} {
		if pendingReservesJoinProtectionSlot(method) {
			t.Fatalf("method %q unexpectedly reserves a pending slot", method)
		}
	}
	for _, method := range []string{"button", "math", "random", "turnstile"} {
		if !pendingReservesJoinProtectionSlot(method) {
			t.Fatalf("method %q should reserve a pending slot", method)
		}
	}
}

func TestJoinProtectorPendingRelease(t *testing.T) {
	protector := newJoinProtector()
	policy := testJoinProtectionPolicy()
	policy.MaxPendingVerifications = 2
	now := time.Now()
	if decision := protector.ObserveJoin(1, policy, 0, now); decision.Protect {
		t.Fatalf("first pending reservation unexpectedly protected: %+v", decision)
	}
	protector.ReleasePending(1)
	if decision := protector.ObserveJoin(1, policy, 0, now.Add(time.Second)); decision.Protect {
		t.Fatalf("released pending reservation still counted: %+v", decision)
	}
}

func TestJoinProtectorActiveProtectionDoesNotGrowJoinHistory(t *testing.T) {
	protector := newJoinProtector()
	policy := testJoinProtectionPolicy()
	policy.JoinThreshold = 2
	now := time.Now()
	protector.ObserveJoin(1, policy, 0, now)
	entered := protector.ObserveJoin(1, policy, 0, now.Add(time.Second))
	if !entered.Entered {
		t.Fatalf("decision = %+v, want protection entry", entered)
	}
	for index := 0; index < 10000; index++ {
		protector.ObserveJoin(1, policy, 0, now.Add(2*time.Second+time.Duration(index)*time.Millisecond))
	}
	state := protector.group(1)
	state.mu.Lock()
	joinCount := len(state.joins)
	intercepted := state.intercepted
	state.mu.Unlock()
	if joinCount != 0 {
		t.Fatalf("active protection retained %d join timestamps, want 0", joinCount)
	}
	if intercepted != 10001 {
		t.Fatalf("intercepted = %d, want 10001", intercepted)
	}
}

func TestJoinProtectorNotificationRateLimit(t *testing.T) {
	protector := newJoinProtector()
	policy := testJoinProtectionPolicy()
	policy.JoinThreshold = 2
	now := time.Now()
	protector.ObserveJoin(1, policy, 0, now)
	entered := protector.ObserveJoin(1, policy, 0, now.Add(time.Second))
	quiet := protector.ObserveJoin(1, policy, 0, now.Add(10*time.Second))
	reminder := protector.ObserveJoin(1, policy, 0, now.Add(31*time.Second))
	if !entered.Notify || quiet.Notify || !reminder.Notify {
		t.Fatalf("notify flags entered=%t quiet=%t reminder=%t", entered.Notify, quiet.Notify, reminder.Notify)
	}
}

func TestPromptSendFailureRemainsOnCleanupPath(t *testing.T) {
	telegramErr := errors.New("telegram unavailable")
	var persisted atomic.Bool
	action, err := runPromptFailureCleanup(promptFailureCleanupOps{
		applyAction: func() (string, error) { return "", telegramErr },
		persist: func() error {
			persisted.Store(true)
			return nil
		},
	})
	if action != "" || !errors.Is(err, telegramErr) || !persisted.Load() {
		t.Fatalf("action=%q err=%v persisted=%t", action, err, persisted.Load())
	}
}

func TestJoinProtectionBanFailurePersistsAndOpensCooldown(t *testing.T) {
	telegramErr := errors.New("telegram unavailable")
	var temporaryBanCalls atomic.Int64
	var persistedReason string
	var failed atomic.Bool
	err := runJoinProtectionBan(joinProtectionBanOps{
		allow: func() bool { return true },
		temporaryBan: func() error {
			temporaryBanCalls.Add(1)
			return telegramErr
		},
		persistCleanup: func(reason string) error {
			persistedReason = reason
			return nil
		},
		success: func() { t.Fatal("failed ban marked successful") },
		fail:    func(error) { failed.Store(true) },
	})
	if !errors.Is(err, telegramErr) || temporaryBanCalls.Load() != 1 || persistedReason != "join_protection_temporary_ban_failed" || !failed.Load() {
		t.Fatalf("err=%v calls=%d reason=%q failed=%t", err, temporaryBanCalls.Load(), persistedReason, failed.Load())
	}
}

func TestJoinProtectionBanTerminalErrorDoesNotPersist(t *testing.T) {
	var persisted atomic.Bool
	var succeeded atomic.Bool
	err := runJoinProtectionBan(joinProtectionBanOps{
		allow:        func() bool { return true },
		temporaryBan: func() error { return errors.New("Bad Request: user not found") },
		persistCleanup: func(string) error {
			persisted.Store(true)
			return nil
		},
		success: func() { succeeded.Store(true) },
		fail:    func(error) { t.Fatal("terminal error opened cooldown") },
	})
	if err != nil || persisted.Load() || !succeeded.Load() {
		t.Fatalf("err=%v persisted=%t succeeded=%t", err, persisted.Load(), succeeded.Load())
	}
}

func TestJoinProtectionBanCooldownDefersWithoutTelegramRetry(t *testing.T) {
	var temporaryBanCalls atomic.Int64
	var persistedReason string
	err := runJoinProtectionBan(joinProtectionBanOps{
		allow: func() bool { return false },
		temporaryBan: func() error {
			temporaryBanCalls.Add(1)
			return nil
		},
		persistCleanup: func(reason string) error {
			persistedReason = reason
			return nil
		},
		success: func() { t.Fatal("deferred ban marked successful") },
		fail:    func(error) { t.Fatal("existing cooldown reopened") },
	})
	if err != nil || temporaryBanCalls.Load() != 0 || persistedReason != "join_protection_cleanup_deferred_during_cooldown" {
		t.Fatalf("err=%v calls=%d reason=%q", err, temporaryBanCalls.Load(), persistedReason)
	}
}

func TestTelegramCleanupBreakerCooldownAndRetryAfter(t *testing.T) {
	breaker := newTelegramCleanupBreaker()
	now := time.Now()
	err := &tele.FloodError{RetryAfter: 600}
	until, notify := breaker.Fail(1, 5*time.Minute, err, now)
	if !notify || until.Before(now.Add(600*time.Second)) {
		t.Fatalf("until=%s notify=%t", until, notify)
	}
	if allowed, _ := breaker.Allow(1, now.Add(time.Minute)); allowed {
		t.Fatal("cleanup allowed during cooldown")
	}
	if _, notifyAgain := breaker.Fail(1, 5*time.Minute, err, now.Add(time.Minute)); notifyAgain {
		t.Fatal("duplicate cooldown notification")
	}
	if allowed, _ := breaker.Allow(2, now); !allowed {
		t.Fatal("another group should remain available")
	}
}

func TestTelegramCleanupBreakerRetryAfterIsClampedToOneHour(t *testing.T) {
	breaker := newTelegramCleanupBreaker()
	now := time.Now()
	until, notify := breaker.Fail(1, 5*time.Minute, &tele.FloodError{RetryAfter: 7200}, now)
	if !notify {
		t.Fatal("first failure should notify")
	}
	if want := now.Add(time.Hour); !until.Equal(want) {
		t.Fatalf("until=%s want=%s", until, want)
	}
}

func TestTerminalTelegramCleanupErrors(t *testing.T) {
	for _, message := range []string{"Bad Request: user not found", "participant_id_invalid", "user not participant", "already kicked"} {
		if !isTerminalTelegramCleanupError(errors.New(message)) {
			t.Fatalf("expected terminal error for %q", message)
		}
	}
	if isTerminalTelegramCleanupError(errors.New("temporary network failure")) {
		t.Fatal("temporary failure classified as terminal")
	}
}
