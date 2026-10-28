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
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

func TestGlobalExemptionBotPathsIntegration(t *testing.T) {
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
		_, _ = pool.Exec(ctx, `DELETE FROM global_exempt_users WHERE user_id=$1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM banned_users WHERE user_id=$1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM groups WHERE chat_id=$1`, chatID)
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
	if _, err = q.UpsertGlobalExemptUser(ctx, userID, "fixture", nil); err != nil {
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
	if exempt, banned := s.globalExemption(ctx, chatID, userID); exempt || !banned {
		t.Fatal("group A banned trust must take precedence")
	}
	if exempt, banned := s.globalExemption(ctx, chatID-1, userID); !exempt || banned {
		t.Fatal("group A ban record must not negate group B exemption")
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
	if _, err = q.DeleteGlobalExemptUser(ctx, userID); err != nil {
		t.Fatal(err)
	}
	result, err = s.HandleVerificationExpiry(ctx, pending)
	if err != nil || !result.RetryAt.IsZero() {
		t.Fatalf("revoked pending did not resume: %+v %v", result, err)
	}
	if countString(transport.Methods(), "kickChatMember") < beforeManual+2 {
		t.Fatalf("pending action not resumed: %v", transport.Methods())
	}
}
