package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

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
	// Source-refactor contract: an incompatible primary must not suppress a
	// compatible backup. Capability gates still apply to every real candidate.
	if !readiness.CanChat || !readiness.ToolsDeclared || readiness.ChatPrimaryModelRef != "mock:backup" {
		t.Fatalf("declared backup did not keep chat ready: %+v", readiness)
	}
	backup := registry.models[ai.NewModelRef("mock", "backup")]
	backup.SupportsTools = false
	registry.models[ai.NewModelRef("mock", "backup")] = backup
	readiness = a.ChatReadinessForPool(cfg)
	if readiness.CanChat || readiness.ToolsDeclared || len(readiness.Blockers) == 0 {
		t.Fatalf("all undeclared tools models passed: %+v", readiness)
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

func TestAssistantChatBrainPromptContracts(t *testing.T) {
	defaultSystem := assistantSystemPrompt(store.GroupAssistantPolicy{})
	for _, want := range []string{"around 10 Chinese characters", "Do not write bracketed action descriptions", "customer-service"} {
		if !strings.Contains(defaultSystem, want) {
			t.Fatalf("default system prompt missing %q", want)
		}
	}
	if strings.Contains(defaultSystem, "克制的只读群友助手") {
		t.Fatal("legacy read-only persona remains the default persona")
	}

	profileSystem := assistantSystemPromptForMode(store.GroupAssistantPolicy{MimicProfileText: "说话很利落，爱用短句"}, "direct")
	for _, want := range []string{"[ACTIVE_PERSONA]", "authoritative: yes", "完全覆盖默认人格", "说话很利落"} {
		if !strings.Contains(profileSystem, want) {
			t.Fatalf("active persona prompt missing %q", want)
		}
	}
	if strings.Contains(profileSystem, "仅作表达方式参考") {
		t.Fatal("mimic profile is still reference-only")
	}

	history := []store.GroupAssistantMessage{{
		Role: "user", SenderName: "SenderName", SenderID: 42, TelegramMessageID: 314,
		CreatedAt: time.Date(2026, 9, 8, 3, 4, 5, 0, time.UTC), Text: "上下文消息",
	}}
	sender := assistantPromptSender{
		Name: "当前发送者", ID: 43, MessageID: 315,
		MessageTime: time.Date(2026, 9, 8, 3, 5, 5, 0, time.UTC),
		IsReply:     true, IsReplyToOther: true, MessageType: "text",
	}
	contextMessages := assistantUntrustedContextWithSender(store.GroupAssistantPolicy{}, nil, history, "当前消息", sender)
	var contextText strings.Builder
	for _, message := range contextMessages {
		contextText.WriteString(fmt.Sprint(message.Content))
	}
	for _, want := range []string{"SenderName", "sender_id=42", "time=2026-09-08T03:04:05Z", "message_id=314", "[CURRENT_SENDER_TAG]"} {
		if !strings.Contains(contextText.String(), want) {
			t.Fatalf("assistant context missing %q", want)
		}
	}

	decisionContext := assistantDecisionContext(&tele.Bot{Me: &tele.User{ID: 900, Username: "assistant_test_bot"}}, sender, history, "当前消息", "合并消息上下文", 2, 5)
	for _, want := range []string{"skip", "casual"} {
		if !strings.Contains(assistantDecisionPrompt, want) {
			t.Fatalf("decision policy missing %q", want)
		}
	}
	for _, want := range []string{"[IS_REPLY_TO_OTHER]", "[SENDER_IS_OWNER]", "[IS_MERGED_MESSAGE]", "[MERGED_MESSAGE_COUNT]\n2", "[MERGED_MESSAGE_CONTEXT]\n合并消息上下文"} {
		if !strings.Contains(decisionContext, want) {
			t.Fatalf("decision context missing %q", want)
		}
	}
}

func TestAssistantHistoryReplySourceContract(t *testing.T) {
	chat := &tele.Chat{ID: -17}
	msg := &tele.Message{
		ID: 900, Chat: chat, Sender: &tele.User{ID: 88, FirstName: "Alice"},
		ReplyTo: &tele.Message{ID: 501, Chat: chat},
	}
	if got := assistantMessageReplySourceID(msg); got != "501" {
		t.Fatalf("reply source id = %q, want 501", got)
	}

	replied := store.GroupAssistantMessage{
		Role: "user", SenderName: "Alice", SenderID: 88, TelegramMessageID: 900,
		SourceID: assistantMessageReplySourceID(msg), CreatedAt: time.Unix(100, 0).UTC(), Text: "回复内容",
	}
	contextMessages := assistantUntrustedContextWithSender(store.GroupAssistantPolicy{}, nil, []store.GroupAssistantMessage{replied}, "当前消息", assistantPromptSender{})
	var contextText strings.Builder
	for _, message := range contextMessages {
		contextText.WriteString(fmt.Sprint(message.Content))
	}
	if !strings.Contains(contextText.String(), "is_reply=yes") || !strings.Contains(contextText.String(), "reply_to=501") {
		t.Fatalf("reply metadata did not reach history context: %s", contextText.String())
	}

	msg.ReplyTo = nil
	if got := assistantMessageReplySourceID(msg); got != "" {
		t.Fatalf("non-reply source id = %q, want empty", got)
	}
	nonReply := replied
	nonReply.SourceID = ""
	nonReplyLine := assistantHistoryLine(nonReply)
	if !strings.Contains(nonReplyLine, "is_reply=no") || !strings.Contains(nonReplyLine, "reply_to=none") || strings.Contains(nonReplyLine, "unknown") || strings.Contains(nonReplyLine, "reply_to=900") {
		t.Fatalf("non-reply history metadata is wrong: %s", nonReplyLine)
	}

	legacySelfID := replied
	legacySelfID.SourceID = "900"
	legacyLine := assistantHistoryLine(legacySelfID)
	if !strings.Contains(legacyLine, "is_reply=no") || !strings.Contains(legacyLine, "reply_to=none") {
		t.Fatalf("legacy self SourceID was treated as a reply: %s", legacyLine)
	}
	zeroTarget := &tele.Message{ID: 901, Chat: chat, ReplyTo: &tele.Message{ID: 0, Chat: chat}}
	if got := assistantMessageReplySourceID(zeroTarget); got != "" {
		t.Fatalf("zero reply target source id = %q, want empty", got)
	}
}

func TestAssistantOutgoingReplySourceModes(t *testing.T) {
	target := &tele.Message{ID: 987654}
	directSource := assistantOutgoingReplySourceID(&tele.SendOptions{ReplyTo: target})
	if directSource != "987654" {
		t.Fatalf("direct source id = %q, want 987654", directSource)
	}

	for _, mode := range []string{"join", "cold"} {
		sourceID := assistantOutgoingReplySourceID(&tele.SendOptions{})
		if sourceID != "" {
			t.Fatalf("%s source id = %q, want empty", mode, sourceID)
		}
		line := assistantHistoryLine(store.GroupAssistantMessage{
			Role: "assistant", TelegramMessageID: 701, SourceID: sourceID, Text: mode,
		})
		if !strings.Contains(line, "is_reply=no") || !strings.Contains(line, "reply_to=none") {
			t.Fatalf("%s history incorrectly marked as reply: %s", mode, line)
		}
	}

	directLine := assistantHistoryLine(store.GroupAssistantMessage{
		Role: "assistant", TelegramMessageID: 701, SourceID: directSource, Text: "direct",
	})
	if !strings.Contains(directLine, "is_reply=yes") || !strings.Contains(directLine, "reply_to=987654") {
		t.Fatalf("direct history lost actual ReplyTo: %s", directLine)
	}
}
