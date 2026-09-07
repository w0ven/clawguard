package bot

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
)

func TestAssistantV2EnabledHardGate(t *testing.T) {
	a, cfg := assistantVerificationPool(t, func(w http.ResponseWriter, r *http.Request) {})
	readiness := a.ChatReadinessForPool(cfg)
	if !readiness.CanChat || len(readiness.Blockers) != 0 {
		t.Fatalf("tools-capable primary blocked: %+v", readiness)
	}
	registry := a.models.(*assistantVerificationRegistry)
	model := registry.models[ai.NewModelRef("mock", "main")]
	model.SupportsTools = false
	registry.models[ai.NewModelRef("mock", "main")] = model
	readiness = a.ChatReadinessForPool(cfg)
	if readiness.CanChat || readiness.ToolsDeclared || len(readiness.Blockers) == 0 {
		t.Fatalf("undeclared tools primary passed: %+v", readiness)
	}
}

func TestAssistantV2HardGateSkip(t *testing.T) {
	bot := &tele.Bot{Me: &tele.User{ID: 900, Username: "assistant_test_bot"}}
	msg := &tele.Message{ID: 17, Chat: &tele.Chat{ID: -17}, Sender: &tele.User{ID: 8}, Text: "嗯"}
	if assistantHardInterjectionGate(msg, nil, bot) {
		t.Fatal("acknowledgement passed unsolicited interjection gate")
	}
	msg.Text = "@another_user 这个问题怎么看"
	if assistantHardInterjectionGate(msg, nil, bot) {
		t.Fatal("message addressed to another user passed unsolicited interjection gate")
	}
}

func TestAssistantV2ColdPreSendCancellation(t *testing.T) {
	a := NewGroupAssistant(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if a.coldTopicPreSendValid(ctx, -17, 1, store.GroupAssistantPolicy{ChatEnabled: true, ProactiveColdTopicEnabled: true}) {
		t.Fatal("canceled cold pre-send review allowed delivery")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("test context was not canceled")
	}
}
