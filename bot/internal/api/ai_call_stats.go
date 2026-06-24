package api

import (
	"context"
	"strings"

	"github.com/openclaw/clawguard/internal/store"
)

const historicalAICallModelLabel = "已删除/历史模型"

type aiCallPerModelResponse struct {
	Model      string `json:"model"`
	Calls      int64  `json:"calls"`
	Current    bool   `json:"current"`
	Historical bool   `json:"historical,omitempty"`
}

func currentLLMModelRefs(ctx context.Context, queries *store.Queries) (map[string]struct{}, error) {
	providers, err := queries.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	providerKeyByID := make(map[int64]string, len(providers))
	for _, provider := range providers {
		providerKeyByID[provider.ID] = provider.Key
	}

	models, err := queries.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	refs := make(map[string]struct{}, len(models))
	for _, model := range models {
		providerKey := strings.TrimSpace(providerKeyByID[model.ProviderID])
		modelKey := strings.TrimSpace(model.ModelKey)
		if providerKey == "" || modelKey == "" {
			continue
		}
		refs[providerKey+":"+modelKey] = struct{}{}
	}
	return refs, nil
}

func summarizeAICallsByCurrentModels(perModel []store.AICallPerModel, currentRefs map[string]struct{}) []aiCallPerModelResponse {
	out := make([]aiCallPerModelResponse, 0, len(perModel))
	currentIndex := make(map[string]int, len(currentRefs))
	var historicalCalls int64
	for _, item := range perModel {
		model := strings.TrimSpace(item.Model)
		currentModel := model
		if _, ok := currentRefs[currentModel]; !ok {
			currentModel = normalizeConfigModelRef(model)
		}
		if _, ok := currentRefs[currentModel]; ok {
			if index, exists := currentIndex[currentModel]; exists {
				out[index].Calls += item.Calls
				continue
			}
			currentIndex[currentModel] = len(out)
			out = append(out, aiCallPerModelResponse{
				Model:   currentModel,
				Calls:   item.Calls,
				Current: true,
			})
			continue
		}
		historicalCalls += item.Calls
	}
	if historicalCalls > 0 {
		out = append(out, aiCallPerModelResponse{
			Model:      historicalAICallModelLabel,
			Calls:      historicalCalls,
			Current:    false,
			Historical: true,
		})
	}
	return out
}
