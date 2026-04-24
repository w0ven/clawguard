package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/openclaw/clawguard/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type moderatorProviderRegistry struct {
	clients map[string]LLMClient
}

func (r moderatorProviderRegistry) GetByKey(key string) (Provider, bool) {
	_, ok := r.clients[key]
	return Provider{Key: key, Enabled: ok}, ok
}

func (r moderatorProviderRegistry) List() []Provider             { return nil }
func (r moderatorProviderRegistry) Reload(context.Context) error { return nil }

func (r moderatorProviderRegistry) Client(key string) (LLMClient, bool) {
	client, ok := r.clients[key]
	return client, ok
}

type moderatorCheckClient struct {
	result *CheckResult
	err    error
}

func (c moderatorCheckClient) Check(context.Context, CheckRequest) (*CheckResult, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.result, nil
}

func (c moderatorCheckClient) Probe(context.Context, string) (int, error) { return 0, nil }
func (c moderatorCheckClient) Chat(context.Context, CheckRequest) (*ChatRawResult, error) {
	return nil, nil
}

func TestCheckSingleLogsFallbackFailureAndReturnsNextModel(t *testing.T) {
	firstRef := NewModelRef("p1", "m1")
	secondRef := NewModelRef("p2", "m2")
	models := fakeModelRegistry{items: map[ModelRef]Model{
		firstRef:  {ID: 1, ProviderID: 10, ProviderKey: "p1", ModelKey: "m1", Enabled: true, CapabilityTags: []string{"moderation"}},
		secondRef: {ID: 2, ProviderID: 20, ProviderKey: "p2", ModelKey: "m2", Enabled: true, CapabilityTags: []string{"moderation"}},
	}}
	providers := moderatorProviderRegistry{clients: map[string]LLMClient{
		"p1": moderatorCheckClient{err: errors.New("upstream unavailable")},
		"p2": moderatorCheckClient{result: &CheckResult{Verdicts: []Verdict{{Verdict: "clean", Confidence: 0.9, Category: "正常"}}, Model: "m2"}},
	}}
	observedCore, logs := observer.New(zap.WarnLevel)
	moderator := NewModerator(zap.New(observedCore), nil, nil, providers, models, NewResolver(models), nil)

	output, err := moderator.checkSingle(context.Background(), CheckInput{
		ChatID: 1,
		UserID: 2,
		Text:   "hello",
		Policy: config.AIPolicy{
			PrimaryModelRef:   firstRef.String(),
			FallbackModelRefs: []string{secondRef.String()},
			TimeoutMs:         1000,
		},
	})
	if err != nil {
		t.Fatalf("checkSingle returned error: %v", err)
	}
	if output.Model != secondRef.String() {
		t.Fatalf("output.Model = %q, want %q", output.Model, secondRef.String())
	}
	if logs.FilterMessage("ai call failed, trying next model").Len() == 0 {
		t.Fatalf("expected fallback failure warning, got logs: %#v", logs.All())
	}
}
