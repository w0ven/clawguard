//go:build integration

package bot

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/openclaw/clawguard/internal/store"
	"github.com/pressly/goose/v3"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func settlementFixture(t *testing.T) (*pgxpool.Pool, *store.Queries, *Service, *telegramMockTransport) {
	t.Helper()
	ctx := context.Background()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL required")
	}
	bootstrap, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("expiry_test_%d", time.Now().UnixNano())
	if _, err = bootstrap.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = bootstrap.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		bootstrap.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	params := u.Query()
	params.Set("search_path", schema)
	u.RawQuery = params.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	_, file, _, _ := runtime.Caller(0)
	if err = goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err = goose.UpContext(ctx, db, filepath.Join(filepath.Dir(file), "..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	q := store.New(pool)
	b, tr := newMockTelegramBot(t, "")
	s := &Service{logger: zap.NewNop(), queries: q, bot: b, sender: b, sendLimiter: NewSendLimiter(), joinProtector: newJoinProtector(), cleanupBreaker: newTelegramCleanupBreaker()}
	const chat int64 = -90001
	if _, err = pool.Exec(ctx, `WITH g AS (INSERT INTO groups(chat_id,title,type) VALUES($1,'fixture','supergroup')) INSERT INTO authorized_groups(chat_id,enabled) VALUES($1,true)`, chat); err != nil {
		t.Fatal(err)
	}
	s.cacheAuthorizedGroup(ctx, chat, true)
	s.cacheSystemState(ctx, store.SystemState{})
	policy := keywordReplyPolicy("", 0)
	s.policySnapshots.Store(chat, policy)
	s.policySnapshotAt.Store(chat, time.Now())
	return pool, q, s, tr
}
func settlementTrigger(t *testing.T, p *pgxpool.Pool, table string, on bool) {
	t.Helper()
	ctx := context.Background()
	if on {
		_, err := p.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION fail_%s() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'fixture failure'; END$$; CREATE TRIGGER fail_%s BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION fail_%s()`, table, table, table, table))
		if err != nil {
			t.Fatal(err)
		}
	} else {
		_, err := p.Exec(ctx, fmt.Sprintf(`DROP TRIGGER fail_%s ON %s; DROP FUNCTION fail_%s()`, table, table, table))
		if err != nil {
			t.Fatal(err)
		}
	}
}
func cleanupCount(t *testing.T, p *pgxpool.Pool, user int64) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), `SELECT count(*) FROM violations WHERE user_id=$1`, user).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func settlementPending(t *testing.T, q *store.Queries, user int64, method string, payload string) store.PendingVerification {
	t.Helper()
	p, err := q.UpsertPendingVerification(context.Background(), store.UpsertPendingVerificationParams{ChatID: -90001, UserID: user, Method: method, Payload: []byte(payload), ExpiresAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVerificationCleanupSettlementIntegration(t *testing.T) {
	ctx := context.Background()
	pool, q, s, tr := settlementFixture(t)
	t.Run("expiry_log_failure_then_new_instance_frozen", func(t *testing.T) {
		old := settlementPending(t, q, 101, "button", `{}`)
		settlementTrigger(t, pool, "violations", true)
		before := countString(tr.Methods(), "kickChatMember")
		if _, err := s.HandleVerificationExpiry(ctx, old); err == nil {
			t.Fatal("expected log failure")
		}
		after := countString(tr.Methods(), "kickChatMember")
		if after != before+1 {
			t.Fatalf("ban calls %d->%d", before, after)
		}
		saved, err := q.PendingVerificationCleanupExists(ctx, verificationCleanupKey(old))
		if err != nil || !saved {
			t.Fatalf("missing durable result: %v %v", saved, err)
		}
		newer := settlementPending(t, q, 101, "button", `{"new":true}`)
		if old.ID == newer.ID {
			t.Fatal("upsert reused instance id")
		}
		settlementTrigger(t, pool, "violations", false)
		s.cacheSystemState(ctx, store.SystemState{Frozen: true})
		if _, err = q.UpsertGroupExemptUser(ctx, -90001, 101, "fixture", nil); err != nil {
			t.Fatal(err)
		}
		if err = s.RecoverVerificationCleanupSettlements(ctx, 20); err != nil {
			t.Fatal(err)
		}
		if err = s.RecoverVerificationCleanupSettlements(ctx, 20); err != nil {
			t.Fatal(err)
		}
		p, err := q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: -90001, UserID: 101})
		if err != nil || p.ID != newer.ID {
			t.Fatalf("new pending lost: %+v %v", p, err)
		}
		if n := cleanupCount(t, pool, 101); n != 1 {
			t.Fatalf("violations=%d", n)
		}
		if countString(tr.Methods(), "kickChatMember") != after {
			t.Fatal("repeated punishment")
		}
		if _, err = s.HandleVerificationExpiry(ctx, old); err != nil {
			t.Fatal(err)
		}
		if countString(tr.Methods(), "kickChatMember") != after {
			t.Fatal("old task punished again")
		}
		s.cacheSystemState(ctx, store.SystemState{})
	})
	t.Run("delete_failure_rolls_back_log_and_retries_once", func(t *testing.T) {
		policy := keywordReplyPolicy("", 0)
		policy.Feedback.VerifyFail.Enabled = true
		policy.Feedback.VerifyFail.Template = "fixture failure"
		s.policySnapshots.Store(int64(-90001), policy)
		defer func() { s.policySnapshots.Store(int64(-90001), keywordReplyPolicy("", 0)) }()
		feedbackBefore := countString(tr.Methods(), "sendMessage")
		old := settlementPending(t, q, 102, "button", `{}`)
		if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_pending_delete() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'fixture'; END$$; CREATE TRIGGER fail_pending_delete BEFORE DELETE ON pending_verifications FOR EACH ROW EXECUTE FUNCTION fail_pending_delete()`); err != nil {
			t.Fatal(err)
		}
		before := countString(tr.Methods(), "kickChatMember")
		if _, err := s.HandleVerificationExpiry(ctx, old); err == nil {
			t.Fatal("expected delete failure")
		}
		after := countString(tr.Methods(), "kickChatMember")
		if after != before+1 || cleanupCount(t, pool, 102) != 0 {
			t.Fatal("action/log rollback mismatch")
		}
		if _, err := pool.Exec(ctx, `DROP TRIGGER fail_pending_delete ON pending_verifications; DROP FUNCTION fail_pending_delete()`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.HandleVerificationExpiry(ctx, old); err != nil {
			t.Fatal(err)
		}
		if _, err := s.HandleVerificationExpiry(ctx, old); err != nil {
			t.Fatal(err)
		}
		if countString(tr.Methods(), "kickChatMember") != after || cleanupCount(t, pool, 102) != 1 {
			t.Fatal("duplicate action or log")
		}
		if got := countString(tr.Methods(), "sendMessage"); got != feedbackBefore+1 {
			t.Fatalf("feedback duplicated or lost: %d -> %d", feedbackBefore, got)
		}
	})
	t.Run("concurrent_settlement_is_idempotent", func(t *testing.T) {
		pending := settlementPending(t, q, 108, "button", `{}`)
		key := verificationCleanupKey(pending)
		if _, err := q.RecordVerificationCleanup(ctx, store.VerificationCleanupSettlement{Key: key, PendingID: &pending.ID, ChatID: pending.ChatID, UserID: pending.UserID, Rule: "verify_timeout", Action: "kick"}); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, _, err := q.SettleVerificationCleanup(ctx, key); errs <- err }()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if cleanupCount(t, pool, 108) != 1 {
			t.Fatal("duplicate log after concurrent settlement")
		}
	})
	t.Run("join_protection_log_retry", func(t *testing.T) {
		payload := fmt.Sprintf(`{"reason":"fixture","action":"temporary_ban","temporary_ban_seconds":3600,"action_until":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339Nano))
		old := settlementPending(t, q, 103, "join_protection_cleanup", payload)
		settlementTrigger(t, pool, "violations", true)
		before := countString(tr.Methods(), "kickChatMember")
		if _, err := s.HandleVerificationExpiry(ctx, old); err == nil {
			t.Fatal("expected join log failure")
		}
		after := countString(tr.Methods(), "kickChatMember")
		if after != before+1 {
			t.Fatalf("join action %d->%d", before, after)
		}
		settlementTrigger(t, pool, "violations", false)
		if _, err := s.HandleVerificationExpiry(ctx, old); err != nil {
			t.Fatal(err)
		}
		if cleanupCount(t, pool, 103) != 1 || countString(tr.Methods(), "kickChatMember") != after {
			t.Fatal("join duplicated or missing")
		}
	})
	t.Run("stale_instance_never_punishes_replacement", func(t *testing.T) {
		old := settlementPending(t, q, 107, "button", `{}`)
		newer := settlementPending(t, q, 107, "button", `{"new":true}`)
		before := countString(tr.Methods(), "kickChatMember")
		if _, err := s.HandleVerificationExpiry(ctx, old); err != nil {
			t.Fatal(err)
		}
		current, err := q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: -90001, UserID: 107})
		if err != nil || current.ID != newer.ID || countString(tr.Methods(), "kickChatMember") != before {
			t.Fatalf("stale instance punished or removed new pending: %+v %v", current, err)
		}
	})
	t.Run("frozen_and_exempt_before_action", func(t *testing.T) {
		old := settlementPending(t, q, 104, "button", `{}`)
		before := countString(tr.Methods(), "kickChatMember")
		s.cacheSystemState(ctx, store.SystemState{Frozen: true})
		r, err := s.HandleVerificationExpiry(ctx, old)
		if err != nil || r.RetryAt.IsZero() {
			t.Fatalf("frozen %+v %v", r, err)
		}
		s.cacheSystemState(ctx, store.SystemState{})
		if _, err = q.UpsertGroupExemptUser(ctx, -90001, 104, "fixture", nil); err != nil {
			t.Fatal(err)
		}
		r, err = s.HandleVerificationExpiry(ctx, old)
		if err != nil || r.RetryAt.IsZero() {
			t.Fatalf("exempt %+v %v", r, err)
		}
		if countString(tr.Methods(), "kickChatMember") != before {
			t.Fatal("punishment despite gate")
		}
	})
	t.Run("unauthorized_and_legacy_redis_are_not_punished", func(t *testing.T) {
		old := settlementPending(t, q, 109, "button", `{}`)
		before := countString(tr.Methods(), "kickChatMember")
		s.cacheAuthorizedGroup(ctx, -90001, false)
		r, err := s.HandleVerificationExpiry(ctx, old)
		if err != nil || r.RetryAt.IsZero() {
			t.Fatalf("unauthorized result: %+v %v", r, err)
		}
		s.cacheAuthorizedGroup(ctx, -90001, true)
		_, client := newJoinProtectionTestRedis(t)
		s.redis = client
		defer func() { s.redis = nil }()
		legacy := joinProtectionDeferredCleanup{ChatID: -90001, UserID: 110, Action: "ban"}
		if err = enqueueJoinProtectionCleanupRedis(ctx, client, legacy, time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		if err = s.ProcessJoinProtectionCleanupFallbacks(ctx, 1); err != nil {
			t.Fatal(err)
		}
		if countString(tr.Methods(), "kickChatMember") != before {
			t.Fatal("unsafe action by unauthorized or legacy task")
		}
		if n := client.ZCard(ctx, joinProtectionCleanupQueueKey).Val(); n != 1 {
			t.Fatalf("legacy task not retained: %d", n)
		}
	})
	t.Run("redis_log_failure_retries_without_action", func(t *testing.T) {
		_, client := newJoinProtectionTestRedis(t)
		s.redis = client
		defer func() { s.redis = nil }()
		if err := s.rememberMembershipSession(ctx, -90001, 111, "redis-log"); err != nil {
			t.Fatal(err)
		}
		task := joinProtectionDeferredCleanup{ChatID: -90001, UserID: 111, Action: "ban", MembershipGeneration: "redis-log"}
		if err := enqueueJoinProtectionCleanupRedis(ctx, client, task, time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		settlementTrigger(t, pool, "violations", true)
		before := countString(tr.Methods(), "kickChatMember")
		if err := s.ProcessJoinProtectionCleanupFallbacks(ctx, 1); err != nil {
			t.Fatal(err)
		}
		after := countString(tr.Methods(), "kickChatMember")
		if after != before+1 || cleanupCount(t, pool, 111) != 0 {
			t.Fatal("redis initial action/log mismatch")
		}
		members, err := client.ZRange(ctx, joinProtectionCleanupQueueKey, 0, -1).Result()
		if err != nil || len(members) != 1 {
			t.Fatalf("missing redis retry: %v %v", members, err)
		}
		settlementTrigger(t, pool, "violations", false)
		s.cacheSystemState(ctx, store.SystemState{Frozen: true})
		if err = client.ZAdd(ctx, joinProtectionCleanupQueueKey, redis.Z{Score: float64(time.Now().Add(-time.Second).UnixMilli()), Member: members[0]}).Err(); err != nil {
			t.Fatal(err)
		}
		if err = s.ProcessJoinProtectionCleanupFallbacks(ctx, 1); err != nil {
			t.Fatal(err)
		}
		if countString(tr.Methods(), "kickChatMember") != after || cleanupCount(t, pool, 111) != 1 {
			t.Fatal("redis retry repeated action or lost log")
		}
		s.cacheSystemState(ctx, store.SystemState{})
	})
	t.Run("redis_old_session_cannot_touch_new_pending", func(t *testing.T) {
		_, client := newJoinProtectionTestRedis(t)
		s.redis = client
		defer func() { s.redis = nil }()
		if err := s.rememberMembershipSession(ctx, -90001, 112, "old-session"); err != nil {
			t.Fatal(err)
		}
		if err := enqueueJoinProtectionCleanupRedis(ctx, client, joinProtectionDeferredCleanup{ChatID: -90001, UserID: 112, Action: "ban", MembershipGeneration: "old-session"}, time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := s.rememberMembershipSession(ctx, -90001, 112, "new-session"); err != nil {
			t.Fatal(err)
		}
		newer := settlementPending(t, q, 112, "button", `{}`)
		before := countString(tr.Methods(), "kickChatMember")
		if err := s.ProcessJoinProtectionCleanupFallbacks(ctx, 1); err != nil {
			t.Fatal(err)
		}
		current, err := q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: -90001, UserID: 112})
		if err != nil || current.ID != newer.ID || countString(tr.Methods(), "kickChatMember") != before {
			t.Fatalf("redis old task affected replacement: %+v %v", current, err)
		}
	})
	t.Run("redis_result_survives_db_failure_and_freeze", func(t *testing.T) {
		_, client := newJoinProtectionTestRedis(t)
		s.redis = client
		defer func() { s.redis = nil }()
		if err := s.rememberMembershipSession(ctx, -90001, 105, "session1"); err != nil {
			t.Fatal(err)
		}
		task := joinProtectionDeferredCleanup{ChatID: -90001, UserID: 105, Action: joinProtectionActionTemporaryBan, TemporaryBanSeconds: 3600, ActionUntil: time.Now().Add(time.Hour), MembershipGeneration: "session1"}
		if err := enqueueJoinProtectionCleanupRedis(ctx, client, task, time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		settlementTrigger(t, pool, "verification_cleanup_settlements", true)
		before := countString(tr.Methods(), "kickChatMember")
		if err := s.ProcessJoinProtectionCleanupFallbacks(ctx, 1); err != nil {
			t.Fatal(err)
		}
		after := countString(tr.Methods(), "kickChatMember")
		if after != before+1 {
			t.Fatalf("redis action %d->%d", before, after)
		}
		members, err := client.ZRange(ctx, joinProtectionCleanupQueueKey, 0, -1).Result()
		if err != nil || len(members) != 1 {
			t.Fatalf("redis members=%v %v", members, err)
		}
		v, err := client.HGet(ctx, joinProtectionCleanupTaskKey(members[0]), "completed_action").Result()
		if err != nil || v == "" {
			t.Fatalf("missing redis completion: %s %v", v, err)
		}
		if err := cancelJoinProtectionCleanupRedis(ctx, client, -90001, 105); err != nil {
			t.Fatal(err)
		}
		if n := client.Exists(ctx, joinProtectionCleanupTaskKey(members[0])).Val(); n != 1 {
			t.Fatal("cancel discarded a completed result")
		}
		// A later cleanup for the same session is a distinct instance; finishing
		// the old result must neither delete nor act on the replacement.
		if err := enqueueJoinProtectionCleanupRedis(ctx, client, task, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		replacement, err := client.Get(ctx, joinProtectionCleanupCurrentKey(-90001, 105)).Result()
		if err != nil || replacement == members[0] {
			t.Fatalf("replacement=%q old=%q err=%v", replacement, members[0], err)
		}
		settlementTrigger(t, pool, "verification_cleanup_settlements", false)
		s.cacheSystemState(ctx, store.SystemState{Frozen: true})
		if _, err = q.UpsertGroupExemptUser(ctx, -90001, 105, "fixture", nil); err != nil {
			t.Fatal(err)
		}
		if err = client.ZAdd(ctx, joinProtectionCleanupQueueKey, redis.Z{Score: float64(time.Now().Add(-time.Second).UnixMilli()), Member: members[0]}).Err(); err != nil {
			t.Fatal(err)
		}
		if err = s.ProcessJoinProtectionCleanupFallbacks(ctx, 1); err != nil {
			t.Fatal(err)
		}
		if countString(tr.Methods(), "kickChatMember") != after || cleanupCount(t, pool, 105) != 1 {
			t.Fatal("redis repeated action or lost log")
		}
		if n := client.Exists(ctx, joinProtectionCleanupTaskKey(members[0])).Val(); n != 0 {
			t.Fatal("completed redis task remains")
		}
		if n := client.Exists(ctx, joinProtectionCleanupTaskKey(replacement)).Val(); n != 1 {
			t.Fatal("new redis task removed")
		}
		current, err := client.Get(ctx, joinProtectionCleanupCurrentKey(-90001, 105)).Result()
		if err != nil || current != replacement {
			t.Fatalf("new redis pointer lost: %q %v", current, err)
		}
		s.cacheSystemState(ctx, store.SystemState{})
	})
	t.Run("poison_log_does_not_starve_other_settlements", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_one_cleanup() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.user_id=113 THEN RAISE EXCEPTION 'fixture'; END IF; RETURN NEW; END$$; CREATE TRIGGER fail_one_cleanup BEFORE INSERT ON violations FOR EACH ROW EXECUTE FUNCTION fail_one_cleanup()`); err != nil {
			t.Fatal(err)
		}
		for _, user := range []int64{113, 114} {
			if _, err := q.RecordVerificationCleanup(ctx, store.VerificationCleanupSettlement{Key: fmt.Sprintf("fixture:%d", user), ChatID: -90001, UserID: user, Rule: "verify_timeout", Action: "kick"}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.RecoverVerificationCleanupSettlements(ctx, 1); err == nil {
			t.Fatal("expected poison row failure")
		}
		if err := s.RecoverVerificationCleanupSettlements(ctx, 1); err != nil {
			t.Fatal(err)
		}
		if cleanupCount(t, pool, 114) != 1 {
			t.Fatal("healthy row was starved")
		}
		if _, err := pool.Exec(ctx, `DROP TRIGGER fail_one_cleanup ON violations; DROP FUNCTION fail_one_cleanup(); UPDATE verification_cleanup_settlements SET next_attempt_at=now() WHERE operation_key='fixture:113'`); err != nil {
			t.Fatal(err)
		}
		if err := s.RecoverVerificationCleanupSettlements(ctx, 1); err != nil {
			t.Fatal(err)
		}
		if cleanupCount(t, pool, 113) != 1 {
			t.Fatal("poison row not recovered")
		}
	})

}
