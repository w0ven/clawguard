package bot

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/openclaw/clawguard/internal/ai"
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
		trustUpdateCalls   int
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
		recordAIDecision: func(ctx context.Context, chat *tele.Chat, user *tele.User, matched string, mode string, aiOutput *ai.CheckOutput) error {
			t.Fatal("did not expect ai_decision recording on CAS hit")
			return nil
		},
		updateTrustBanned: func(ctx context.Context, chatID, userID int64, notes string) {
			trustUpdateCalls++
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
		decisionMode: "cas",
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
	if trustUpdateCalls != 1 {
		t.Fatalf("updateTrustBanned calls = %d, want 1", trustUpdateCalls)
	}
}

func TestPerformAsyncVerificationMatch_ProfilePath_RecordsAIDecisionAndBansTrust(t *testing.T) {
	var (
		recordCalls      int
		recordedChatID   int64
		recordedUserID   int64
		recordedMatched  string
		recordedMode     string
		recordedOutput   *ai.CheckOutput
		trustUpdateCalls int
		trustNotes       string
	)

	output := &ai.CheckOutput{
		Verdict: ai.Verdict{
			Verdict:    "ad",
			Confidence: 0.91,
			Category:   "spam",
			Reason:     "profile ad",
		},
		Model: "mock-model",
	}

	ops := asyncVerificationMatchOps{
		banUser: func(chat *tele.Chat, user *tele.User) error {
			return nil
		},
		awaitPendingVerification: func(ctx context.Context, chatID, userID int64, wait time.Duration) (store.PendingVerification, error) {
			return store.PendingVerification{}, pgx.ErrNoRows
		},
		deleteVerificationMessage: func(chat *tele.Chat, messageID *int64) {},
		deletePendingVerification: func(ctx context.Context, chatID, userID int64) error {
			return nil
		},
		insertViolation: func(ctx context.Context, params store.InsertViolationParams) error {
			return nil
		},
		sendCASFeedback:     func() {},
		sendProfileFeedback: func() {},
		recordAIDecision: func(ctx context.Context, chat *tele.Chat, user *tele.User, matched string, mode string, aiOutput *ai.CheckOutput) error {
			recordCalls++
			recordedChatID = chat.ID
			recordedUserID = user.ID
			recordedMatched = matched
			recordedMode = mode
			recordedOutput = aiOutput
			return nil
		},
		updateTrustBanned: func(ctx context.Context, chatID, userID int64, notes string) {
			trustUpdateCalls++
			trustNotes = notes
		},
		logger: zap.NewNop(),
	}

	match := asyncVerificationMatch{
		chat:         &tele.Chat{ID: -1001, Title: "test-group"},
		user:         &tele.User{ID: 42, Username: "new_user"},
		rule:         "profile_match",
		matched:      stringPtr("ad@spam"),
		messageText:  stringPtr(`{"matched":"ad@spam"}`),
		feedbackText: "个人简介违规：ad@spam",
		feedbackKind: "profile",
		logLabel:     "profile matched user",
		aiOutput:     output,
		decisionMode: "join_ai",
	}

	if err := performAsyncVerificationMatch(context.Background(), ops, match); err != nil {
		t.Fatalf("performAsyncVerificationMatch() error = %v", err)
	}
	if recordCalls != 1 {
		t.Fatalf("recordAIDecision calls = %d, want 1", recordCalls)
	}
	if recordedChatID != match.chat.ID || recordedUserID != match.user.ID {
		t.Fatalf("recordAIDecision target = (%d, %d), want (%d, %d)", recordedChatID, recordedUserID, match.chat.ID, match.user.ID)
	}
	if recordedMatched != "ad@spam" {
		t.Fatalf("recordAIDecision matched = %q, want %q", recordedMatched, "ad@spam")
	}
	if recordedMode != "join_ai" {
		t.Fatalf("recordAIDecision mode = %q, want %q", recordedMode, "join_ai")
	}
	if recordedOutput != output {
		t.Fatal("recordAIDecision output pointer mismatch")
	}
	if trustUpdateCalls != 1 {
		t.Fatalf("updateTrustBanned calls = %d, want 1", trustUpdateCalls)
	}
	if trustNotes != "profile_match: ad@spam" {
		t.Fatalf("updateTrustBanned notes = %q, want %q", trustNotes, "profile_match: ad@spam")
	}
}

func TestPerformAsyncVerificationMatch_CASPath_SkipsAIDecision(t *testing.T) {
	var (
		recordCalls      int
		trustUpdateCalls int
	)

	ops := asyncVerificationMatchOps{
		banUser: func(chat *tele.Chat, user *tele.User) error {
			return nil
		},
		awaitPendingVerification: func(ctx context.Context, chatID, userID int64, wait time.Duration) (store.PendingVerification, error) {
			return store.PendingVerification{}, pgx.ErrNoRows
		},
		deleteVerificationMessage: func(chat *tele.Chat, messageID *int64) {},
		deletePendingVerification: func(ctx context.Context, chatID, userID int64) error {
			return nil
		},
		insertViolation: func(ctx context.Context, params store.InsertViolationParams) error {
			return nil
		},
		sendCASFeedback:     func() {},
		sendProfileFeedback: func() {},
		recordAIDecision: func(ctx context.Context, chat *tele.Chat, user *tele.User, matched string, mode string, aiOutput *ai.CheckOutput) error {
			recordCalls++
			return nil
		},
		updateTrustBanned: func(ctx context.Context, chatID, userID int64, notes string) {
			trustUpdateCalls++
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
		decisionMode: "cas",
	}

	if err := performAsyncVerificationMatch(context.Background(), ops, match); err != nil {
		t.Fatalf("performAsyncVerificationMatch() error = %v", err)
	}
	if recordCalls != 0 {
		t.Fatalf("recordAIDecision calls = %d, want 0", recordCalls)
	}
	if trustUpdateCalls != 1 {
		t.Fatalf("updateTrustBanned calls = %d, want 1", trustUpdateCalls)
	}
}
