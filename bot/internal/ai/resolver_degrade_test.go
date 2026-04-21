package ai

import (
	"context"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

type stubStats struct {
	rows []store.LlmModelStat
	err  error
}

func (s *stubStats) ListLLMModelStats(ctx context.Context) ([]store.LlmModelStat, error) {
	return s.rows, s.err
}

type stubModelRegistry struct {
	models map[ModelRef]Model
}

func (r *stubModelRegistry) Get(ref ModelRef) (Model, bool) {
	m, ok := r.models[ref]
	return m, ok
}

func (r *stubModelRegistry) List(filter ModelFilter) []Model {
	out := make([]Model, 0, len(r.models))
	for _, m := range r.models {
		out = append(out, m)
	}
	return out
}

func (r *stubModelRegistry) Reload(ctx context.Context) error { return nil }

func TestResolverAutoDegradeSkipsUnhealthy(t *testing.T) {
	prim := NewModelRef("p", "m1")
	fb := NewModelRef("p", "m2")

	models := &stubModelRegistry{
		models: map[ModelRef]Model{
			prim: {ID: 1, ProviderKey: "p", ModelKey: "m1", Enabled: true, CapabilityTags: []string{"moderation"}},
			fb:   {ID: 2, ProviderKey: "p", ModelKey: "m2", Enabled: true, CapabilityTags: []string{"moderation"}},
		},
	}
	stats := &stubStats{
		rows: []store.LlmModelStat{
			{ModelID: 1, Healthy: false},
			{ModelID: 2, Healthy: true},
		},
	}
	r := NewResolver(models).WithStats(stats)
	r.cacheTTL = 100 * time.Millisecond

	policy := config.AIPolicy{
		PrimaryModelRef:   "p:m1",
		FallbackModelRefs: []string{"p:m2"},
		AutoDegrade:       true,
	}
	chain, err := r.BuildChain(policy, []string{"moderation"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(chain) != 1 || chain[0] != fb {
		t.Fatalf("expected only fallback in chain, got %#v", chain)
	}
}

func TestResolverAutoDegradeAllUnhealthyFallsBack(t *testing.T) {
	prim := NewModelRef("p", "m1")
	fb := NewModelRef("p", "m2")

	models := &stubModelRegistry{
		models: map[ModelRef]Model{
			prim: {ID: 1, ProviderKey: "p", ModelKey: "m1", Enabled: true, CapabilityTags: []string{"moderation"}},
			fb:   {ID: 2, ProviderKey: "p", ModelKey: "m2", Enabled: true, CapabilityTags: []string{"moderation"}},
		},
	}
	stats := &stubStats{
		rows: []store.LlmModelStat{
			{ModelID: 1, Healthy: false},
			{ModelID: 2, Healthy: false},
		},
	}
	r := NewResolver(models).WithStats(stats)

	policy := config.AIPolicy{
		PrimaryModelRef:   "p:m1",
		FallbackModelRefs: []string{"p:m2"},
		AutoDegrade:       true,
	}
	chain, err := r.BuildChain(policy, []string{"moderation"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("expected full chain when all unhealthy, got %#v", chain)
	}
}

func TestResolverNoDegradeWhenPolicyOff(t *testing.T) {
	prim := NewModelRef("p", "m1")
	fb := NewModelRef("p", "m2")

	models := &stubModelRegistry{
		models: map[ModelRef]Model{
			prim: {ID: 1, ProviderKey: "p", ModelKey: "m1", Enabled: true, CapabilityTags: []string{"moderation"}},
			fb:   {ID: 2, ProviderKey: "p", ModelKey: "m2", Enabled: true, CapabilityTags: []string{"moderation"}},
		},
	}
	stats := &stubStats{
		rows: []store.LlmModelStat{{ModelID: 1, Healthy: false}},
	}
	r := NewResolver(models).WithStats(stats)

	policy := config.AIPolicy{
		PrimaryModelRef:   "p:m1",
		FallbackModelRefs: []string{"p:m2"},
		AutoDegrade:       false,
	}
	chain, _ := r.BuildChain(policy, []string{"moderation"})
	if len(chain) != 2 || chain[0] != prim {
		t.Fatalf("expected full chain when auto_degrade off, got %#v", chain)
	}
}
