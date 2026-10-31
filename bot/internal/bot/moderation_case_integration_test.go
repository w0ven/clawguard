//go:build integration

package bot

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
)

func TestFilterCaseTracksHumanAndDeduplicates(t *testing.T) {
	ctx := context.Background()
	pool, q, s, tr := settlementFixture(t)
	if err := q.RecordPrivateStart(ctx, 101); err != nil {
		t.Fatal(err)
	}
	policy := config.DefaultPolicy
	policy.Feedback.DeleteMsg.Enabled = false
	msg := &tele.Message{ID: 42, Text: "test person@example.com 13800138000", Chat: &tele.Chat{ID: -90001, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 101}}
	if err := s.applyFilterAction(ctx, msg, policy, FilterResult{Hit: true, Reason: "filter_keyword", Action: "delete"}); err != nil {
		t.Fatal(err)
	}
	var cases, queued int
	var summary string
	if err := pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE n.state='pending'),max(c.summary) FROM moderation_cases c JOIN moderation_notifications n ON n.case_id=c.id`).Scan(&cases, &queued, &summary); err != nil {
		t.Fatal(err)
	}
	if cases != 1 || queued != 1 || summary != "test [邮箱] [号码]" {
		t.Fatalf("case=%d pending=%d summary=%q", cases, queued, summary)
	}
	key := moderationEventKey(msg, "filter_case", "filter_keyword:"+sha256HexString(collectMessageContent(msg)), "delete")
	if err := s.recordMessageModerationCase(ctx, msg, "filter_event", key, "delete", "failed", "群规则处理", 0); err != nil {
		t.Fatal(err)
	}
	var result string
	if err := pool.QueryRow(ctx, `SELECT result FROM moderation_cases WHERE source_id=$1`, key).Scan(&result); err != nil || result != "executed" {
		t.Fatalf("replay regressed result %s %v", result, err)
	}
	msg.ID = 44
	key2 := moderationEventKey(msg, "filter", "filter_keyword", "delete_mute")
	if err := s.recordMessageModerationCase(ctx, msg, "filter_event", key2, "delete_mute", "partial", "仅消息删除成功", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.recordMessageModerationCase(ctx, msg, "filter_event", key2, "delete_mute", "executed", "群规则处理", 0); err != nil {
		t.Fatal(err)
	}
	var explanation string
	if err := pool.QueryRow(ctx, `SELECT result,explanation FROM moderation_cases WHERE source_id=$1`, key2).Scan(&result, &explanation); err != nil || result != "executed" || explanation != "群规则处理" {
		t.Fatalf("retry result %s explanation=%s %v", result, explanation, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM moderation_notifications`).Scan(&cases); err != nil || cases != 2 {
		t.Fatalf("duplicate count %d %v", cases, err)
	}
	if countString(tr.Methods(), "sendMessage") != 0 {
		t.Fatal("penalty path sent private notification synchronously")
	}
	msg.Sender = &tele.User{ID: 102, IsBot: true}
	msg.ID = 43
	if err := s.applyFilterAction(ctx, msg, policy, FilterResult{Hit: true, Reason: "filter_keyword", Action: "delete"}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM moderation_cases`).Scan(&cases); err != nil || cases != 2 {
		t.Fatalf("bot got case %d %v", cases, err)
	}
}

func TestFilterPartialThenRetryCaseIntegration(t *testing.T) {
	ctx := context.Background()
	pool, q, s, tr := settlementFixture(t)
	_, client := newJoinProtectionTestRedis(t)
	s.redis = client
	if err := q.RecordPrivateStart(ctx, 101); err != nil {
		t.Fatal(err)
	}
	policy := config.DefaultPolicy
	policy.Feedback.DeleteMsg.Enabled = false
	policy.Feedback.Ban.Enabled = false
	msg := &tele.Message{ID: 99, Text: "fixture", Chat: &tele.Chat{ID: -90001, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 101}}
	tr.mu.Lock()
	tr.failBanUserID = 101
	tr.mu.Unlock()
	res := FilterResult{Hit: true, Reason: "filter_keyword", Action: "delete_ban"}
	if err := s.applyFilterAction(ctx, msg, policy, res); err == nil {
		t.Fatal("expected ban failure")
	}
	var result string
	if err := pool.QueryRow(ctx, `SELECT result FROM moderation_cases`).Scan(&result); err != nil || result != "partial" {
		t.Fatalf("partial=%s %v", result, err)
	}
	tr.mu.Lock()
	tr.failBanUserID = 0
	tr.mu.Unlock()
	if err := s.applyFilterAction(ctx, msg, policy, res); err != nil {
		t.Fatal(err)
	}
	var cases, notifications int
	if err := pool.QueryRow(ctx, `SELECT count(*),max(result),(SELECT count(*) FROM moderation_notifications) FROM moderation_cases`).Scan(&cases, &result, &notifications); err != nil || cases != 1 || notifications != 1 || result != "executed" {
		t.Fatalf("cases=%d notifications=%d result=%s %v", cases, notifications, result, err)
	}
	if countString(tr.Methods(), "deleteMessage") != 1 {
		t.Fatal("repeated delete")
	}
}

func TestCommandAuditReversalAndBotIdentityIntegration(t *testing.T) {
	ctx := context.Background()
	pool, q, svc, _ := settlementFixture(t)
	chat := &tele.Chat{ID: -90001, Type: tele.ChatSuperGroup}
	operator := &tele.User{ID: 200}
	for _, id := range []int64{101, 102, 103} {
		if err := q.RecordPrivateStart(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_trust(chat_id,user_id,status,score,is_bot) VALUES(-90001,102,'new',0.5,true)`); err != nil {
		t.Fatal(err)
	}
	svc.writeCommandModerationAudit(ctx, "unban", operator, chat, &tele.User{ID: 101}, "unban", "manual_unban", 0, 50, "success", "")
	svc.writeCommandModerationAudit(ctx, "unban", operator, chat, &tele.User{ID: 101}, "unban", "manual_unban", 0, 50, "success", "")
	svc.writeCommandModerationAudit(ctx, "unban", operator, chat, &tele.User{ID: 101}, "unban", "manual_unban", 0, 51, "skipped", "")
	svc.writeCommandModerationAudit(ctx, "spam", operator, chat, &tele.User{ID: 102}, "ban", "spam", 0, 52, "success", "")
	svc.writeCommandModerationAudit(ctx, "spam", operator, chat, &tele.User{ID: 103, IsBot: true}, "ban", "spam", 0, 53, "success", "")
	var count int
	var result string
	if err := pool.QueryRow(ctx, `SELECT count(*),max(result) FROM moderation_cases`).Scan(&count, &result); err != nil || count != 1 || result != "partial" {
		t.Fatalf("count=%d result=%q err=%v", count, result, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM moderation_notifications`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("notifications=%d err=%v", count, err)
	}
	msg := &tele.Message{Text: "/spam", Chat: chat, ReplyTo: &tele.Message{ID: 10, Sender: &tele.User{ID: 103, IsBot: true}}}
	target, err := svc.resolveCommandTarget(ctx, msg, chat, 1)
	if err != nil || !target.IsBot {
		t.Fatalf("target=%+v err=%v", target, err)
	}
}

func TestSpamCommandPartialBanFailureIntegration(t *testing.T) {
	ctx := context.Background()
	pool, q, svc, tr := settlementFixture(t)
	svc.verifyBtn = tele.Btn{Unique: "verify_human"}
	svc.verifyMathBtn = tele.Btn{Unique: "verify_math"}
	svc.verifyRandBtn = tele.Btn{Unique: "verify_random"}
	svc.verifyAdminBtn = tele.Btn{Unique: "verify_admin"}
	svc.registerHandlers()
	svc.chatAdmins.Store("chat_admin:-90001:1", chatAdminCacheEntry{IsAdmin: true, ExpireAt: time.Now().Add(time.Hour)})
	if err := q.RecordPrivateStart(ctx, 101); err != nil {
		t.Fatal(err)
	}
	tr.mu.Lock()
	tr.failBanUserID = 101
	tr.mu.Unlock()
	msg := &tele.Message{ID: 222, Text: "/spam", Chat: &tele.Chat{ID: -90001, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 1}, ReplyTo: &tele.Message{ID: 221, Sender: &tele.User{ID: 101}}}
	if err := svc.ProcessUpdate(tele.Update{ID: 222, Message: msg}); err != nil {
		t.Fatal(err)
	}
	var result, action string
	if err := pool.QueryRow(ctx, `SELECT result,action FROM moderation_cases WHERE source_kind='command_event'`).Scan(&result, &action); err != nil || result != "partial" || action != "delete_ban" {
		t.Fatalf("result=%q action=%q err=%v methods=%v", result, action, err, tr.Methods())
	}
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM moderation_notifications`).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("state=%s err=%v", state, err)
	}
}

func TestAIPartialActionCreatesReachabilitySafeCase(t *testing.T) {
	ctx := context.Background()
	pool, _, svc, tr := settlementFixture(t)
	tr.mu.Lock()
	tr.failBanUserID = 101
	tr.mu.Unlock()
	msg := &tele.Message{ID: 151, Text: "fixture", Chat: &tele.Chat{ID: -90001, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 101}}
	if err := svc.applyAIAction(ctx, msg, config.DefaultPolicy, store.UserTrust{Status: "new", Score: .5}, ai.CheckOutput{}, "ban", false); err == nil {
		t.Fatal("expected injected ban failure")
	}
	var result, state, explanation string
	if err := pool.QueryRow(ctx, `SELECT c.result,n.state,c.explanation FROM moderation_cases c JOIN moderation_notifications n ON n.case_id=c.id WHERE c.source_kind='ai_event'`).Scan(&result, &state, &explanation); err != nil {
		t.Fatal(err)
	}
	if result != "partial" || state != "unreachable" || explanation != "消息已删除，其他处理步骤未确认，请联系管理员核查" {
		t.Fatalf("result=%s state=%s explanation=%s", result, state, explanation)
	}
	if countString(tr.Methods(), "sendMessage") != 0 {
		t.Fatal("partial penalty synchronously sent a private message")
	}
}

func TestWarningEscalationIdentityIntegration(t *testing.T) {
	ctx := context.Background()
	pool, q, s, _ := settlementFixture(t)
	if err := q.RecordPrivateStart(ctx, 101); err != nil {
		t.Fatal(err)
	}
	p := config.DefaultPolicy
	p.Warnings.Enabled = true
	p.Warnings.MaxWarns = 1
	p.Warnings.ActionAtMax = "mute"
	p.Feedback.Warn.Enabled = false
	p.Feedback.Mute.Enabled = false
	chat := &tele.Chat{ID: -90001, Type: tele.ChatSuperGroup}
	u := &tele.User{ID: 101}
	count, escalated, err := s.incrWarningForEvent(ctx, chat, u, "test", p, false, false, "warning-test-event")
	if err != nil || count != 1 || !escalated {
		t.Fatalf("count=%d escalated=%v err=%v", count, escalated, err)
	}
	var warningID, caseWarning int64
	var action string
	if err = pool.QueryRow(ctx, `SELECT w.id,(c.ownership->>'warning_id')::bigint,c.action FROM warnings w JOIN moderation_cases c ON c.source_kind='warning_escalation'`).Scan(&warningID, &caseWarning, &action); err != nil || warningID != caseWarning || action != "mute" {
		t.Fatalf("warning=%d case=%d action=%s err=%v", warningID, caseWarning, action, err)
	}
	_, _, err = s.incrWarningForEvent(ctx, chat, u, "test", p, false, false, "warning-test-event")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM moderation_cases`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("cases=%d err=%v", n, err)
	}
}

func TestVersionedCleanupCasesIntegration(t *testing.T) {
	ctx := context.Background()
	pool, q, s, _ := settlementFixture(t)
	_, redisClient := newJoinProtectionTestRedis(t)
	s.redis = redisClient
	task := joinProtectionDeferredCleanup{ChatID: -90001, UserID: 101, MembershipGeneration: "u123", Action: "temporary_ban", TemporaryBanSeconds: 300, ActionUntil: time.Now().Add(time.Minute)}
	if err := enqueueJoinProtectionCleanupRedis(ctx, redisClient, task, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, ok, err := claimDueJoinProtectionCleanupRedis(ctx, redisClient, time.Now().Add(time.Second), time.Minute)
	if err != nil || !ok || got.NotificationVersion != "1" {
		t.Fatalf("task=%+v ok=%v err=%v", got, ok, err)
	}
	if err = s.settleRedisJoinCleanup(ctx, got, "temporary_ban"); err != nil {
		t.Fatal(err)
	}
	if err = s.settleRedisJoinCleanup(ctx, got, "temporary_ban"); err != nil {
		t.Fatal(err)
	}
	old := got
	old.Member = "old-legacy"
	old.NotificationVersion = ""
	if err = s.settleRedisJoinCleanup(ctx, old, "temporary_restrict"); err != nil {
		t.Fatal(err)
	}
	if _, err = q.RecordVerificationCleanup(ctx, store.VerificationCleanupSettlement{Key: "pending:fixture", ChatID: -90001, UserID: 102, Action: "temporary_restrict", Rule: "join_protection_deferred_action_cleanup"}); err != nil {
		t.Fatal(err)
	}
	if _, err = q.RecordVerificationCleanup(ctx, store.VerificationCleanupSettlement{Key: "redis:new-partial", ChatID: -90001, UserID: 103, Action: "partial_kick", NotifyRedis: true}); err != nil {
		t.Fatal(err)
	}
	var partial string
	if err = pool.QueryRow(ctx, `SELECT result FROM moderation_cases WHERE user_id=103`).Scan(&partial); err != nil || partial != "partial" {
		t.Fatalf("partial=%s err=%v", partial, err)
	}
	var n int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM moderation_cases`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("cases=%d err=%v", n, err)
	}
	var nBan, nMute int
	if err = pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE action='ban'),count(*) FILTER(WHERE action='mute') FROM moderation_cases`).Scan(&nBan, &nMute); err != nil || nBan != 1 || nMute != 1 {
		t.Fatalf("ban=%d mute=%d err=%v", nBan, nMute, err)
	}
}

func TestWarningPersistedWhenEscalationFailsIntegration(t *testing.T) {
	ctx := context.Background()
	pool, q, s, tr := settlementFixture(t)
	if err := q.RecordPrivateStart(ctx, 101); err != nil {
		t.Fatal(err)
	}
	p := config.DefaultPolicy
	p.Warnings.Enabled = true
	p.Warnings.MaxWarns = 1
	p.Warnings.ActionAtMax = "ban"
	p.Feedback.Warn.Enabled = false
	p.Feedback.Ban.Enabled = false
	tr.mu.Lock()
	tr.failBanUserID = 101
	tr.mu.Unlock()
	msg := &tele.Message{ID: 400, Text: "blocked", Chat: &tele.Chat{ID: -90001, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 101}}
	err := s.applyFilterAction(ctx, msg, p, FilterResult{Hit: true, Reason: "filter_keyword", Action: "warn"})
	if err == nil {
		t.Fatal("expected injected escalation failure")
	}
	var warnings int
	var result string
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM warnings), result FROM moderation_cases WHERE source_kind='filter_event'`).Scan(&warnings, &result); err != nil || warnings != 1 || result != "partial" {
		t.Fatalf("warnings=%d result=%s err=%v", warnings, result, err)
	}
	before := countString(tr.Methods(), "kickChatMember")
	_ = s.applyFilterAction(ctx, msg, p, FilterResult{Hit: true, Reason: "filter_keyword", Action: "warn"})
	if countString(tr.Methods(), "kickChatMember") != before {
		t.Fatal("uncertain escalation was dispatched twice")
	}
}

func TestKickPartialReturnsAppliedError(t *testing.T) {
	err := runVerificationKick(func() error { return nil }, func() error { return fmt.Errorf("network unavailable") })
	var applied *actionAppliedError
	if !errors.As(err, &applied) || applied.action != "ban" {
		t.Fatalf("partial result %v", err)
	}
	err = runVerificationKick(func() error { return fmt.Errorf("ban failed") }, func() error { t.Fatal("unban must not be called"); return nil })
	if errors.As(err, &applied) {
		t.Fatalf("unconfirmed ban marked applied: %v", err)
	}
}

func TestBotInviterHumanAndNewMemberCaseIntegration(t *testing.T) {
	ctx := context.Background()
	pool, q, s, _ := settlementFixture(t)
	chat := &tele.Chat{ID: -90001, Type: tele.ChatSuperGroup}
	inviter := &tele.User{ID: 101}
	botUser := &tele.User{ID: 102, IsBot: true}
	if err := q.RecordPrivateStart(ctx, 101); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO user_trust(chat_id,user_id,status,score,is_bot,notes) VALUES(-90001,102,'new',0.5,true,$1),(-90001,101,'new',0.5,false,NULL)`, botTrustNotes("fixture", inviter))
	if err != nil {
		t.Fatal(err)
	}
	p := config.DefaultPolicy
	p.Filter.BanBotInviterOnViolation = true
	msg := &tele.Message{ID: 500, Text: "bad", Chat: chat, Sender: botUser}
	s.maybeBanBotInviterAfterViolation(ctx, msg, p, "ban", "fixture")
	var id int64
	if err = pool.QueryRow(ctx, `SELECT user_id FROM moderation_cases WHERE source_kind='bot_inviter'`).Scan(&id); err != nil || id != 101 {
		t.Fatalf("inviter=%d err=%v", id, err)
	}
	p.Filter.NewUser.Enabled = true
	p.Filter.NewUser.NoInvites = true
	target := tele.User{ID: 103}
	if !s.handleUngraduatedInvite(ctx, chat, inviter, []tele.User{target}, nil, p, "chat_member", "u500") {
		t.Fatal("expected kick")
	}
	var action string
	if err = pool.QueryRow(ctx, `SELECT action FROM moderation_cases WHERE source_kind='ungraduated_invite' AND user_id=103`).Scan(&action); err != nil || action != "kick" {
		t.Fatalf("action=%s err=%v", action, err)
	}
}

func TestVerificationPartialKickDoesNotRepeatIntegration(t *testing.T) {
	ctx := context.Background()
	pool, q, s, tr := settlementFixture(t)
	p := settlementPending(t, q, 101, "button", `{}`)
	chat := &tele.Chat{ID: -90001}
	user := &tele.User{ID: 101}
	tr.mu.Lock()
	tr.failUnban = true
	tr.mu.Unlock()
	err := s.failVerificationImmediately(ctx, chat, user, p, "kick", "verify_wrong")
	if err == nil {
		t.Fatal("expected unban failure")
	}
	var result string
	if err = pool.QueryRow(ctx, `SELECT result FROM moderation_cases WHERE source_kind='verification_immediate'`).Scan(&result); err != nil || result != "partial" {
		t.Fatalf("result=%s err=%v", result, err)
	}
	before := countString(tr.Methods(), "kickChatMember")
	err = s.failVerificationImmediately(ctx, chat, user, p, "kick", "verify_wrong")
	if err == nil || countString(tr.Methods(), "kickChatMember") != before {
		t.Fatalf("retry dispatched another ban: %v %v", err, tr.Methods())
	}
}

func TestPromptFailurePartialDoesNotRepeatIntegration(t *testing.T) {
	ctx := context.Background()
	pool, _, s, tr := settlementFixture(t)
	chat := &tele.Chat{ID: -90001}
	user := &tele.User{ID: 101}
	p := config.DefaultPolicy
	p.Verify.FailAction = "kick"
	tr.mu.Lock()
	tr.failUnban = true
	tr.mu.Unlock()
	if err := s.handleVerificationPromptFailure(ctx, chat, user, p, fmt.Errorf("prompt failure"), "u991"); err != nil {
		t.Fatal(err)
	}
	var result string
	if err := pool.QueryRow(ctx, `SELECT result FROM moderation_cases WHERE source_kind='verification_prompt_failed'`).Scan(&result); err != nil || result != "partial" {
		t.Fatalf("result=%s err=%v", result, err)
	}
	before := countString(tr.Methods(), "kickChatMember")
	if err := s.handleVerificationPromptFailure(ctx, chat, user, p, fmt.Errorf("prompt failure"), "u991"); err != nil {
		t.Fatal(err)
	}
	if countString(tr.Methods(), "kickChatMember") != before {
		t.Fatal("repeated ban")
	}
}

func TestJoinDeletedAccountAndAIFallbackCasesIntegration(t *testing.T) {
	ctx := context.Background()
	pool, _, s, _ := settlementFixture(t)
	chat := &tele.Chat{ID: -90001, Type: tele.ChatSuperGroup}
	p := config.DefaultPolicy
	p.Filter.DeletedAccount.Enabled = true
	p.Feedback.DeleteMsg.Enabled = false
	user := &tele.User{ID: 101, FirstName: "Deleted Account"}
	handled, err := s.handleDeletedAccountJoin(ctx, chat, user, p, false, "u123")
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	var generation string
	if err = pool.QueryRow(ctx, `SELECT ownership->>'membership_generation' FROM moderation_cases WHERE source_kind='deleted_account_join'`).Scan(&generation); err != nil || generation != "u123" {
		t.Fatalf("generation=%s err=%v", generation, err)
	}
	msg := &tele.Message{ID: 999, Text: "hello", Chat: chat, Sender: &tele.User{ID: 102}}
	if err = s.applyAIModerationErrorFallback(ctx, msg, p, store.UserTrust{Status: "new"}, fmt.Errorf("fixture upstream")); err != nil {
		t.Fatal(err)
	}
	var action string
	if err = pool.QueryRow(ctx, `SELECT action FROM moderation_cases WHERE source_kind='ai_error_delete'`).Scan(&action); err != nil || action != "delete" {
		t.Fatalf("action=%s err=%v", action, err)
	}
}

func TestAIKickPartialNoAutomaticReplayIntegration(t *testing.T) {
	ctx := context.Background()
	pool, _, s, tr := settlementFixture(t)
	tr.mu.Lock()
	tr.failUnban = true
	tr.mu.Unlock()
	msg := &tele.Message{ID: 700, Text: "fixture", Chat: &tele.Chat{ID: -90001, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 101}}
	err := s.applyAIAction(ctx, msg, config.DefaultPolicy, store.UserTrust{Status: "new", Score: .5}, ai.CheckOutput{}, "kick", false)
	if err == nil {
		t.Fatal("expected unban failure")
	}
	var result string
	if err = pool.QueryRow(ctx, `SELECT result FROM moderation_cases WHERE source_kind='ai_event'`).Scan(&result); err != nil || result != "partial" {
		t.Fatalf("result=%s err=%v", result, err)
	}
	before := countString(tr.Methods(), "kickChatMember")
	err = s.applyAIAction(ctx, msg, config.DefaultPolicy, store.UserTrust{Status: "new", Score: .5}, ai.CheckOutput{}, "kick", false)
	if err == nil || countString(tr.Methods(), "kickChatMember") != before {
		t.Fatalf("replayed action err=%v", err)
	}
}

func TestProfileBanAndDeleteDistinctCasesIntegration(t *testing.T) {
	ctx := context.Background()
	pool, _, s, _ := settlementFixture(t)
	msg := &tele.Message{ID: 801, Text: "hello", Chat: &tele.Chat{ID: -90001, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 101}}
	if err := s.handleProfileOnMessageViolation(ctx, msg, config.DefaultPolicy, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	var ban, deleted int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE action='ban'),count(*) FILTER(WHERE action='delete') FROM moderation_cases WHERE source_kind='profile_event'`).Scan(&ban, &deleted); err != nil || ban != 1 || deleted != 1 {
		t.Fatalf("ban=%d delete=%d err=%v", ban, deleted, err)
	}
}

func TestAIMuteTelegramAppliedDBFailureIntegration(t *testing.T) {
	ctx := context.Background()
	pool, q, s, tr := settlementFixture(t)
	if _, err := pool.Exec(ctx, `INSERT INTO user_trust(chat_id,user_id,status,score) VALUES(-90001,101,'new',0.5); CREATE FUNCTION fail_activate_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='active' THEN RAISE EXCEPTION 'fixture'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_activate_fixture BEFORE UPDATE ON ai_mute_ownership FOR EACH ROW EXECUTE FUNCTION fail_activate_fixture()`); err != nil {
		t.Fatal(err)
	}
	msg := &tele.Message{ID: 802, Text: "bad", Chat: &tele.Chat{ID: -90001, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 101}}
	out := ai.CheckOutput{Verdict: ai.Verdict{Verdict: "spam", Category: "spam", Reason: "fixture", Confidence: .9}, Model: "fixture"}
	id, err := s.recordAIDecision(ctx, msg, msg.Text, out, "mute", "message", nil)
	if err != nil {
		t.Fatal(err)
	}
	trust, err := q.GetUserTrust(ctx, -90001, 101)
	if err != nil {
		t.Fatal(err)
	}
	err = s.applyAIAction(ctx, msg, config.DefaultPolicy, trust, out, "mute", false, id)
	if err == nil {
		t.Fatal("expected DB failure")
	}
	if countString(tr.Methods(), "restrictChatMember") != 1 {
		t.Fatalf("Telegram restriction not applied %v", tr.Methods())
	}
	var result string
	if err = pool.QueryRow(ctx, `SELECT result FROM moderation_cases WHERE source_kind='ai_event'`).Scan(&result); err != nil || result != "partial" {
		t.Fatalf("result=%s err=%v", result, err)
	}
	before := countString(tr.Methods(), "restrictChatMember")
	err = s.applyAIAction(ctx, msg, config.DefaultPolicy, trust, out, "mute", false, id)
	if err == nil || countString(tr.Methods(), "restrictChatMember") != before {
		t.Fatalf("mute replay err=%v", err)
	}
}

func TestNewInviteKickPartialDoesNotRepeatIntegration(t *testing.T) {
	ctx := context.Background()
	pool, _, s, tr := settlementFixture(t)
	chat := &tele.Chat{ID: -90001, Type: tele.ChatSuperGroup}
	inviter := &tele.User{ID: 101}
	target := tele.User{ID: 102}
	if _, err := pool.Exec(ctx, `INSERT INTO user_trust(chat_id,user_id,status,score) VALUES(-90001,101,'new',0.5)`); err != nil {
		t.Fatal(err)
	}
	p := config.DefaultPolicy
	p.Filter.NewUser.Enabled = true
	p.Filter.NewUser.NoInvites = true
	tr.mu.Lock()
	tr.failUnban = true
	tr.mu.Unlock()
	if !s.handleUngraduatedInvite(ctx, chat, inviter, []tele.User{target}, nil, p, "chat_member", "u999") {
		t.Fatal("partial removal must not continue normal join")
	}
	var result string
	if err := pool.QueryRow(ctx, `SELECT result FROM moderation_cases WHERE source_kind='ungraduated_invite'`).Scan(&result); err != nil || result != "partial" {
		t.Fatalf("result=%s err=%v", result, err)
	}
	before := countString(tr.Methods(), "kickChatMember")
	if !s.handleUngraduatedInvite(ctx, chat, inviter, []tele.User{target}, nil, p, "chat_member", "u999") || countString(tr.Methods(), "kickChatMember") != before {
		t.Fatal("partial removal repeated")
	}
}

func TestModerationPermissionMutationsWaitForSubjectReviewIntegration(t *testing.T) {
	_, _, s, tr := settlementFixture(t)
	ctx := context.Background()
	chat := &tele.Chat{ID: -90001, Type: tele.ChatSuperGroup}
	user := &tele.User{ID: 101}
	for _, tc := range []struct {
		name, method string
		action       func() error
	}{
		{"ban", "banChatMember", func() error { return s.banUser(chat, user) }},
		{"kick", "kickChatMember", func() error { return s.kickUser(chat, user) }},
		{"mute", "restrictChatMember", func() error { return s.muteUser(chat, user, 300) }},
		{"verification", "kickChatMember", func() error { _, err := s.applyVerificationFailAction(chat, user, "kick"); return err }},
		{"flood", "restrictChatMember", func() error { return s.temporaryRestrictJoinFloodUser(chat, user, time.Now().Add(time.Hour)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release, err := s.AcquireModerationReview(ctx, chat.ID, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			before := countString(tr.Methods(), tc.method)
			done := make(chan error, 1)
			go func() { done <- tc.action() }()
			select {
			case err := <-done:
				release()
				t.Fatalf("action bypassed gate: %v", err)
			case <-time.After(35 * time.Millisecond):
			}
			if got := countString(tr.Methods(), tc.method); got != before {
				release()
				t.Fatalf("mutation before review release: %d %d", before, got)
			}
			other, err := s.AcquireModerationReview(ctx, chat.ID, 102)
			if err != nil {
				release()
				t.Fatal(err)
			}
			other()
			release()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("operation did not resume")
			}
		})
	}
}

func TestFilterWarningCaseHasExactOwnership(t *testing.T) {
	ctx := context.Background()
	pool, _, svc, _ := settlementFixture(t)
	p := config.DefaultPolicy
	p.Warnings.Enabled = true
	p.Warnings.MaxWarns = 5
	p.Feedback.Warn.Enabled = false
	msg := &tele.Message{ID: 901, Text: "fixture", Chat: &tele.Chat{ID: -90001, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 101}}
	if err := svc.applyFilterAction(ctx, msg, p, FilterResult{Hit: true, Reason: "filter_keyword", Action: "warn"}); err != nil {
		t.Fatal(err)
	}
	var actual, owned int64
	if err := pool.QueryRow(ctx, `SELECT w.warning_id,(c.ownership->>'warning_id')::bigint FROM moderation_warning_events w JOIN moderation_cases c ON c.chat_id=w.chat_id AND c.user_id=w.user_id AND c.source_kind='filter_event'`).Scan(&actual, &owned); err != nil || actual != owned {
		t.Fatalf("warning=%d owner=%d err=%v", actual, owned, err)
	}
}

func TestCommandWarningCaseHasExactOwnership(t *testing.T) {
	ctx := context.Background()
	pool, q, svc, _ := settlementFixture(t)
	chat := &tele.Chat{ID: -90001, Type: tele.ChatSuperGroup}
	operator := &tele.User{ID: 7}
	target := &tele.User{ID: 101}
	command := &tele.Message{ID: 902, Chat: chat, Sender: operator}
	key := moderationEventKey(command, "command_warning", "warn", "warn")
	id, err := q.InsertWarningForModeration(ctx, key+":warning", store.InsertWarningParams{ChatID: chat.ID, UserID: target.ID})
	if err != nil {
		t.Fatal(err)
	}
	svc.writeCommandModerationAudit(ctx, "warn", operator, chat, target, "warn", "fixture", 0, command.ID, "success", "")
	var owned int64
	if err = pool.QueryRow(ctx, `SELECT (ownership->>'warning_id')::bigint FROM moderation_cases WHERE source_kind='command_event'`).Scan(&owned); err != nil || id != owned {
		t.Fatalf("warning=%d ownership=%d err=%v", id, owned, err)
	}
}
