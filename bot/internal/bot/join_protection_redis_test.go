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
	server, client := newJoinProtectionTestRedis(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	task := joinProtectionDeferredCleanup{
		ChatID:               400,
		UserID:               500,
		Username:             "queued-user",
		FirstName:            "Queued",
		TemporaryBanSeconds:  3600,
		ActionUntil:          now.Add(time.Hour),
		Reason:               "temporary_ban_failed",
		Action:               joinProtectionActionTemporaryBan,
		Rule:                 "join_protection_cleanup_test",
		MembershipGeneration: "u123",
	}
	if err := enqueueJoinProtectionCleanupRedis(ctx, client, task, now); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := claimDueJoinProtectionCleanupRedis(ctx, client, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || loaded.ChatID != task.ChatID || loaded.UserID != task.UserID || loaded.TemporaryBanSeconds != 3600 || loaded.Action != task.Action || loaded.Rule != task.Rule || loaded.MembershipGeneration != task.MembershipGeneration || !loaded.ActionUntil.Equal(task.ActionUntil) {
		t.Fatalf("loaded = %+v", loaded)
	}
	if loaded.Member != "400:500:u123" {
		t.Fatalf("member = %q", loaded.Member)
	}
	if ttl := server.TTL(joinProtectionCleanupTaskKey(loaded.Member)); ttl != 0 {
		t.Fatalf("cleanup task TTL = %s, want no expiry", ttl)
	}
	if ttl := server.TTL(joinProtectionCleanupGroupKey(task.ChatID)); ttl != 0 {
		t.Fatalf("cleanup group TTL = %s, want no expiry", ttl)
	}
	if got, err := client.Get(ctx, membershipSessionKey(task.ChatID, task.UserID)).Result(); err != nil || got != task.MembershipGeneration {
		t.Fatalf("membership session = %q, err=%v", got, err)
	}
	if ttl := server.TTL(membershipSessionKey(task.ChatID, task.UserID)); ttl <= 0 {
		t.Fatalf("membership session TTL = %s, want positive TTL", ttl)
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

func TestCancelJoinProtectionCleanupRemovesEveryMembershipGeneration(t *testing.T) {
	server, client := newJoinProtectionTestRedis(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	for _, generation := range []string{"u1", "u2"} {
		if err := enqueueJoinProtectionCleanupRedis(ctx, client, joinProtectionDeferredCleanup{
			ChatID:               410,
			UserID:               510,
			TemporaryBanSeconds:  3600,
			ActionUntil:          now.Add(time.Hour),
			Action:               joinProtectionActionTemporaryBan,
			MembershipGeneration: generation,
		}, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := enqueueJoinProtectionCleanupRedis(ctx, client, joinProtectionDeferredCleanup{
		ChatID:               410,
		UserID:               511,
		TemporaryBanSeconds:  3600,
		ActionUntil:          now.Add(time.Hour),
		Action:               joinProtectionActionTemporaryBan,
		MembershipGeneration: "other",
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, membershipSessionKey(410, 510), "u2", time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	if err := cancelJoinProtectionCleanupRedis(ctx, client, 410, 510); err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"410:510:u1", "410:510:u2"} {
		if server.Exists(joinProtectionCleanupTaskKey(member)) {
			t.Fatalf("cleanup task %q still exists", member)
		}
		if score, err := client.ZScore(ctx, joinProtectionCleanupQueueKey, member).Result(); err != redis.Nil || score != 0 {
			t.Fatalf("queue member %q score=%v err=%v", member, score, err)
		}
	}
	if server.Exists(membershipSessionKey(410, 510)) {
		t.Fatal("membership session still exists")
	}
	if !server.Exists(joinProtectionCleanupTaskKey("410:511:other")) {
		t.Fatal("another user's cleanup task was removed")
	}
}

func TestRedisDeferredCleanupQueueAcceptsSnapshottedVerificationAction(t *testing.T) {
	_, client := newJoinProtectionTestRedis(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	task := joinProtectionDeferredCleanup{
		ChatID: 501,
		UserID: 601,
		Action: "ban",
		Rule:   "verification_prompt_failed_redis_cleanup",
	}
	if err := enqueueJoinProtectionCleanupRedis(ctx, client, task, now); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := claimDueJoinProtectionCleanupRedis(ctx, client, now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim generic cleanup: ok=%t err=%v", ok, err)
	}
	if loaded.Action != "ban" || loaded.Rule != task.Rule || loaded.TemporaryBanSeconds != 0 {
		t.Fatalf("loaded = %+v", loaded)
	}
}

func TestRestoreActiveJoinProtectionFromMemoryShadow(t *testing.T) {
	_, client := newJoinProtectionTestRedis(t)
	ctx := context.Background()
	policy := testJoinProtectionPolicy()
	policy.JoinThreshold = 2
	now := time.Unix(1_800_000_000, 0)
	protector := newJoinProtector()
	protector.ObserveJoin(701, policy, 0, now)
	entered := protector.ObserveJoin(701, policy, 0, now.Add(time.Second))
	if !entered.Entered {
		t.Fatalf("memory decision = %+v", entered)
	}
	snapshot, ok := protector.Snapshot(701, policy, now.Add(2*time.Second))
	if !ok {
		t.Fatal("missing active memory snapshot")
	}
	if err := restoreJoinProtectionRedis(ctx, client, 701, snapshot, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	observation, err := observeJoinProtectionRedis(ctx, client, 701, policy, nil, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if observation.NeedsData || !observation.Decision.Protect || observation.Decision.Entered || observation.Decision.Intercepted != 2 {
		t.Fatalf("restored observation = %+v", observation)
	}
}

func TestRestoreJoinWindowFromMemoryShadow(t *testing.T) {
	_, client := newJoinProtectionTestRedis(t)
	ctx := context.Background()
	policy := testJoinProtectionPolicy()
	policy.JoinThreshold = 3
	now := time.Unix(1_800_000_000, 0)
	protector := newJoinProtector()
	protector.ObserveJoin(702, policy, 0, now)
	protector.ObserveJoin(702, policy, 0, now.Add(time.Second))
	snapshot, ok := protector.Snapshot(702, policy, now.Add(2*time.Second))
	if !ok || len(snapshot.Joins) != 2 {
		t.Fatalf("memory snapshot = %+v", snapshot)
	}
	if err := restoreJoinProtectionWindowRedis(ctx, client, 702, snapshot.Joins, policy, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	pending := 0
	observation, err := observeJoinProtectionRedis(ctx, client, 702, policy, &pending, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Decision.Protect || !observation.Decision.Entered || observation.Decision.Trigger != "join_threshold" {
		t.Fatalf("restored window decision = %+v", observation.Decision)
	}
}

func TestJoinProtectionCleanupPayloadUsesTemporaryBanDuration(t *testing.T) {
	createdAt := time.Unix(1_800_000_000, 0)
	payload := decodeJoinProtectionCleanupPayload([]byte(`{"reason":"failed","temporary_ban_seconds":7200}`), 3600, createdAt)
	if payload.TemporaryBanSeconds != 7200 || payload.Reason != "failed" || !payload.ActionUntil.Equal(createdAt.Add(2*time.Hour)) {
		t.Fatalf("payload = %+v", payload)
	}
	invalid := decodeJoinProtectionCleanupPayload([]byte(`{"temporary_ban_seconds":0}`), 3600, createdAt)
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
