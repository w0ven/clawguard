package ai

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/store"
)

type Provider struct {
	ID           int64
	Key          string
	Label        string
	BaseURL      string
	APIKey       string
	Type         string
	TimeoutMs    int
	Enabled      bool
	ExtraHeaders map[string]string
}

type Model struct {
	ID                   int64
	ProviderKey          string
	ProviderID           int64
	ModelKey             string
	Label                string
	APIFormat            string
	Enabled              bool
	SupportsVision       bool
	SupportsJSON         bool
	SupportsTools        bool
	CapabilityTags       []string
	Priority             int
	Meta                 map[string]any
	ProbeEnabled         bool
	ProbeIntervalSeconds int
}

type ProviderRegistry interface {
	GetByKey(key string) (Provider, bool)
	List() []Provider
	Client(key string) (LLMClient, bool)
	Reload(ctx context.Context) error
}

type ModelRegistry interface {
	Get(ref ModelRef) (Model, bool)
	List(filter ModelFilter) []Model
	Reload(ctx context.Context) error
}

type ModelFilter struct {
	Enabled       *bool
	ProviderKey   string
	RequireCaps   []string
	RequireVision bool
	RequireJSON   bool
}

type DBProviderRegistry struct {
	logger  *zap.Logger
	queries providerStore

	mu      sync.RWMutex
	byKey   map[string]Provider
	clients map[string]LLMClient
}

type DBModelRegistry struct {
	queries modelStore

	mu    sync.RWMutex
	byRef map[ModelRef]Model
	items []Model
}

type providerStore interface {
	ListProviders(context.Context) ([]store.LlmProvider, error)
}

type modelStore interface {
	GetEnabledModelsWithProvider(context.Context) ([]store.GetEnabledModelsWithProviderRow, error)
}

func NewProviderRegistry(logger *zap.Logger, queries providerStore) *DBProviderRegistry {
	return &DBProviderRegistry{
		logger:  logger,
		queries: queries,
		byKey:   map[string]Provider{},
		clients: map[string]LLMClient{},
	}
}

func NewModelRegistry(queries modelStore) *DBModelRegistry {
	return &DBModelRegistry{
		queries: queries,
		byRef:   map[ModelRef]Model{},
	}
}

func (r *DBProviderRegistry) GetByKey(key string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, ok := r.byKey[strings.TrimSpace(key)]
	return item, ok
}

func (r *DBProviderRegistry) List() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]Provider, 0, len(r.byKey))
	for _, item := range r.byKey {
		items = append(items, item)
	}
	return items
}

func (r *DBProviderRegistry) Client(key string) (LLMClient, bool) {
	key = strings.TrimSpace(key)
	r.mu.RLock()
	provider, ok := r.byKey[key]
	if !ok || !provider.Enabled {
		r.mu.RUnlock()
		return nil, false
	}
	if client, ok := r.clients[key]; ok {
		r.mu.RUnlock()
		return client, true
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if client, ok := r.clients[key]; ok {
		return client, true
	}
	timeout := time.Duration(provider.TimeoutMs) * time.Millisecond
	client := NewOpenAICompatibleClient(provider.BaseURL, provider.APIKey, timeout, provider.ExtraHeaders)
	r.clients[key] = client
	return client, true
}

func (r *DBProviderRegistry) Reload(ctx context.Context) error {
	rows, err := r.queries.ListProviders(ctx)
	if err != nil {
		return err
	}
	next := make(map[string]Provider, len(rows))
	for _, row := range rows {
		headers := map[string]string{}
		if len(row.ExtraHeaders) > 0 {
			if err := json.Unmarshal(row.ExtraHeaders, &headers); err != nil {
				return err
			}
		}
		apiKey, err := DecryptAPIKey(row.ApiKeyEnc)
		if err != nil {
			if r.logger != nil {
				r.logger.Warn("decrypt provider api key failed", zap.String("provider_key", row.Key), zap.Error(err))
			}
			continue
		}
		next[row.Key] = Provider{
			ID:           row.ID,
			Key:          row.Key,
			Label:        row.Label,
			BaseURL:      row.BaseURL,
			APIKey:       apiKey,
			Type:         row.Type,
			TimeoutMs:    int(row.TimeoutMs),
			Enabled:      row.Enabled,
			ExtraHeaders: headers,
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byKey = next
	r.clients = map[string]LLMClient{}
	return nil
}

func (r *DBModelRegistry) Get(ref ModelRef) (Model, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, ok := r.byRef[ref]
	return item, ok
}

func (r *DBModelRegistry) List(filter ModelFilter) []Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]Model, 0, len(r.items))
	for _, item := range r.items {
		if filter.Enabled != nil && item.Enabled != *filter.Enabled {
			continue
		}
		if filter.ProviderKey != "" && item.ProviderKey != filter.ProviderKey {
			continue
		}
		if filter.RequireVision && !item.SupportsVision {
			continue
		}
		if filter.RequireJSON && !item.SupportsJSON {
			continue
		}
		if !hasCapabilities(item.CapabilityTags, filter.RequireCaps) {
			continue
		}
		items = append(items, item)
	}
	return items
}

func (r *DBModelRegistry) Reload(ctx context.Context) error {
	rows, err := r.queries.GetEnabledModelsWithProvider(ctx)
	if err != nil {
		return err
	}
	items := make([]Model, 0, len(rows))
	byRef := make(map[ModelRef]Model, len(rows))
	for _, row := range rows {
		meta := map[string]any{}
		if len(row.Meta) > 0 {
			if err := json.Unmarshal(row.Meta, &meta); err != nil {
				return err
			}
		}
		item := Model{
			ID:                   row.ID,
			ProviderKey:          row.ProviderKey,
			ProviderID:           row.ProviderID,
			ModelKey:             row.ModelKey,
			Label:                row.Label,
			APIFormat:            row.ApiFormat,
			Enabled:              row.Enabled && row.ProviderEnabled,
			SupportsVision:       row.SupportsVision,
			SupportsJSON:         row.SupportsJson,
			SupportsTools:        row.SupportsTools,
			CapabilityTags:       append([]string(nil), row.CapabilityTags...),
			Priority:             int(row.Priority),
			Meta:                 meta,
			ProbeEnabled:         row.ProbeEnabled,
			ProbeIntervalSeconds: int(row.ProbeIntervalSeconds),
		}
		ref := NewModelRef(item.ProviderKey, item.ModelKey)
		items = append(items, item)
		byRef[ref] = item
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byRef = byRef
	r.items = items
	return nil
}

func hasCapabilities(have, need []string) bool {
	if len(need) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(have))
	for _, item := range have {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		set[item] = struct{}{}
	}
	for _, item := range need {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := set[item]; !ok {
			return false
		}
	}
	return true
}
