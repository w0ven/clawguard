package ai

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/redis/go-redis/v9"
)

func TestImageHookAfterQuotaAndSkips(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	m := NewModerator(context.Background(), nil, client, nil, nil, nil, nil, nil)
	called := 0
	input := CheckInput{ChatID: 1, UserID: 2, Text: "test", ImageBase64: "abc", SkipCache: true, Policy: config.AIPolicy{PerUserDailyLimit: 1}, BeforeModel: func(context.Context) *CheckOutput { called++; return &CheckOutput{Model: "adkiller-image-ocr"} }}
	out, err := m.CheckMessage(context.Background(), input)
	if err != nil || out.Model != "adkiller-image-ocr" || called != 1 {
		t.Fatalf("first output=%+v err=%v called=%d", out, err, called)
	}
	out, err = m.CheckMessage(context.Background(), input)
	if err != nil || !out.Skipped || called != 1 {
		t.Fatalf("quota output=%+v err=%v called=%d", out, err, called)
	}
	input.UserID = 3
	input.Text = "x"
	input.Policy.SkipMessagesShorterThan = 2
	out, err = m.CheckMessage(context.Background(), input)
	if err != nil || !out.Skipped || called != 1 {
		t.Fatalf("short output=%+v err=%v called=%d", out, err, called)
	}
}
func TestOnlyExhaustedChainIsTagged(t *testing.T) {
	ref := NewModelRef("p", "m")
	models := fakeModelRegistry{items: map[ModelRef]Model{ref: {ProviderKey: "p", ModelKey: "m", Enabled: true, CapabilityTags: []string{"moderation"}}}}
	providers := moderatorProviderRegistry{clients: map[string]LLMClient{"p": moderatorCheckClient{err: errors.New("synthetic")}}}
	m := NewModerator(context.Background(), nil, nil, nil, providers, models, NewResolver(models), nil)
	input := CheckInput{ChatID: 1, UserID: 2, Text: "synthetic", Policy: config.AIPolicy{PrimaryModelRef: ref.String(), BatchWindowMs: 1, TimeoutMs: 100}}
	_, err := m.CheckMessage(context.Background(), input)
	if !errors.Is(err, ErrModelsExhausted) {
		t.Fatalf("expected exhausted: %v", err)
	}
	for i := 0; i < aiInflightChatLimit; i++ {
		release, ok := m.TryAcquireInflight(1)
		if !ok {
			t.Fatal("cannot acquire")
		}
		defer release()
	}
	_, err = m.CheckMessage(context.Background(), input)
	if err == nil || errors.Is(err, ErrModelsExhausted) {
		t.Fatalf("inflight must not be exhaustion: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = m.CheckMessage(ctx, input)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
}

type sequenceVisionClient struct {
	mu    sync.Mutex
	calls int
}

func (c *sequenceVisionClient) Check(_ context.Context, _ CheckRequest) (*CheckResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls == 2 {
		return nil, errors.New("synthetic failure")
	}
	return &CheckResult{Verdicts: []Verdict{{Verdict: "normal", Confidence: 0.9}}}, nil
}
func (*sequenceVisionClient) Probe(context.Context, string) (int, error) { return 0, nil }
func (*sequenceVisionClient) Chat(context.Context, CheckRequest) (*ChatRawResult, error) {
	return nil, nil
}

func TestImageBatchPreservesIndependentSuccess(t *testing.T) {
	ref := NewModelRef("p", "m")
	models := fakeModelRegistry{items: map[ModelRef]Model{ref: {ProviderKey: "p", ModelKey: "m", Enabled: true, SupportsVision: true, CapabilityTags: []string{"moderation", "vision"}}}}
	c := &sequenceVisionClient{}
	m := NewModerator(context.Background(), nil, nil, nil, moderatorProviderRegistry{clients: map[string]LLMClient{"p": c}}, models, NewResolver(models), nil)
	input := CheckInput{ChatID: 1, UserID: 2, Text: "synthetic", ImageBase64: "a", Policy: config.AIPolicy{PrimaryModelRef: ref.String(), BatchWindowMs: 50, TimeoutMs: 1000}}
	type result struct {
		output CheckOutput
		err    error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func() { <-start; out, err := m.CheckMessage(context.Background(), input); results <- result{out, err} }()
	}
	close(start)
	a, b := <-results, <-results
	success, fail := 0, 0
	for _, r := range []result{a, b} {
		if r.err == nil && r.output.Model == ref.String() {
			success++
		} else if errors.Is(r.err, ErrModelsExhausted) {
			fail++
		}
	}
	if success != 1 || fail != 1 {
		t.Fatalf("results=%+v %+v", a, b)
	}
}
