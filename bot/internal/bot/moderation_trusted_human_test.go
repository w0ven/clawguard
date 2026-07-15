package bot

import (
	"context"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

func TestResetTrustAfterViolationKeepsTrustedHumanTrusted(t *testing.T) {
	actions := []string{"delete", "delete_warn", "warn", "delete_mute", "mute"}
	for _, action := range actions {
		t.Run(action, func(t *testing.T) {
			const chatID = int64(-100123)
			const userID = int64(42)
			db := newModerationProfileMatchMockDB(chatID, userID)
			graduatedAt := db.now.Add(-24 * time.Hour)
			db.userTrust.Status = "trusted"
			db.userTrust.Score = 0.95
			db.userTrust.MessagesChecked = 12
			db.userTrust.MessagesClean = 12
			db.userTrust.GraduatedAt = &graduatedAt

			svc := &Service{logger: zap.NewNop(), queries: store.New(db)}
			svc.resetTrustAfterViolation(context.Background(), &tele.Message{
				Chat:   &tele.Chat{ID: chatID},
				Sender: &tele.User{ID: userID},
			}, action, stringPtr("test violation"))

			got := db.currentTrust()
			if got.Status != "trusted" {
				t.Fatalf("status = %q, want trusted", got.Status)
			}
			if got.Score != 0.95 || got.MessagesChecked != 12 || got.MessagesClean != 12 {
				t.Fatalf("trusted counters changed: score=%v checked=%d clean=%d", got.Score, got.MessagesChecked, got.MessagesClean)
			}
			if got.GraduatedAt == nil || !got.GraduatedAt.Equal(graduatedAt) {
				t.Fatalf("graduated_at = %v, want %v", got.GraduatedAt, graduatedAt)
			}
			if calls := db.trustSideEffectCalls(); calls != 0 {
				t.Fatalf("trust side effects = %d, want 0", calls)
			}
		})
	}
}

func TestApplyFilterActionStillDeletesAndWarnsTrustedHuman(t *testing.T) {
	const chatID = int64(-100123)
	const userID = int64(42)
	db := newModerationProfileMatchMockDB(chatID, userID)
	graduatedAt := db.now.Add(-24 * time.Hour)
	db.userTrust.Status = "trusted"
	db.userTrust.Score = 0.9
	db.userTrust.MessagesChecked = 10
	db.userTrust.MessagesClean = 10
	db.userTrust.GraduatedAt = &graduatedAt

	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient, sender: botClient}
	policy := config.DefaultPolicy
	policy.Feedback.DeleteMsg.Enabled = false
	policy.Feedback.Warn.Enabled = false
	policy.Warnings.MaxWarns = 3

	msg := &tele.Message{
		ID:     1001,
		Chat:   &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup},
		Sender: &tele.User{ID: userID, Username: "trusted_human"},
		Text:   "contains blocked keyword",
	}
	err := svc.applyFilterAction(context.Background(), msg, policy, FilterResult{
		Hit:         true,
		Reason:      "filter_keyword",
		MatchedRule: "blocked",
		Action:      "delete_warn",
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := countString(transport.Methods(), "deleteMessage"); got != 1 {
		t.Fatalf("deleteMessage calls = %d, want 1", got)
	}
	got := db.currentTrust()
	if got.Status != "trusted" || got.Score != 0.9 || got.MessagesClean != 10 {
		t.Fatalf("trusted user changed after enforced filter: %+v", got)
	}
	db.mu.Lock()
	warnings := len(db.warnings)
	violations := len(db.violations)
	db.mu.Unlock()
	if warnings != 1 || violations != 1 {
		t.Fatalf("warnings=%d violations=%d, want 1 and 1", warnings, violations)
	}
}

func TestResetTrustAfterViolationStillPenalizesUngraduatedHumansAndBots(t *testing.T) {
	tests := []struct {
		name        string
		status      string
		telegramBot bool
	}{
		{name: "ungraduated human", status: "new"},
		{name: "trusted bot", status: "trusted", telegramBot: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const chatID = int64(-100123)
			const userID = int64(42)
			db := newModerationProfileMatchMockDB(chatID, userID)
			db.userTrust.Status = tt.status
			db.userTrust.Score = 0.9
			db.userTrust.MessagesClean = 8
			db.userTrust.IsBot = tt.telegramBot

			svc := &Service{logger: zap.NewNop(), queries: store.New(db)}
			svc.resetTrustAfterViolation(context.Background(), &tele.Message{
				Chat:   &tele.Chat{ID: chatID},
				Sender: &tele.User{ID: userID, IsBot: tt.telegramBot},
			}, "delete_warn", stringPtr("test violation"))

			got := db.currentTrust()
			if got.Status != "suspicious" || got.Score > 0.3 || got.MessagesClean != 0 {
				t.Fatalf("trust after violation = %+v, want suspicious with reset clean score", got)
			}
		})
	}
}

func TestWarningThresholdBanStillBansTrustedHuman(t *testing.T) {
	const chatID = int64(-100123)
	const userID = int64(42)
	db := newModerationProfileMatchMockDB(chatID, userID)
	db.userTrust.Status = "trusted"
	db.userTrust.Score = 1

	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient, sender: botClient}
	policy := config.DefaultPolicy
	policy.Warnings.Enabled = true
	policy.Warnings.MaxWarns = 1
	policy.Warnings.ActionAtMax = "ban"
	policy.Feedback.Warn.Enabled = false
	policy.Feedback.Ban.Enabled = false

	count, escalated, err := svc.IncrWarning(
		context.Background(),
		&tele.Chat{ID: chatID, Type: tele.ChatSuperGroup},
		&tele.User{ID: userID},
		"filter_keyword",
		policy,
		false,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || !escalated {
		t.Fatalf("count=%d escalated=%t, want 1 and true", count, escalated)
	}
	if got := countString(transport.Methods(), "kickChatMember"); got != 1 {
		t.Fatalf("kickChatMember calls = %d, want 1", got)
	}
	if got := db.currentTrust(); got.Status != "banned" || got.Score != 0 {
		t.Fatalf("trust after warning ban = %+v, want banned score 0", got)
	}
}
