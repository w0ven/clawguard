//go:build integration

package bot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

func TestGroupExemptionBotPathsIntegration(t *testing.T) {
	ctx := context.Background()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("TEST_DATABASE_URL required")
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	q := store.New(pool)
	const chatID int64 = -100998877
	const userID int64 = 9988776655
	// The integration database is a disposable isolated fixture.
	if _, err = pool.Exec(ctx, `INSERT INTO groups(chat_id,title,type) VALUES($1,'fixture','supergroup')`, chatID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM group_exempt_users WHERE user_id=$1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM banned_users WHERE user_id=$1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM groups WHERE chat_id=$1`, chatID)
	}()
	if _, err = pool.Exec(ctx, `INSERT INTO authorized_groups(chat_id) VALUES($1),($2) ON CONFLICT(chat_id) DO UPDATE SET enabled=true`, chatID, chatID-1); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM authorized_groups WHERE chat_id IN ($1,$2)`, chatID, chatID-1)
	}()
	b, transport := newMockTelegramBot(t, "")
	s := &Service{logger: zap.NewNop(), queries: q, bot: b, sender: b, sendLimiter: NewSendLimiter(), joinProtector: newJoinProtector(), cleanupBreaker: newTelegramCleanupBreaker()}
	s.cacheAuthorizedGroup(ctx, chatID, true)
	s.cacheSystemState(ctx, store.SystemState{})
	s.chatAdmins.Store(fmt.Sprintf("chat_admin:%d:%d", chatID, userID), chatAdminCacheEntry{IsAdmin: false, ExpireAt: time.Now().Add(time.Hour)})
	policy := keywordReplyPolicy("blocked", 0)
	policy.Filter.Keywords.Enabled = true
	policy.Filter.Keywords.List = []string{"blocked"}
	policy.Filter.Keywords.Action = "delete"
	policy.AI.Enabled = false
	s.policySnapshots.Store(chatID, policy)
	s.policySnapshotAt.Store(chatID, time.Now())
	if _, err = q.UpsertGroupExemptUser(ctx, chatID, userID, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	msg := &tele.Message{ID: 111, Text: "blocked", Chat: &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: userID, FirstName: "Fixture"}}
	for _, edited := range []bool{false, true} {
		if err = s.handleIncomingMessageWithOptions(b.NewContext(tele.Update{Message: msg}), edited); err != nil {
			t.Fatal(err)
		}
	}
	if got := countString(transport.Methods(), "sendMessage"); got != 2 {
		t.Fatalf("non-review keyword replies=%d methods=%v", got, transport.Methods())
	}
	if containsString(transport.Methods(), "deleteMessage") {
		t.Fatal("exempt message deleted")
	}
	// No sender_chat representation may match the user ID.
	channel := *msg
	channel.SenderChat = &tele.Chat{ID: -101, Type: tele.ChatChannel}
	if s.exemptMessage(ctx, &channel) {
		t.Fatal("synthetic sender matched exemption")
	}
	// An exempt join skips deleted-account filtering and all verification restrictions.
	member := &tele.User{ID: userID, FirstName: "Deleted", LastName: "Account"}
	update := &tele.ChatMemberUpdate{Chat: msg.Chat, OldChatMember: &tele.ChatMember{User: member, Role: tele.Left}, NewChatMember: &tele.ChatMember{User: member, Role: tele.Member}}
	if err = s.handleChatMemberUpdate(b.NewContext(tele.Update{ID: 551, ChatMember: update})); err != nil {
		t.Fatal(err)
	}
	if containsString(transport.Methods(), "kickChatMember") || containsString(transport.Methods(), "restrictChatMember") {
		t.Fatalf("exempt join punished: %v", transport.Methods())
	}
	pending, err := q.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: chatID, UserID: userID, Method: "button", Payload: []byte(`{}`), ExpiresAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.HandleVerificationExpiry(ctx, pending)
	if err != nil || result.RetryAt.IsZero() {
		t.Fatalf("pending not paused: %+v %v", result, err)
	}
	if err = s.handleAsyncVerificationMatch(ctx, asyncVerificationMatch{chat: msg.Chat, user: msg.Sender}); err != nil {
		t.Fatal(err)
	}
	if err = s.handleProfileOnMessageViolation(ctx, msg, policy, "x", nil); err != nil {
		t.Fatal(err)
	}
	if err = s.applyAIModerationErrorFallback(ctx, msg, policy, store.UserTrust{Status: "new"}, errors.New("fixture")); err != nil {
		t.Fatal(err)
	}
	if containsString(transport.Methods(), "kickChatMember") || containsString(transport.Methods(), "deleteMessage") {
		t.Fatalf("exempt async action: %v", transport.Methods())
	}
	// Failed automatic verification does not report a fictitious action and
	// preserves the pending record; explicit admin rejection remains effective.
	if err = s.failVerificationImmediately(ctx, msg.Chat, msg.Sender, pending, "kick", "verify_math_wrong"); !errors.Is(err, errExemptPending) {
		t.Fatalf("automatic failure: %v", err)
	}
	if err = s.completeVerification(ctx, msg.Chat, msg.Sender, pending); !errors.Is(err, errExemptPending) {
		t.Fatalf("automatic pass: %v", err)
	}
	if err = s.failVerificationImmediately(ctx, msg.Chat, msg.Sender, pending, "kick", "verify_admin_reject"); err != nil {
		t.Fatalf("manual rejection: %v", err)
	}
	pending, err = q.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: chatID, UserID: userID, Method: "button", Payload: []byte(`{}`), ExpiresAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	beforeManual := countString(transport.Methods(), "kickChatMember")
	// Explicit manual ban does not use the automatic exemption gate.
	if err = s.BanChatUser(ctx, chatID, userID); err != nil {
		t.Fatal(err)
	}
	if countString(transport.Methods(), "kickChatMember") != beforeManual+1 {
		t.Fatalf("manual ban bypassed: %v", transport.Methods())
	}
	// Existing per-group banned status takes precedence even with AI disabled.
	_, err = q.UpsertUserTrust(ctx, store.UpsertUserTrustParams{ChatID: chatID, UserID: userID, JoinedAt: time.Now(), Status: "banned", Score: 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.UpsertBannedUser(ctx, store.UpsertBannedUserParams{UserID: userID, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	if exempt, banned := s.groupExemption(ctx, chatID, userID); exempt || !banned {
		t.Fatal("group A banned trust must take precedence")
	}
	if exempt, _ := s.groupExemption(ctx, chatID-1, userID); exempt {
		t.Fatal("group A exemption leaked to group B")
	}
	if _, err = q.UpsertGroupExemptUser(ctx, chatID-1, userID, "B", nil); err != nil {
		t.Fatal(err)
	}
	if exempt, banned := s.groupExemption(ctx, chatID-1, userID); !exempt || banned {
		t.Fatal("group A ban negated group B exemption")
	}
	if _, err = q.DeleteGroupExemptUser(ctx, chatID-1, userID); err != nil {
		t.Fatal(err)
	}
	msg.ID = 112
	if err = s.handleIncomingMessageWithOptions(b.NewContext(tele.Update{Message: msg}), false); err != nil {
		t.Fatal(err)
	}
	if !containsString(transport.Methods(), "deleteMessage") {
		t.Fatalf("existing banned status ignored: %v", transport.Methods())
	}
	// Restore only test state, then revoke: pending verification resumes the ordinary fail action.
	if _, err = pool.Exec(ctx, `DELETE FROM user_trust WHERE chat_id=$1 AND user_id=$2`, chatID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err = q.DeleteGroupExemptUser(ctx, chatID, userID); err != nil {
		t.Fatal(err)
	}
	result, err = s.HandleVerificationExpiry(ctx, pending)
	if err != nil || !result.RetryAt.IsZero() {
		t.Fatalf("revoked pending did not resume: %+v %v", result, err)
	}
	if countString(transport.Methods(), "kickChatMember") < beforeManual+2 {
		t.Fatalf("pending action not resumed: %v", transport.Methods())
	}

	// Same user in another group follows that group's message, join and
	// queued-verification policies; exemption in A never leaks into B.
	groupB := chatID - 1
	if _, err = pool.Exec(ctx, `INSERT INTO groups(chat_id,title,type) VALUES($1,'fixture B','supergroup')`, groupB); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM groups WHERE chat_id=$1`, groupB) }()
	s.cacheAuthorizedGroup(ctx, groupB, true)
	s.policySnapshots.Store(groupB, policy)
	s.policySnapshotAt.Store(groupB, time.Now())
	s.chatAdmins.Store(fmt.Sprintf("chat_admin:%d:%d", groupB, userID), chatAdminCacheEntry{IsAdmin: false, ExpireAt: time.Now().Add(time.Hour)})
	if _, err = q.UpsertGroupExemptUser(ctx, chatID, userID, "A", nil); err != nil {
		t.Fatal(err)
	}
	beforeDelete := countString(transport.Methods(), "deleteMessage")
	msgB := *msg
	msgB.ID = 113
	msgB.Chat = &tele.Chat{ID: groupB, Type: tele.ChatSuperGroup}
	if err = s.handleIncomingMessageWithOptions(b.NewContext(tele.Update{Message: &msgB}), false); err != nil {
		t.Fatal(err)
	}
	if countString(transport.Methods(), "deleteMessage") <= beforeDelete {
		t.Fatal("group B message was exempted by group A")
	}
	beforeKick := countString(transport.Methods(), "kickChatMember")
	updateB := &tele.ChatMemberUpdate{Chat: msgB.Chat, OldChatMember: &tele.ChatMember{User: member, Role: tele.Left}, NewChatMember: &tele.ChatMember{User: member, Role: tele.Member}}
	if err = s.handleChatMemberUpdate(b.NewContext(tele.Update{ID: 552, ChatMember: updateB})); err != nil {
		t.Fatal(err)
	}
	if countString(transport.Methods(), "kickChatMember") <= beforeKick {
		t.Fatal("group B join was exempted by group A")
	}
	// Remove only the test's join-ban status to exercise the independent job.
	if _, err = pool.Exec(ctx, `DELETE FROM user_trust WHERE chat_id=$1 AND user_id=$2`, groupB, userID); err != nil {
		t.Fatal(err)
	}
	pendingB, err := q.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: groupB, UserID: userID, Method: "button", Payload: []byte(`{}`), ExpiresAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	result, err = s.HandleVerificationExpiry(ctx, pendingB)
	if err != nil || !result.RetryAt.IsZero() {
		t.Fatalf("group B pending incorrectly paused: %+v %v", result, err)
	}
	if exempt, _ := s.groupExemption(ctx, chatID, userID); !exempt {
		t.Fatal("group B action changed group A exemption")
	}
	// A stale exemption for a bot must never defeat OtherBotsAction.
	botID := userID + 50
	if _, err = q.UpsertGroupExemptUser(ctx, chatID, botID, "mistaken bot", nil); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM group_exempt_users WHERE chat_id=$1 AND user_id=$2`, chatID, botID)
	}()
	botPolicy := policy
	botPolicy.Filter.OtherBotsAction = "ban"
	s.policySnapshots.Store(chatID, botPolicy)
	botUser := &tele.User{ID: botID, IsBot: true, FirstName: "fixture bot"}
	beforeKick = countString(transport.Methods(), "kickChatMember")
	botUpdate := &tele.ChatMemberUpdate{Chat: msg.Chat, OldChatMember: &tele.ChatMember{User: botUser, Role: tele.Left}, NewChatMember: &tele.ChatMember{User: botUser, Role: tele.Member}}
	if err = s.handleChatMemberUpdate(b.NewContext(tele.Update{ID: 553, ChatMember: botUpdate})); err != nil {
		t.Fatal(err)
	}
	if countString(transport.Methods(), "kickChatMember") != beforeKick+1 {
		t.Fatalf("bot exemption bypassed other bot policy: %v", transport.Methods())
	}
	if active, _ := s.groupExemption(ctx, chatID, botID); active {
		t.Fatal("known bot exempted in ID-only background path")
	}
	s.policySnapshots.Store(chatID, policy)
	// Redis fallback jobs also use the task's own group, not a global ID.
	_, redisClient := newJoinProtectionTestRedis(t)
	s.redis = redisClient
	for _, id := range []int64{chatID, groupB} {
		generation := fmt.Sprintf("fixture-%d", id)
		if err = s.rememberMembershipSession(ctx, id, userID, generation); err != nil {
			t.Fatal(err)
		}
		task := joinProtectionDeferredCleanup{ChatID: id, UserID: userID, FirstName: "fixture", Action: joinProtectionActionTemporaryBan, ActionUntil: time.Now().Add(time.Hour), TemporaryBanSeconds: 3600, MembershipGeneration: generation}
		if err = enqueueJoinProtectionCleanupRedis(ctx, redisClient, task, time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	beforeKick = countString(transport.Methods(), "kickChatMember")
	if err = s.ProcessJoinProtectionCleanupFallbacks(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if got := countString(transport.Methods(), "kickChatMember") - beforeKick; got != 1 {
		t.Fatalf("redis cleanup: expected only group B action; got %d", got)
	}
	if task, ok, err := claimDueJoinProtectionCleanupRedis(ctx, redisClient, time.Now().Add(2*time.Minute), time.Minute); err != nil || !ok || task.ChatID != chatID {
		t.Fatalf("exempt group A task not deferred: %+v %v %v", task, ok, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE group_exempt_users SET expires_at=now()-interval '1 second' WHERE chat_id=$1 AND user_id=$2`, chatID, userID); err != nil {
		t.Fatal(err)
	}
	if exempt, _ := s.groupExemption(ctx, chatID, userID); exempt {
		t.Fatal("expired A exemption still active")
	}
	pending, err = q.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: chatID, UserID: userID, Method: "button", Payload: []byte(`{}`), ExpiresAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	result, err = s.HandleVerificationExpiry(ctx, pending)
	if err != nil || !result.RetryAt.IsZero() {
		t.Fatalf("expired exemption did not resume pending: %+v %v", result, err)
	}
}

func TestAIBanOwnershipFromDecisionIntegration(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	const chatID int64 = -100881111
	const userID int64 = 881111
	_, _ = pool.Exec(ctx, `DELETE FROM user_trust WHERE chat_id=$1 AND user_id=$2`, chatID, userID)
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_trust WHERE chat_id=$1 AND user_id=$2`, chatID, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM ai_decisions WHERE chat_id=$1 AND user_id=$2`, chatID, userID)
	}()
	_, err = pool.Exec(ctx, `INSERT INTO groups(chat_id,title,type) VALUES($1,'fixture','supergroup')`, chatID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM groups WHERE chat_id=$1`, chatID) }()
	_, err = pool.Exec(ctx, `INSERT INTO user_trust(chat_id,user_id,status,score) VALUES($1,$2,'new',0.5)`, chatID, userID)
	if err != nil {
		t.Fatal(err)
	}
	q := store.New(pool)
	b, _ := newMockTelegramBot(t, "")
	s := &Service{logger: zap.NewNop(), queries: q, bot: b, sender: b, sendLimiter: NewSendLimiter()}
	s.cacheSystemState(ctx, store.SystemState{})
	s.cacheAuthorizedGroup(ctx, chatID, true)
	msg := &tele.Message{ID: 901, Text: "bad", Chat: &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: userID, FirstName: "fixture"}}
	out := ai.CheckOutput{Verdict: ai.Verdict{Verdict: "spam", Category: "spam", Reason: "fixture", Confidence: 0.99}, Model: "fixture"}
	id, err := s.recordAIDecision(ctx, msg, msg.Text, out, "ban", "message", nil)
	if err != nil || id == 0 {
		t.Fatalf("decision: %d %v", id, err)
	}
	trust, err := q.GetUserTrust(ctx, chatID, userID)
	if err != nil {
		t.Fatal(err)
	}
	p := keywordReplyPolicy("none", 0)
	if err = s.applyAIAction(ctx, msg, p, trust, out, "ban", false, id); err != nil {
		t.Fatal(err)
	}
	var source string
	var got int64
	err = pool.QueryRow(ctx, `SELECT banned_reason->>'source',(banned_reason->>'decision_id')::bigint FROM user_trust WHERE chat_id=$1 AND user_id=$2 AND status='banned'`, chatID, userID).Scan(&source, &got)
	if err != nil || source != "ai" || got != id {
		t.Fatalf("AI ownership source=%s id=%d want=%d err=%v", source, got, id, err)
	}
	profileID := userID + 1
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_trust WHERE chat_id=$1 AND user_id=$2`, chatID, profileID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM ai_decisions WHERE chat_id=$1 AND user_id=$2`, chatID, profileID)
	}()
	if _, err = pool.Exec(ctx, `INSERT INTO user_trust(chat_id,user_id,status,score) VALUES($1,$2,'new',0.5) ON CONFLICT (chat_id,user_id) DO UPDATE SET status='new',score=0.5,banned_reason=null`, chatID, profileID); err != nil {
		t.Fatal(err)
	}
	msg.Sender = &tele.User{ID: profileID, FirstName: "profile"}
	msg.ID = 902
	if err = s.handleProfileOnMessageViolation(ctx, msg, p, "fixture bio", &out); err != nil {
		t.Fatal(err)
	}
	err = pool.QueryRow(ctx, `SELECT (u.banned_reason->>'decision_id')::bigint FROM user_trust u JOIN ai_decisions d ON d.id=(u.banned_reason->>'decision_id')::bigint AND d.chat_id=u.chat_id AND d.user_id=u.user_id WHERE u.chat_id=$1 AND u.user_id=$2 AND u.status='banned' AND d.scene='bio'`, chatID, profileID).Scan(&got)
	if err != nil || got == 0 {
		t.Fatalf("profile ownership not bound: %d %v", got, err)
	}
}

func TestAIMuteOwnershipFromActualActionIntegration(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	const chatID int64 = -100881112
	const userID int64 = 881112
	if _, err = pool.Exec(ctx, `INSERT INTO groups(chat_id,title,type) VALUES($1,'mute fixture','supergroup')`, chatID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, table := range []string{"ai_mute_ownership", "ai_decisions", "user_trust", "groups"} {
			_, _ = pool.Exec(context.Background(), "DELETE FROM "+table+" WHERE chat_id=$1", chatID)
		}
	}()
	if _, err = pool.Exec(ctx, `INSERT INTO user_trust(chat_id,user_id,status,score) VALUES($1,$2,'new',0.5)`, chatID, userID); err != nil {
		t.Fatal(err)
	}
	q := store.New(pool)
	b, _ := newMockTelegramBot(t, "")
	s := &Service{logger: zap.NewNop(), queries: q, bot: b, sender: b, sendLimiter: NewSendLimiter()}
	s.cacheSystemState(ctx, store.SystemState{})
	s.cacheAuthorizedGroup(ctx, chatID, true)
	msg := &tele.Message{ID: 901, Text: "bad", Chat: &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: userID, FirstName: "fixture"}}
	out := ai.CheckOutput{Verdict: ai.Verdict{Verdict: "spam", Category: "spam", Reason: "fixture", Confidence: 0.99}, Model: "fixture"}
	id, err := s.recordAIDecision(ctx, msg, msg.Text, out, "mute", "message", nil)
	if err != nil {
		t.Fatal(err)
	}
	trust, err := q.GetUserTrust(ctx, chatID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.applyAIAction(ctx, msg, keywordReplyPolicy("none", 0), trust, out, "mute", false, id); err != nil {
		t.Fatal(err)
	}
	var got, until int64
	var state string
	if err = pool.QueryRow(ctx, `SELECT decision_id,until_date,state FROM ai_mute_ownership WHERE chat_id=$1 AND user_id=$2`, chatID, userID).Scan(&got, &until, &state); err != nil || got != id || state != "active" || until <= time.Now().Unix() {
		t.Fatalf("mute ownership %d %d %s %v", got, until, state, err)
	}
	if err = s.muteUser(msg.Chat, msg.Sender, 300); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT state FROM ai_mute_ownership WHERE chat_id=$1 AND user_id=$2`, chatID, userID).Scan(&state); err != nil || state != "conflict" {
		t.Fatalf("manual mute did not invalidate: %s %v", state, err)
	}
}
