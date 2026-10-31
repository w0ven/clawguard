package ai

import (
	"context"
	"errors"
	"github.com/openclaw/clawguard/internal/config"
	"testing"
	"time"
)

type batchTwoClient struct{}

func (batchTwoClient) Check(ctx context.Context, _ CheckRequest) (*CheckResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &CheckResult{Verdicts: []Verdict{{Verdict: "normal", Confidence: .9}, {Verdict: "normal", Confidence: .9}}}, nil
}
func (batchTwoClient) Probe(context.Context, string) (int, error)                 { return 0, nil }
func (batchTwoClient) Chat(context.Context, CheckRequest) (*ChatRawResult, error) { return nil, nil }
func waitBatchCount(t *testing.T, m *Moderator, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		count := 0
		for _, batch := range m.batches {
			count += batch.active
		}
		m.mu.Unlock()
		if count == n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("batch active count did not reach %d", n)
}
func TestBatchFirstCallerCancellationDoesNotAbortSecond(t *testing.T) {
	ref := NewModelRef("p", "m")
	models := fakeModelRegistry{items: map[ModelRef]Model{ref: {ProviderKey: "p", ModelKey: "m", Enabled: true, CapabilityTags: []string{"moderation"}}}}
	m := NewModerator(context.Background(), nil, nil, nil, moderatorProviderRegistry{clients: map[string]LLMClient{"p": batchTwoClient{}}}, models, NewResolver(models), nil)
	input := CheckInput{ChatID: 1, UserID: 2, Text: "fixture", Policy: config.AIPolicy{PrimaryModelRef: ref.String(), BatchWindowMs: 150, TimeoutMs: 1000}}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { _, err := m.CheckMessage(ctx, input); first <- err }()
	waitBatchCount(t, m, 1)
	go func() {
		out, err := m.CheckMessage(context.Background(), input)
		if err == nil && out.Verdict.Verdict != "normal" {
			err = errors.New("incorrect verdict")
		}
		second <- err
	}()
	waitBatchCount(t, m, 2)
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("first err %v", err)
	}
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("second aborted: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second blocked")
	}
}
func TestBatchAllCancelledCleansImmediately(t *testing.T) {
	m := NewModerator(context.Background(), nil, nil, nil, nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := m.CheckMessage(ctx, CheckInput{ChatID: 1, UserID: 2, Text: "fixture", Policy: config.AIPolicy{BatchWindowMs: 10000}})
		done <- err
	}()
	waitBatchCount(t, m, 1)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	waitBatchCount(t, m, 0)
}
func TestBatchServiceShutdownUnblocksWaiters(t *testing.T) {
	life, cancel := context.WithCancel(context.Background())
	m := NewModerator(life, nil, nil, nil, nil, nil, nil, nil)
	done := make(chan error, 1)
	go func() {
		_, err := m.CheckMessage(context.Background(), CheckInput{ChatID: 1, UserID: 2, Text: "fixture", Policy: config.AIPolicy{BatchWindowMs: 10000}})
		done <- err
	}()
	waitBatchCount(t, m, 1)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown stranded waiter")
	}
}
