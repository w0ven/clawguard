package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

type legacyMigratorStore interface {
	CountProviders(context.Context) (int64, error)
	CreateProvider(context.Context, store.CreateProviderParams) (store.LlmProvider, error)
	CreateModel(context.Context, store.CreateModelParams) (store.LlmModel, error)
}

func MigrateLegacyProviders(ctx context.Context, logger *zap.Logger, queries legacyMigratorStore, cfg config.Config) error {
	count, err := queries.CountProviders(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	if strings.TrimSpace(cfg.LLMProviders) == "" {
		return nil
	}
	providers, err := cfg.ParseLLMProviders()
	if err != nil {
		return err
	}
	migratedProviders := 0
	migratedModels := 0
	for _, provider := range providers {
		provider.Name = strings.TrimSpace(provider.Name)
		provider.BaseURL = strings.TrimSpace(provider.BaseURL)
		if provider.Name == "" || provider.BaseURL == "" {
			continue
		}
		apiKey, ok := os.LookupEnv(provider.APIKeyEnv)
		if !ok {
			apiKey = ""
		}
		apiKeyEnc, err := EncryptAPIKey(apiKey)
		if err != nil {
			return err
		}
		createdProvider, err := queries.CreateProvider(ctx, store.CreateProviderParams{
			Key:          provider.Name,
			Label:        provider.Name,
			Type:         "openai_compatible",
			BaseUrl:      provider.BaseURL,
			ApiKeyEnc:    apiKeyEnc,
			TimeoutMs:    30000,
			ExtraHeaders: []byte("{}"),
			Enabled:      true,
		})
		if err != nil {
			return err
		}
		migratedProviders++
		for _, modelKey := range provider.Models {
			modelKey = strings.TrimSpace(modelKey)
			if modelKey == "" {
				continue
			}
			meta, _ := json.Marshal(map[string]any{"legacy_source": "LLM_PROVIDERS"})
			if _, err := queries.CreateModel(ctx, store.CreateModelParams{
				ProviderID:     createdProvider.ID,
				ModelKey:       modelKey,
				Label:          modelKey,
				ApiFormat:      "openai_chat",
				Enabled:        true,
				SupportsVision: false,
				SupportsJson:   true,
				SupportsTools:  false,
				CapabilityTags: []string{"moderation"},
				Priority:       100,
				Meta:           meta,
			}); err != nil {
				return err
			}
			migratedModels++
		}
	}
	if logger != nil && migratedProviders > 0 {
		logger.Info(fmt.Sprintf("[llm] migrated %d providers, %d models from legacy env", migratedProviders, migratedModels))
	}
	return nil
}
