package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/openclaw/clawguard/internal/store"
)

type llmModelReferenceTarget struct {
	ProviderKey string
	ModelKey    string
	Ref         string
}

type llmModelReferenceCleanup struct {
	GlobalConfigs           int
	GroupConfigs            int
	PrimaryModelRefs        int
	FallbackModelRefs       int
	LegacyPrimaryRefs       int
	LegacyFallbackChainRefs int
	LegacyProviderModelRefs int
	AuditFailures           int
}

func newLLMModelReferenceTarget(providerKey, modelKey string) llmModelReferenceTarget {
	providerKey = strings.TrimSpace(providerKey)
	modelKey = strings.TrimSpace(modelKey)
	ref := ""
	if providerKey != "" && modelKey != "" {
		ref = providerKey + ":" + modelKey
	}
	return llmModelReferenceTarget{
		ProviderKey: providerKey,
		ModelKey:    modelKey,
		Ref:         ref,
	}
}

func (c llmModelReferenceCleanup) refsRemoved() int {
	return c.PrimaryModelRefs +
		c.FallbackModelRefs +
		c.LegacyPrimaryRefs +
		c.LegacyFallbackChainRefs +
		c.LegacyProviderModelRefs
}

func (c *llmModelReferenceCleanup) addRefs(other llmModelReferenceCleanup) {
	c.PrimaryModelRefs += other.PrimaryModelRefs
	c.FallbackModelRefs += other.FallbackModelRefs
	c.LegacyPrimaryRefs += other.LegacyPrimaryRefs
	c.LegacyFallbackChainRefs += other.LegacyFallbackChainRefs
	c.LegacyProviderModelRefs += other.LegacyProviderModelRefs
}

func (c llmModelReferenceCleanup) response() map[string]any {
	return map[string]any{
		"global_configs":             c.GlobalConfigs,
		"group_configs":              c.GroupConfigs,
		"refs_removed":               c.refsRemoved(),
		"primary_model_refs":         c.PrimaryModelRefs,
		"fallback_model_refs":        c.FallbackModelRefs,
		"legacy_primary_refs":        c.LegacyPrimaryRefs,
		"legacy_fallback_chain_refs": c.LegacyFallbackChainRefs,
		"legacy_provider_model_refs": c.LegacyProviderModelRefs,
		"audit_failures":             c.AuditFailures,
	}
}

func cleanupDeletedLLMModelReferences(ctx context.Context, queries *store.Queries, admin store.Admin, target llmModelReferenceTarget) (llmModelReferenceCleanup, error) {
	var total llmModelReferenceCleanup

	globalConfig, err := queries.GetGlobalConfig(ctx)
	switch {
	case err == nil:
		cleaned, refs, changed, err := cleanLLMModelReferencesFromConfig(globalConfig.Config, target)
		if err != nil {
			return total, fmt.Errorf("clean global config: %w", err)
		}
		if changed {
			if _, err := queries.UpsertGlobalConfig(ctx, store.UpsertGlobalConfigParams{Config: cleaned}); err != nil {
				return total, fmt.Errorf("update global config: %w", err)
			}
			total.GlobalConfigs++
			total.addRefs(refs)
			if err := writeAuditWithQueries(ctx, queries, admin, "global", nil, "delete_llm_model_cleanup_refs", globalConfig.Config, cleaned); err != nil {
				total.AuditFailures++
			}
		}
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return total, fmt.Errorf("load global config: %w", err)
	}

	groups, err := queries.ListGroups(ctx)
	if err != nil {
		return total, fmt.Errorf("list groups: %w", err)
	}
	for _, group := range groups {
		cleaned, refs, changed, err := cleanLLMModelReferencesFromConfig(group.Config, target)
		if err != nil {
			return total, fmt.Errorf("clean group %d config: %w", group.ChatID, err)
		}
		if !changed {
			continue
		}
		if _, err := queries.UpdateGroupConfig(ctx, store.UpdateGroupConfigParams{
			ChatID: group.ChatID,
			Config: cleaned,
		}); err != nil {
			return total, fmt.Errorf("update group %d config: %w", group.ChatID, err)
		}
		total.GroupConfigs++
		total.addRefs(refs)
		chatID := group.ChatID
		if err := writeAuditWithQueries(ctx, queries, admin, "group", &chatID, "delete_llm_model_cleanup_refs", group.Config, cleaned); err != nil {
			total.AuditFailures++
		}
	}

	return total, nil
}

func cleanLLMModelReferencesFromConfig(raw []byte, target llmModelReferenceTarget) ([]byte, llmModelReferenceCleanup, bool, error) {
	var cleanup llmModelReferenceCleanup
	if strings.TrimSpace(string(raw)) == "" || target.Ref == "" {
		return raw, cleanup, false, nil
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, cleanup, false, err
	}
	aiConfig, ok := doc["ai"].(map[string]any)
	if !ok {
		return raw, cleanup, false, nil
	}

	changed := false
	if value, ok := aiConfig["primary_model_ref"].(string); ok && target.matchesRef(value) {
		delete(aiConfig, "primary_model_ref")
		cleanup.PrimaryModelRefs++
		changed = true
	}

	if removed := removeMatchingModelRefs(aiConfig, "fallback_model_refs", target); removed > 0 {
		cleanup.FallbackModelRefs += removed
		changed = true
	}
	if removed := removeMatchingModelRefs(aiConfig, "fallback_chain", target); removed > 0 {
		cleanup.LegacyFallbackChainRefs += removed
		changed = true
	}

	if stringFieldEquals(aiConfig, "primary_provider", target.ProviderKey) && stringFieldEquals(aiConfig, "primary_model", target.ModelKey) {
		delete(aiConfig, "primary_provider")
		delete(aiConfig, "primary_model")
		cleanup.LegacyPrimaryRefs++
		changed = true
	}
	if stringFieldEquals(aiConfig, "provider", target.ProviderKey) && stringFieldEquals(aiConfig, "model", target.ModelKey) {
		delete(aiConfig, "provider")
		delete(aiConfig, "model")
		cleanup.LegacyProviderModelRefs++
		changed = true
	}

	if !changed {
		return raw, cleanup, false, nil
	}
	cleaned, err := json.Marshal(doc)
	if err != nil {
		return nil, cleanup, false, err
	}
	return cleaned, cleanup, true, nil
}

func removeMatchingModelRefs(config map[string]any, field string, target llmModelReferenceTarget) int {
	values, ok := config[field].([]any)
	if !ok {
		return 0
	}
	next := make([]any, 0, len(values))
	removed := 0
	for _, value := range values {
		if ref, ok := value.(string); ok && target.matchesRef(ref) {
			removed++
			continue
		}
		next = append(next, value)
	}
	if removed > 0 {
		config[field] = next
	}
	return removed
}

func (t llmModelReferenceTarget) matchesRef(raw string) bool {
	return normalizeConfigModelRef(raw) == t.Ref
}

func normalizeConfigModelRef(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if provider, model, ok := splitModelRef(raw, ":"); ok {
		return provider + ":" + model
	}
	if provider, model, ok := splitModelRef(raw, "/"); ok {
		return provider + ":" + model
	}
	return ""
}

func splitModelRef(raw, sep string) (string, string, bool) {
	parts := strings.SplitN(raw, sep, 2)
	if len(parts) != 2 {
		return "", "", false
	}
	provider := strings.TrimSpace(parts[0])
	model := strings.TrimSpace(parts[1])
	if provider == "" || model == "" {
		return "", "", false
	}
	return provider, model, true
}

func stringFieldEquals(config map[string]any, field, want string) bool {
	value, ok := config[field].(string)
	return ok && strings.TrimSpace(value) == want
}
