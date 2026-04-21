package ai

import (
	"context"
	"testing"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

type fakeLegacyStore struct {
	count           int64
	createdProviders []store.CreateProviderParams
	createdModels    []store.CreateModelParams
}

func (f *fakeLegacyStore) CountProviders(context.Context) (int64, error) {
	return f.count, nil
}

func (f *fakeLegacyStore) CreateProvider(_ context.Context, arg store.CreateProviderParams) (store.LlmProvider, error) {
	f.createdProviders = append(f.createdProviders, arg)
	return store.LlmProvider{ID: int64(len(f.createdProviders)), Key: arg.Key}, nil
}

func (f *fakeLegacyStore) CreateModel(_ context.Context, arg store.CreateModelParams) (store.LlmModel, error) {
	f.createdModels = append(f.createdModels, arg)
	return store.LlmModel{ID: int64(len(f.createdModels)), ProviderID: arg.ProviderID, ModelKey: arg.ModelKey}, nil
}

func TestMigrateLegacyProviders(t *testing.T) {
	if err := ConfigureEncryption("legacy-test-key", nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LEGACY_API_KEY", "legacy-secret")

	cfg := config.Config{
		LLMProviders: `[{"name":"legacy","base_url":"https://example.com/v1","api_key_env":"LEGACY_API_KEY","models":["m1","m2"]}]`,
	}

	store := &fakeLegacyStore{}
	if err := MigrateLegacyProviders(context.Background(), nil, store, cfg); err != nil {
		t.Fatal(err)
	}
	if len(store.createdProviders) != 1 || len(store.createdModels) != 2 {
		t.Fatalf("migrated providers/models = %d/%d, want 1/2", len(store.createdProviders), len(store.createdModels))
	}
	if store.createdModels[0].SupportsJson != true {
		t.Fatalf("SupportsJson = %v, want true", store.createdModels[0].SupportsJson)
	}
	if len(store.createdModels[0].CapabilityTags) != 1 || store.createdModels[0].CapabilityTags[0] != "moderation" {
		t.Fatalf("CapabilityTags = %#v", store.createdModels[0].CapabilityTags)
	}

	skipped := &fakeLegacyStore{count: 1}
	if err := MigrateLegacyProviders(context.Background(), nil, skipped, cfg); err != nil {
		t.Fatal(err)
	}
	if len(skipped.createdProviders) != 0 || len(skipped.createdModels) != 0 {
		t.Fatalf("expected skip when table non-empty, got %d providers and %d models", len(skipped.createdProviders), len(skipped.createdModels))
	}
}
