package bot

import (
	"context"
	"errors"
	"testing"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

func TestApplyAIModerationErrorFallbackDeletesUngraduatedMessage(t *testing.T) {
	const (
		chatID = -100123
		userID = 42
	)

	ref := ai.NewModelRef("test-provider", "test-model")
	models := aiErrorFallbackModelRegistry{items: map[ai.ModelRef]ai.Model{
		ref: {
			ID:             1,
			ProviderID:     10,
			ProviderKey:    "test-provider",
			ModelKey:       "test-model",
			Enabled:        true,
			CapabilityTags: []string{"moderation"},
		},
	}}
	providers := aiErrorFallbackProviderRegistry{clients: map[string]ai.LLMClient{
		"test-provider": aiErrorFallbackClient{err: errors.New("upstream unavailable")},
	}}

	botClient, transport := newMockTelegramBot(t, "")
	db := newModerationProfileMatchMockDB(chatID, userID)
	svc := &Service{
		logger:      zap.NewNop(),
		queries:     store.New(db),
		bot:         botClient,
		sender:      botClient,
		sendLimiter: NewSendLimiter(),
		aiModerator: ai.NewModerator(context.Background(), zap.NewNop(), nil, store.New(db), providers, models, ai.NewResolver(models), nil),
	}

	policy := config.DefaultPolicy
	policy.AI.Enabled = true
	policy.AI.PrimaryModelRef = ref.String()
	policy.AI.FallbackModelRefs = nil
	policy.AI.FallbackChain = nil
	policy.AI.MaxRetries = 0
	policy.AI.BatchWindowMs = 1
	policy.AI.TimeoutMs = 1000
	policy.AI.PerUserDailyLimit = 0
	policy.AI.CheckProfileOnMessage = false
	policy.Feedback.DeleteMsg = config.ActionFeedback{
		Enabled:        true,
		Template:       "{reason}",
		ReplyToMessage: false,
	}

	msg := &tele.Message{
		ID:     1001,
		Text:   "hello from new user",
		Chat:   &tele.Chat{ID: chatID, Title: "test-group", Type: tele.ChatSuperGroup},
		Sender: &tele.User{ID: userID, Username: "new_user", FirstName: "New"},
	}

	if err := svc.applyAIModeration(context.Background(), msg, policy, false, reviewableContent{Text: msg.Text, Kind: "text"}, false); err != nil {
		t.Fatalf("applyAIModeration() error = %v", err)
	}

	methods := transport.Methods()
	if !containsString(methods, "deleteMessage") {
		t.Fatalf("telegram methods = %v, want deleteMessage", methods)
	}
	if !containsString(methods, "sendMessage") {
		t.Fatalf("telegram methods = %v, want sendMessage feedback", methods)
	}

	db.mu.Lock()
	decisions := append([]store.InsertAIDecisionParams(nil), db.aiDecisions...)
	db.mu.Unlock()
	if len(decisions) != 1 {
		t.Fatalf("ai_decisions inserts = %d, want 1", len(decisions))
	}
	if decisions[0].Verdict != "error" {
		t.Fatalf("ai_decisions verdict = %q, want error", decisions[0].Verdict)
	}
	if decisions[0].ActionTaken == "none" || decisions[0].ActionTaken == "" {
		t.Fatalf("ai_decisions action_taken = %q, want non-none error record", decisions[0].ActionTaken)
	}
}

type aiErrorFallbackProviderRegistry struct {
	clients map[string]ai.LLMClient
}

func (r aiErrorFallbackProviderRegistry) GetByKey(key string) (ai.Provider, bool) {
	_, ok := r.clients[key]
	return ai.Provider{Key: key, Enabled: ok}, ok
}

func (r aiErrorFallbackProviderRegistry) List() []ai.Provider          { return nil }
func (r aiErrorFallbackProviderRegistry) Reload(context.Context) error { return nil }

func (r aiErrorFallbackProviderRegistry) Client(key string) (ai.LLMClient, bool) {
	client, ok := r.clients[key]
	return client, ok
}

type aiErrorFallbackModelRegistry struct {
	items map[ai.ModelRef]ai.Model
}

func (r aiErrorFallbackModelRegistry) Get(ref ai.ModelRef) (ai.Model, bool) {
	model, ok := r.items[ref]
	return model, ok
}

func (r aiErrorFallbackModelRegistry) List(ai.ModelFilter) []ai.Model {
	items := make([]ai.Model, 0, len(r.items))
	for _, item := range r.items {
		items = append(items, item)
	}
	return items
}

func (r aiErrorFallbackModelRegistry) Reload(context.Context) error { return nil }

type aiErrorFallbackClient struct {
	err error
}

func (c aiErrorFallbackClient) Check(context.Context, ai.CheckRequest) (*ai.CheckResult, error) {
	return nil, c.err
}

func (c aiErrorFallbackClient) Probe(context.Context, string) (int, error) { return 0, nil }
func (c aiErrorFallbackClient) Chat(context.Context, ai.CheckRequest) (*ai.ChatRawResult, error) {
	return nil, nil
}
