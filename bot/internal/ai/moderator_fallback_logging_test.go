package ai

import (
	"context"
	"errors"
	"testing"
	"time"

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
	moderator := NewModerator(context.Background(), zap.New(observedCore), nil, nil, providers, models, NewResolver(models), nil)

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

func TestEffectiveTimeout(t *testing.T) {
	tests := []struct {
		name            string
		policyMs        int
		providerTimeout time.Duration
		want            time.Duration
	}{
		{name: "provider tightens policy", policyMs: 30000, providerTimeout: time.Second, want: time.Second},
		{name: "provider unset uses policy", policyMs: 30000, providerTimeout: 0, want: 30 * time.Second},
		{name: "policy default", policyMs: 0, providerTimeout: 0, want: 10 * time.Second},
		{name: "provider cannot loosen policy", policyMs: 1000, providerTimeout: 30 * time.Second, want: time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveTimeout(tt.policyMs, tt.providerTimeout); got != tt.want {
				t.Fatalf("effectiveTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}
