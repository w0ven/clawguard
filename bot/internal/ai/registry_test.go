package ai

import (
	"context"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

type fakeProviderStore struct {
	rows []store.LlmProvider
	err  error
}

func (f fakeProviderStore) ListProviders(context.Context) ([]store.LlmProvider, error) {
	return f.rows, f.err
}

type fakeModelStore struct {
	rows []store.GetEnabledModelsWithProviderRow
	err  error
}

func (f fakeModelStore) GetEnabledModelsWithProvider(context.Context) ([]store.GetEnabledModelsWithProviderRow, error) {
	return f.rows, f.err
}

type fakeModelRegistry struct {
	items map[ModelRef]Model
}

func (f fakeModelRegistry) Get(ref ModelRef) (Model, bool) {
	item, ok := f.items[ref]
	return item, ok
}

func (f fakeModelRegistry) List(ModelFilter) []Model {
	out := make([]Model, 0, len(f.items))
	for _, item := range f.items {
		out = append(out, item)
	}
	return out
}

func (f fakeModelRegistry) Reload(context.Context) error {
	return nil
}

func TestProviderRegistryReload(t *testing.T) {
	if err := ConfigureEncryption("provider-registry-test", nil); err != nil {
		t.Fatal(err)
	}
	enc, err := EncryptAPIKey("api-key-1")
	if err != nil {
		t.Fatal(err)
	}
	registry := NewProviderRegistry(nil, fakeProviderStore{
		rows: []store.LlmProvider{
			{
				ID:           1,
				Key:          "legacy",
				Label:        "Legacy",
				Type:         "openai_compatible",
				BaseURL:      "https://example.com/v1",
				ApiKeyEnc:    enc,
				TimeoutMs:    15000,
				ExtraHeaders: []byte(`{"X-Test":"1"}`),
				Enabled:      true,
				CreatedAt:    time.Now(),
				UpdatedAt:    time.Now(),
			},
		},
	})
	if err := registry.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	provider, ok := registry.GetByKey("legacy")
	if !ok {
		t.Fatal("provider not loaded")
	}
	if provider.APIKey != "api-key-1" {
		t.Fatalf("provider.APIKey = %q, want %q", provider.APIKey, "api-key-1")
	}
	if provider.ExtraHeaders["X-Test"] != "1" {
		t.Fatalf("provider.ExtraHeaders not loaded: %#v", provider.ExtraHeaders)
	}
}

func TestResolverBuildChain(t *testing.T) {
	models := fakeModelRegistry{
		items: map[ModelRef]Model{
			NewModelRef("p1", "m1"): {ID: 1, ProviderID: 10, ProviderKey: "p1", ModelKey: "m1", Enabled: true, CapabilityTags: []string{"moderation"}},
			NewModelRef("p2", "m2"): {ID: 2, ProviderID: 20, ProviderKey: "p2", ModelKey: "m2", Enabled: true, CapabilityTags: []string{"moderation"}},
		},
	}
	resolver := NewResolver(models)

	chain, err := resolver.BuildChain(policyWithRefs(), []string{"moderation"})
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 2 || chain[0] != NewModelRef("p1", "m1") || chain[1] != NewModelRef("p2", "m2") {
		t.Fatalf("BuildChain() with refs = %#v", chain)
	}

	legacyChain, err := resolver.BuildChain(policyWithLegacyFields(), []string{"moderation"})
	if err != nil {
		t.Fatal(err)
	}
	if len(legacyChain) != 2 || legacyChain[0] != NewModelRef("p1", "m1") || legacyChain[1] != NewModelRef("p2", "m2") {
		t.Fatalf("BuildChain() with legacy fields = %#v", legacyChain)
	}

	filtered, err := resolver.BuildChain(policyWithMissingFallback(), []string{"moderation"})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0] != NewModelRef("p1", "m1") {
		t.Fatalf("BuildChain() filtered = %#v", filtered)
	}
}

func policyWithRefs() config.AIPolicy {
	return config.AIPolicy{
		PrimaryModelRef:        "p1:m1",
		FallbackModelRefs:      []string{"missing:gone", "p2:m2"},
		CapabilityRequirements: []string{"moderation"},
	}
}

func policyWithLegacyFields() config.AIPolicy {
	return config.AIPolicy{
		PrimaryProvider:        "p1",
		PrimaryModel:           "m1",
		FallbackChain:          []string{"missing/gone", "p2/m2"},
		CapabilityRequirements: []string{"moderation"},
	}
}

func policyWithMissingFallback() config.AIPolicy {
	return config.AIPolicy{
		PrimaryModelRef:        "p1:m1",
		FallbackModelRefs:      []string{"missing:gone"},
		CapabilityRequirements: []string{"moderation"},
	}
}
