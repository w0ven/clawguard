package bot

import (
	"context"
	"testing"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

func TestCheckProfileAIUnavailableReturnsError(t *testing.T) {
	botClient, _ := newMockTelegramBot(t, "profile bio")
	svc := &Service{logger: zap.NewNop(), bot: botClient}
	policy := config.DefaultPolicy
	policy.Verify.CheckProfile = true
	policy.Verify.ProfileCheckMode = "ai"

	_, _, err := svc.checkProfile(
		context.Background(),
		&tele.Chat{ID: -100123},
		&tele.User{ID: 42, FirstName: "New"},
		policy,
	)
	if err == nil {
		t.Fatal("checkProfile() error = nil, want fail-closed error")
	}
}

func TestCheckProfileOnMessageAIUnavailableReturnsError(t *testing.T) {
	botClient, _ := newMockTelegramBot(t, "profile bio")
	svc := &Service{logger: zap.NewNop(), bot: botClient}
	policy := config.DefaultPolicy
	policy.AI.ProfileOnMessageMode = "ai"
	policy.AI.BioCacheTTLMinutes = 0

	_, _, err := svc.checkProfileOnMessage(
		context.Background(),
		&tele.Chat{ID: -100123},
		&tele.User{ID: 42, FirstName: "New"},
		policy,
	)
	if err == nil {
		t.Fatal("checkProfileOnMessage() error = nil, want fail-closed error")
	}
}

func TestHoldProfileReviewAfterErrorMarksSuspiciousAndRestricted(t *testing.T) {
	const (
		chatID = int64(-100123)
		userID = int64(42)
	)
	botClient, transport := newMockTelegramBot(t, "profile bio")
	db := newModerationProfileMatchMockDB(chatID, userID)
	db.userTrust.MessagesClean = 4
	svc := &Service{
		logger:  zap.NewNop(),
		queries: store.New(db),
		bot:     botClient,
	}
	policy := config.DefaultPolicy
	policy.Filter.NewUser.Enabled = true
	policy.Filter.NewUser.NoMedia = true

	svc.holdProfileReviewAfterError(
		&tele.Chat{ID: chatID, Type: tele.ChatSuperGroup},
		&tele.User{ID: userID, FirstName: "New"},
		policy,
		context.DeadlineExceeded,
		"join",
	)

	trust := db.currentTrust()
	if trust.Status != "suspicious" {
		t.Fatalf("user trust status = %q, want suspicious", trust.Status)
	}
	if trust.MessagesClean != 0 {
		t.Fatalf("messages_clean = %d, want 0", trust.MessagesClean)
	}
	if trust.Score != 0.3 {
		t.Fatalf("score = %v, want 0.3", trust.Score)
	}
	if !containsString(transport.Methods(), "restrictChatMember") {
		t.Fatalf("telegram methods = %v, want restrictChatMember", transport.Methods())
	}
}
