//go:build integration

package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
)

func restartedCleanupService(ctx context.Context, old *Service) *Service {
	s := &Service{cfg: old.cfg, logger: old.logger, queries: old.queries, redis: old.redis, bot: old.bot, sender: old.bot, sendLimiter: NewSendLimiter(), joinProtector: newJoinProtector(), cleanupBreaker: newTelegramCleanupBreaker()}
	s.cacheAuthorizedGroup(ctx, -90001, true)
	s.cacheSystemState(ctx, store.SystemState{})
	s.policySnapshots.Store(int64(-90001), keywordReplyPolicy("", 0))
	s.policySnapshotAt.Store(int64(-90001), time.Now())
	return s
}

func TestCleanupDispatchUnknownSurvivesLeaseAndCrashIntegration(t *testing.T) {
	for _, fault := range []string{"bad-json", "missing-result", "timeout"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			pool, q, s, tr := settlementFixture(t)
			pending := settlementPending(t, q, 101, "button", `{"fail_action":"ban"}`)
			tr.mu.Lock()
			tr.failBanUserID = 101
			switch fault {
			case "bad-json":
				tr.failBanUserBody = "<html>"
			case "missing-result":
				tr.failBanUserBody = `{"ok":true}`
			case "timeout":
				tr.failBanNetwork = true
			}
			tr.mu.Unlock()
			claim := func(owner string) store.PendingVerification {
				p, e := q.ClaimExpiredPendingVerifications(ctx, store.ClaimExpiredPendingVerificationsParams{LeaseSeconds: 60, LeaseOwner: owner, BatchLimit: 1})
				if e != nil || len(p) != 1 {
					t.Fatalf("claim: %v %+v", e, p)
				}
				return p[0]
			}
			first := claim("worker-a")
			result, err := s.HandleVerificationExpiry(ctx, first)
			if err != nil || result.RetryAt.IsZero() || !strings.Contains(result.RetryReason, "review") {
				t.Fatalf("first %+v %v", result, err)
			}
			state, _, err := q.CleanupDispatchState(ctx, verificationCleanupKey(pending))
			if err != nil || state != "dispatched" {
				t.Fatalf("dispatch %q %v", state, err)
			}
			before := countString(tr.Methods(), "kickChatMember")
			if before != 1 || countString(tr.Methods(), "unbanChatMember") != 0 || countString(tr.Methods(), "restrictChatMember") != 0 {
				t.Fatalf("calls %v", tr.Methods())
			}
			if _, err = pool.Exec(ctx, `UPDATE pending_verifications SET lease_until=now()-interval '1 second' WHERE id=$1`, pending.ID); err != nil {
				t.Fatal(err)
			}
			second := claim("worker-b")
			restarted := restartedCleanupService(ctx, s)
			result, err = restarted.HandleVerificationExpiry(ctx, second)
			if err != nil || result.RetryAt.IsZero() || countString(tr.Methods(), "kickChatMember") != before {
				t.Fatalf("reclaim %+v %v calls %v", result, err, tr.Methods())
			}
		})
	}
}

func TestJoinProtectionUnknownNeverFallbackOrReplayIntegration(t *testing.T) {
	for _, fault := range []string{"bad-json", "timeout"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			_, q, s, tr := settlementFixture(t)
			chat, user := &tele.Chat{ID: -90001}, &tele.User{ID: 102}
			p := testJoinProtectionPolicy()
			p.TemporaryBanSeconds = 3600
			tr.mu.Lock()
			tr.failBanUserID = user.ID
			tr.failBanUserBody = "<html>"
			tr.failBanNetwork = fault == "timeout"
			tr.mu.Unlock()
			err := s.handleJoinProtectionAction(ctx, chat, user, p, joinProtectionActionTemporaryBan, "u102", time.Now())
			if !errors.Is(err, errCleanupManualReview) {
				t.Fatalf("unknown=%v", err)
			}
			if countString(tr.Methods(), "kickChatMember") != 1 || countString(tr.Methods(), "restrictChatMember") != 0 {
				t.Fatalf("fallback %v", tr.Methods())
			}
			key := membershipCleanupKey("join_protection", chat.ID, user.ID, "u102")
			state, _, err := q.CleanupDispatchState(ctx, key)
			if err != nil || state != "dispatched" {
				t.Fatalf("state=%s %v", state, err)
			}
			err = s.handleJoinProtectionAction(ctx, chat, user, p, joinProtectionActionTemporaryBan, "u102", time.Now())
			if !errors.Is(err, errCleanupManualReview) || countString(tr.Methods(), "kickChatMember") != 1 {
				t.Fatalf("repeat=%v calls=%v", err, tr.Methods())
			}
		})
	}
}

func TestRedisCleanupUnknownSurvivesReclaimIntegration(t *testing.T) {
	ctx := context.Background()
	_, q, s, tr := settlementFixture(t)
	_, r := newJoinProtectionTestRedis(t)
	s.redis = r
	user := int64(103)
	if err := s.rememberMembershipSession(ctx, -90001, user, "u103"); err != nil {
		t.Fatal(err)
	}
	task := joinProtectionDeferredCleanup{ChatID: -90001, UserID: user, TemporaryBanSeconds: 3600, ActionUntil: time.Now().Add(time.Hour), Action: joinProtectionActionTemporaryBan, MembershipGeneration: "u103"}
	if err := enqueueJoinProtectionCleanupRedis(ctx, r, task, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	tr.mu.Lock()
	tr.failBanUserID = user
	tr.failBanUserBody = `{"ok":true,"result":false}`
	tr.mu.Unlock()
	if err := s.ProcessJoinProtectionCleanupFallbacks(ctx, 1); err != nil {
		t.Fatal(err)
	}
	key := membershipCleanupKey("join_protection", -90001, user, "u103")
	state, _, err := q.CleanupDispatchState(ctx, key)
	if err != nil || state != "dispatched" {
		t.Fatalf("state=%q %v calls=%v", state, err, tr.Methods())
	}
	if countString(tr.Methods(), "kickChatMember") != 1 || countString(tr.Methods(), "restrictChatMember") != 0 {
		t.Fatalf("calls=%v", tr.Methods())
	}
	members, err := r.ZRange(ctx, joinProtectionCleanupQueueKey, 0, -1).Result()
	if err != nil || len(members) != 1 {
		t.Fatalf("queue %v %v", members, err)
	}
	if err := rescheduleJoinProtectionCleanupRedis(ctx, r, members[0], time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	restarted := restartedCleanupService(ctx, s)
	if err := restarted.ProcessJoinProtectionCleanupFallbacks(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if countString(tr.Methods(), "kickChatMember") != 1 || countString(tr.Methods(), "restrictChatMember") != 0 {
		t.Fatalf("replay %v", tr.Methods())
	}
}

func TestPromptFailureUnknownNeverQueuesOrRetriesIntegration(t *testing.T) {
	ctx := context.Background()
	_, q, s, tr := settlementFixture(t)
	chat, user := &tele.Chat{ID: -90001}, &tele.User{ID: 105}
	p := keywordReplyPolicy("", 0)
	p.Verify.FailAction = "ban"
	tr.mu.Lock()
	tr.failBanUserID = user.ID
	tr.failBanUserBody = `{"ok":true}`
	tr.mu.Unlock()
	err := s.handleVerificationPromptFailure(ctx, chat, user, p, fmt.Errorf("prompt failed"), "u105")
	if !errors.Is(err, errCleanupManualReview) {
		t.Fatalf("unknown=%v", err)
	}
	state, _, err := q.CleanupDispatchState(ctx, membershipCleanupKey("prompt_failure", chat.ID, user.ID, "u105"))
	if err != nil || state != "dispatched" {
		t.Fatalf("state=%s %v", state, err)
	}
	_, err = q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: chat.ID, UserID: user.ID})
	if err == nil {
		t.Fatal("unknown incorrectly queued for automatic retry")
	}
	err = s.handleVerificationPromptFailure(ctx, chat, user, p, fmt.Errorf("prompt failed"), "u105")
	if !errors.Is(err, errCleanupManualReview) || countString(tr.Methods(), "kickChatMember") != 1 || countString(tr.Methods(), "restrictChatMember") != 0 {
		t.Fatalf("retry %v calls=%v", err, tr.Methods())
	}
}

func TestDispatchCrashBeforeAndAfterTelegramNeverReplaysIntegration(t *testing.T) {
	ctx := context.Background()
	_, q, s, tr := settlementFixture(t)
	for i, state := range []string{"dispatched", "completed"} {
		p := settlementPending(t, q, int64(106+i), "button", `{}`)
		key := verificationCleanupKey(p)
		claimed, _, err := q.BeginCleanupDispatch(ctx, key, p.ChatID, p.UserID, "verification:verify_timeout")
		if err != nil || !claimed {
			t.Fatal(err)
		}
		if state == "completed" {
			if err = q.CompleteCleanupDispatch(ctx, key, "kick"); err != nil {
				t.Fatal(err)
			}
		}
		before := countString(tr.Methods(), "kickChatMember")
		result, err := s.HandleVerificationExpiry(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if countString(tr.Methods(), "kickChatMember") != before {
			t.Fatal("dispatched action replayed")
		}
		if state == "dispatched" && result.RetryAt.IsZero() {
			t.Fatal("unknown not held")
		}
		if state == "completed" && !result.RetryAt.IsZero() {
			t.Fatal("confirmed result not settled")
		}
	}
}

func TestDeferredJoinUnknownRemainsManualAfterDeadlineIntegration(t *testing.T) {
	ctx := context.Background()
	pool, q, s, tr := settlementFixture(t)
	until := time.Now().Add(time.Hour)
	payload := fmt.Sprintf(`{"action":"temporary_ban","temporary_ban_seconds":3600,"action_until":%q}`, until.Format(time.RFC3339Nano))
	p := settlementPending(t, q, 108, "join_protection_cleanup", payload)
	tr.mu.Lock()
	tr.failBanUserID = 108
	tr.failBanNetwork = true
	tr.mu.Unlock()
	result, err := s.HandleVerificationExpiry(ctx, p)
	if err != nil || result.RetryAt.IsZero() {
		t.Fatalf("first %v %v", result, err)
	}
	state, _, err := q.CleanupDispatchState(ctx, verificationCleanupKey(p))
	if err != nil || state != "dispatched" {
		t.Fatalf("state %q %v", state, err)
	}
	expired := fmt.Sprintf(`{"action":"temporary_ban","temporary_ban_seconds":3600,"action_until":%q}`, time.Now().Add(-time.Hour).Format(time.RFC3339Nano))
	if _, err = pool.Exec(ctx, `UPDATE pending_verifications SET payload=$2::jsonb WHERE id=$1`, p.ID, expired); err != nil {
		t.Fatal(err)
	}
	p.Payload = []byte(expired)
	result, err = s.HandleVerificationExpiry(ctx, p)
	if err != nil || result.RetryAt.IsZero() || countString(tr.Methods(), "kickChatMember") != 1 || countString(tr.Methods(), "restrictChatMember") != 0 {
		t.Fatalf("recovery %+v %v calls=%v", result, err, tr.Methods())
	}
	if _, err = q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: p.ChatID, UserID: p.UserID}); err != nil {
		t.Fatalf("unknown discarded: %v", err)
	}
}

func TestVerificationInitialUnknownNeverStartsPunitiveCleanupIntegration(t *testing.T) {
	ctx := context.Background()
	_, q, s, tr := settlementFixture(t)
	p := keywordReplyPolicy("", 0)
	p.Verify.Enabled = true
	p.Verify.Method = "button"
	p.JoinProtection.Enabled = false
	s.policySnapshots.Store(int64(-90001), p)
	s.policySnapshotAt.Store(int64(-90001), time.Now())
	tr.mu.Lock()
	tr.failRestrictUserID = 109
	tr.failRestrictBody = `{"ok":true}`
	tr.mu.Unlock()
	chat, user := &tele.Chat{ID: -90001}, &tele.User{ID: 109, FirstName: "fixture"}
	err := s.startVerification(chat, user, nil, "u109")
	if !errors.Is(err, errCleanupManualReview) {
		t.Fatalf("unknown=%v calls=%v", err, tr.Methods())
	}
	key := membershipCleanupKey("verification_start", chat.ID, user.ID, "u109")
	state, _, err := q.CleanupDispatchState(ctx, key)
	if err != nil || state != "dispatched" {
		t.Fatalf("state=%q %v", state, err)
	}
	if countString(tr.Methods(), "restrictChatMember") != 1 || countString(tr.Methods(), "kickChatMember") != 0 || countString(tr.Methods(), "unbanChatMember") != 0 {
		t.Fatalf("calls=%v", tr.Methods())
	}
	restarted := restartedCleanupService(ctx, s)
	restarted.policySnapshots.Store(int64(-90001), p)
	err = restarted.startVerification(chat, user, nil, "u109")
	if !errors.Is(err, errCleanupManualReview) || countString(tr.Methods(), "restrictChatMember") != 1 || countString(tr.Methods(), "kickChatMember") != 0 {
		t.Fatalf("restart=%v calls=%v", err, tr.Methods())
	}
}

func TestCleanupExplicitRejectionMayRetryIntegration(t *testing.T) {
	ctx := context.Background()
	_, q, s, _ := settlementFixture(t)
	calls := 0
	key := "fixture-explicit-rejection"
	_, err := s.guardedCleanup(ctx, key, -90001, 104, "ban", func() (string, error) { calls++; return "", &telegramExplicitRejection{err: fmt.Errorf("rejected")} })
	if err == nil || errors.Is(err, errCleanupManualReview) {
		t.Fatal(err)
	}
	state, _, err := q.CleanupDispatchState(ctx, key)
	if err != nil || state != "" {
		t.Fatalf("state=%q err=%v", state, err)
	}
	result, err := s.guardedCleanup(ctx, key, -90001, 104, "ban", func() (string, error) { calls++; return "ban", nil })
	if err != nil || result != "ban" || calls != 2 {
		t.Fatalf("result=%s err=%v calls=%d", result, err, calls)
	}
}
