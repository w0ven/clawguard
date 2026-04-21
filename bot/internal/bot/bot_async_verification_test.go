package bot

import (
	"context"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

func TestPerformAsyncVerificationMatchCASHitBansAndDeletesPrompt(t *testing.T) {
	var (
		banCalled          bool
		deletedMessageID   *int64
		deletePendingCalls int
		insertViolationHit bool
		casFeedbackHit     bool
		profileFeedbackHit bool
	)

	ops := asyncVerificationMatchOps{
		banUser: func(chat *tele.Chat, user *tele.User) error {
			banCalled = true
			return nil
		},
		awaitPendingVerification: func(ctx context.Context, chatID, userID int64, wait time.Duration) (store.PendingVerification, error) {
			messageID := int64(7788)
			return store.PendingVerification{
				ChatID:        chatID,
				UserID:        userID,
				JoinMessageID: &messageID,
			}, nil
		},
		deleteVerificationMessage: func(chat *tele.Chat, messageID *int64) {
			deletedMessageID = messageID
		},
		deletePendingVerification: func(ctx context.Context, chatID, userID int64) error {
			deletePendingCalls++
			return nil
		},
		insertViolation: func(ctx context.Context, params store.InsertViolationParams) error {
			if params.Rule != "cas_banned" {
				t.Fatalf("unexpected rule: %s", params.Rule)
			}
			if params.Action != "ban" {
				t.Fatalf("unexpected action: %s", params.Action)
			}
			insertViolationHit = true
			return nil
		},
		sendCASFeedback: func() {
			casFeedbackHit = true
		},
		sendProfileFeedback: func() {
			profileFeedbackHit = true
		},
		logger: zap.NewNop(),
	}

	match := asyncVerificationMatch{
		chat:         &tele.Chat{ID: -1001, Title: "test-group"},
		user:         &tele.User{ID: 42, Username: "new_user"},
		rule:         "cas_banned",
		matched:      stringPtr(`{"offenses":1}`),
		messageText:  stringPtr(`{"offenses":1}`),
		feedbackText: "CAS 黑名单",
		feedbackKind: "cas",
		logLabel:     "cas matched user",
	}

	if err := performAsyncVerificationMatch(context.Background(), ops, match); err != nil {
		t.Fatalf("performAsyncVerificationMatch() error = %v", err)
	}
	if !banCalled {
		t.Fatal("expected banUser to be called")
	}
	if deletedMessageID == nil || *deletedMessageID != 7788 {
		t.Fatalf("expected verification prompt deletion, got %v", deletedMessageID)
	}
	if deletePendingCalls != 1 {
		t.Fatalf("deletePendingVerification calls = %d, want 1", deletePendingCalls)
	}
	if !insertViolationHit {
		t.Fatal("expected violation insert to be called")
	}
	if !casFeedbackHit {
		t.Fatal("expected CAS feedback to be sent")
	}
	if profileFeedbackHit {
		t.Fatal("did not expect profile feedback on CAS hit")
	}
}
