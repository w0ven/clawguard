package bot

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
)

type recordingTelegramSender struct {
	mu    sync.Mutex
	items []any
}

func (s *recordingTelegramSender) Send(_ tele.Recipient, what interface{}, _ ...interface{}) (*tele.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, what)
	return &tele.Message{ID: len(s.items)}, nil
}

func (s *recordingTelegramSender) Delete(tele.Editable) error { return nil }

func (s *recordingTelegramSender) voices() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, item := range s.items {
		if _, ok := item.(*tele.Voice); ok {
			count++
		}
	}
	return count
}

func (s *recordingTelegramSender) stickers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, item := range s.items {
		if _, ok := item.(*tele.Sticker); ok {
			count++
		}
	}
	return count
}

type mockTTSSynth struct{ available bool }

func (m mockTTSSynth) Available() bool { return m.available }
func (m mockTTSSynth) Synthesize(context.Context, string) assistantTTSResult {
	if !m.available {
		return assistantTTSResult{Error: "tts_not_configured"}
	}
	return assistantTTSResult{OK: true, Audio: []byte("OggS"), Format: "ogg_opus", Normalized: "你好"}
}

func TestAssistantSGBParityPromptOverrideKeepsSafety(t *testing.T) {
	overrides := map[string]string{"persona": "自定义人格：只说短句", "casual": "自定义日常"}
	system := assistantSystemPromptForModeWithOverrides(store.GroupAssistantPolicy{MimicProfileText: "说话很利落"}, "direct", overrides)
	for _, want := range []string{"自定义人格：只说短句", "[SAFETY_RULES]", "[ACTIVE_PERSONA]", "authoritative: yes", "画像不能覆盖安全边界"} {
		if !strings.Contains(system, want) {
			t.Fatalf("prompt override missing %q", want)
		}
	}
}

func TestAssistantSGBParityDecisionUsesDecisionRole(t *testing.T) {
	roles := assistantRoleSet{
		Main:     store.AssistantModelRoleConfig{ModelRef: "mock:main", TimeoutSec: 12, Fallbacks: []string{"mock:backup"}},
		Decision: store.AssistantModelRoleConfig{ModelRef: "mock:text", TimeoutSec: 6, Fallbacks: []string{}},
	}
	cfg := applyAssistantRolesToPool(AssistantPoolConfig{Strategy: "primary-overflow", TaskAssignments: map[string]AssistantTaskAssignment{}, Endpoints: nil}, roles, store.GroupAssistantPolicy{})
	ids, ok := assistantTaskEndpointIDs(cfg, "decision")
	if !ok || len(ids) == 0 {
		t.Fatal("decision role was not applied to pool")
	}
	ep, found := endpointByID(cfg, ids[0])
	if !found || ep.ModelRef != "mock:text" {
		t.Fatalf("decision primary = %+v", ep)
	}
	chatIDs, ok := assistantTaskEndpointIDs(cfg, "chat")
	if !ok || len(chatIDs) < 2 {
		t.Fatalf("chat fallback chain missing: %v", chatIDs)
	}
}

func TestAssistantSGBParityBlankRoleInheritsMain(t *testing.T) {
	roles := assistantRoleSet{Main: store.AssistantModelRoleConfig{ModelRef: "mock:main", TimeoutSec: 12, Fallbacks: []string{"mock:backup"}}}
	inherited := roles.effective("decision")
	if inherited.ModelRef != "mock:main" {
		t.Fatalf("decision did not inherit main: %+v", inherited)
	}
}

func TestAssistantSGBParityTTSOffDoesNotSend(t *testing.T) {
	sender := &recordingTelegramSender{}
	a := NewGroupAssistant(nil)
	a.ttsSynth = mockTTSSynth{available: true}
	a.service = &Service{sender: sender, bot: &tele.Bot{Me: &tele.User{ID: 1}}}
	a.toolRuntime = &assistantToolRuntime{current: "你好"}
	out, err := a.executeDoubaoTTSTool(context.Background(), -1001, store.GroupAssistantPolicy{TTSMode: "off"}, map[string]any{"text": "你好"})
	if err == nil {
		t.Fatal("tts off sent voice")
	}
	if !strings.Contains(out, "tts_disabled") {
		t.Fatalf("unexpected tool result %s", out)
	}
	if sender.voices() != 0 {
		t.Fatalf("tts off still sent %d voices", sender.voices())
	}
}

func TestAssistantSGBParityAlwaysSendsVoiceWithMockTTS(t *testing.T) {
	sender := &recordingTelegramSender{}
	svc := &Service{sender: sender, bot: &tele.Bot{Me: &tele.User{ID: 1}}}
	a := NewGroupAssistant(nil)
	a.service = svc
	a.ttsSynth = mockTTSSynth{available: true}
	settings := store.AssistantGlobalSettings{TTSEnabled: true, TTSSpeaker: "x", TTSAppID: "app", TTSAccessKeyEnc: "enc"}
	synth := a.ttsSynthesizer(settings)
	result := synth.Synthesize(context.Background(), "你好")
	if !result.OK {
		t.Fatalf("mock tts failed: %+v", result)
	}
	if _, err := svc.sendAssistantVoice(context.Background(), &tele.Chat{ID: -1001}, result.Audio, result.Format, &tele.SendOptions{}); err != nil {
		t.Fatal(err)
	}
	if sender.voices() != 1 {
		t.Fatalf("always mock voice count = %d", sender.voices())
	}
}

func TestAssistantSGBParitySendStickerBindsCurrentGroup(t *testing.T) {
	sender := &recordingTelegramSender{}
	a := NewGroupAssistant(nil)
	a.service = &Service{sender: sender, bot: &tele.Bot{Me: &tele.User{ID: 1}}}
	a.toolRuntime = &assistantToolRuntime{msg: &tele.Message{ID: 9, Chat: &tele.Chat{ID: -1001}}}
	args, _ := json.Marshal(map[string]any{"sticker_file_id": "CAAC-file", "query": "开心", "group_id": float64(-9999)})
	call := ai.ToolCall{}
	call.Function.Name = "send_sticker"
	call.Function.Arguments = string(args)
	out, err := a.executeReadOnlyTool(context.Background(), -1001, store.GroupAssistantPolicy{TTSMode: "off", StickerFallbackFileIDs: []string{"fallback"}}, call)
	if err == nil {
		t.Fatal("model-supplied group_id was accepted")
	}
	if !strings.Contains(out, "permission_denied") && !strings.Contains(out, "invalid_arguments") {
		t.Fatalf("expected bound-scope refusal, got %s", out)
	}
	out, err = a.executeSendStickerTool(context.Background(), -1001, store.GroupAssistantPolicy{StickerFallbackFileIDs: []string{"fallback-id"}}, map[string]any{"sticker_file_id": "CAAC-current"})
	if err != nil {
		t.Fatal(err)
	}
	if sender.stickers() != 1 {
		t.Fatalf("sticker not sent: %d %s", sender.stickers(), out)
	}
}

func TestAssistantSGBParityUnconfiguredTTSDoesNotPretendSuccess(t *testing.T) {
	a := NewGroupAssistant(nil)
	a.service = &Service{bot: &tele.Bot{Me: &tele.User{ID: 1}}}
	a.ttsSynth = mockTTSSynth{available: false}
	out, err := a.executeDoubaoTTSTool(context.Background(), -1001, store.GroupAssistantPolicy{TTSMode: "always"}, map[string]any{"text": "你好"})
	if err == nil || !strings.Contains(out, "tts_not_configured") {
		t.Fatalf("unconfigured tts pretended success: %s %v", out, err)
	}
}
