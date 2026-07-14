package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newJoinProtectionTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), Protocol: 2})
	t.Cleanup(func() { _ = client.Close() })
	return server, client
}

func TestRedisJoinProtectionSurvivesProcessBoundary(t *testing.T) {
	server, firstClient := newJoinProtectionTestRedis(t)
	secondClient := redis.NewClient(&redis.Options{Addr: server.Addr(), Protocol: 2})
	t.Cleanup(func() { _ = secondClient.Close() })

	ctx := context.Background()
	policy := testJoinProtectionPolicy()
	policy.JoinThreshold = 3
	policy.MaxPendingVerifications = 1000
	now := time.Unix(1_800_000_000, 0)
	pending := 0

	for index := 0; index < 2; index++ {
		observation, err := observeJoinProtectionRedis(ctx, firstClient, 100, policy, &pending, now.Add(time.Duration(index)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if observation.Decision.Protect {
			t.Fatalf("join %d unexpectedly triggered protection", index+1)
		}
	}

	observation, err := observeJoinProtectionRedis(ctx, secondClient, 100, policy, &pending, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Decision.Protect || !observation.Decision.Entered || observation.Decision.Trigger != "join_threshold" {
		t.Fatalf("decision = %+v, want cross-process threshold entry", observation.Decision)
	}

	active, err := observeJoinProtectionRedis(ctx, firstClient, 100, policy, nil, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if active.NeedsData || !active.Decision.Protect || active.Decision.Intercepted != 2 {
		t.Fatalf("active observation = %+v, want direct interception without database data", active)
	}
}

func TestRedisJoinProtectionRecoveryIsAtomic(t *testing.T) {
	_, client := newJoinProtectionTestRedis(t)
	ctx := context.Background()
	policy := testJoinProtectionPolicy()
	policy.JoinThreshold = 2
	pending := 0
	now := time.Unix(1_800_000_000, 0)

	if _, err := observeJoinProtectionRedis(ctx, client, 200, policy, &pending, now); err != nil {
		t.Fatal(err)
	}
	entered, err := observeJoinProtectionRedis(ctx, client, 200, policy, &pending, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	until := entered.Decision.ProtectionUntil
	if until.IsZero() {
		t.Fatal("missing protection deadline")
	}
	if _, ok, err := finishJoinProtectionRedis(ctx, client, 200, until, until.Add(-time.Second)); err != nil || ok {
		t.Fatalf("early finish ok=%t err=%v", ok, err)
	}
	summary, ok, err := finishJoinProtectionRedis(ctx, client, 200, until, until)
	if err != nil || !ok || summary.Intercepted != 1 {
		t.Fatalf("summary=%+v ok=%t err=%v", summary, ok, err)
	}
	if _, ok, err := finishJoinProtectionRedis(ctx, client, 200, until, until); err != nil || ok {
		t.Fatalf("duplicate finish ok=%t err=%v", ok, err)
	}
}

func TestRedisTelegramCleanupBreakerPersists(t *testing.T) {
	server, firstClient := newJoinProtectionTestRedis(t)
	secondClient := redis.NewClient(&redis.Options{Addr: server.Addr(), Protocol: 2})
	t.Cleanup(func() { _ = secondClient.Close() })
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)

	until, notify, failures, err := markTelegramCleanupFailureRedis(ctx, firstClient, 300, 5*time.Minute, 0, "telegram unavailable", now)
	if err != nil || !notify || failures != 1 || !until.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("until=%v notify=%t failures=%d err=%v", until, notify, failures, err)
	}
	allowed, loadedUntil, err := allowTelegramCleanupRedis(ctx, secondClient, 300, now.Add(time.Minute))
	if err != nil || allowed || !loadedUntil.Equal(until) {
		t.Fatalf("allowed=%t until=%v err=%v", allowed, loadedUntil, err)
	}
	if err := markTelegramCleanupSuccessRedis(ctx, secondClient, 300, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	allowed, _, err = allowTelegramCleanupRedis(ctx, firstClient, 300, now.Add(2*time.Minute))
	if err != nil || !allowed {
		t.Fatalf("cleanup did not recover: allowed=%t err=%v", allowed, err)
	}
}

func TestRedisDeferredCleanupQueueLifecycle(t *testing.T) {
	_, client := newJoinProtectionTestRedis(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	task := joinProtectionDeferredCleanup{
		ChatID:              400,
		UserID:              500,
		Username:            "queued-user",
		FirstName:           "Queued",
		TemporaryBanSeconds: 3600,
		Reason:              "temporary_ban_failed",
	}
	if err := enqueueJoinProtectionCleanupRedis(ctx, client, task, now); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := claimDueJoinProtectionCleanupRedis(ctx, client, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || loaded.ChatID != task.ChatID || loaded.UserID != task.UserID || loaded.TemporaryBanSeconds != 3600 {
		t.Fatalf("loaded = %+v", loaded)
	}
	if _, ok, err := claimDueJoinProtectionCleanupRedis(ctx, client, now, time.Minute); err != nil || ok {
		t.Fatalf("leased task was claimed twice: ok=%t err=%v", ok, err)
	}
	if err := removeJoinProtectionCleanupRedis(ctx, client, loaded.Member, loaded.ChatID); err != nil {
		t.Fatal(err)
	}
	_, ok, err = claimDueJoinProtectionCleanupRedis(ctx, client, now.Add(time.Minute), time.Minute)
	if err != nil || ok {
		t.Fatalf("queue not empty: ok=%t err=%v", ok, err)
	}
}

func TestJoinProtectionCleanupPayloadUsesTemporaryBanDuration(t *testing.T) {
	payload := decodeJoinProtectionCleanupPayload([]byte(`{"reason":"failed","temporary_ban_seconds":7200}`), 3600)
	if payload.TemporaryBanSeconds != 7200 || payload.Reason != "failed" {
		t.Fatalf("payload = %+v", payload)
	}
	invalid := decodeJoinProtectionCleanupPayload([]byte(`{"temporary_ban_seconds":0}`), 3600)
	if invalid.TemporaryBanSeconds != 3600 {
		t.Fatalf("fallback duration = %d", invalid.TemporaryBanSeconds)
	}
}

func TestChatNotFoundIsNotTerminalCleanupError(t *testing.T) {
	if isTerminalTelegramCleanupError(errors.New("Bad Request: chat not found")) {
		t.Fatal("chat-level failure must open the cleanup breaker")
	}
	if !isTerminalTelegramCleanupError(errors.New("Bad Request: user not participant")) {
		t.Fatal("user absence should remain terminal")
	}
}
