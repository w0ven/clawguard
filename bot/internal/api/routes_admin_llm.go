package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/store"
)

// registerAdminLLMRoutes wires provider / model / stats management endpoints
// under /api/admin/llm. All routes require owner privilege; we treat LLM
// wiring as a global, destructive surface not subject to per-group scoping.
func (s *Server) registerAdminLLMRoutes(admin *echo.Group) {
	llm := admin.Group("/llm")

	llm.GET("/providers", s.requireOwner(s.handleListLLMProviders))
	llm.POST("/providers", s.requireOwner(s.handleCreateLLMProvider))
	llm.PUT("/providers/:key", s.requireOwner(s.handleUpdateLLMProvider))
	llm.DELETE("/providers/:key", s.requireOwner(s.handleDeleteLLMProvider))
	llm.POST("/reload", s.requireOwner(s.handleReloadLLMRegistry))

	llm.GET("/models", s.requireOwner(s.handleListLLMModels))
	llm.POST("/models", s.requireOwner(s.handleCreateLLMModel))
	llm.PUT("/models/:provider/:model", s.requireOwner(s.handleUpdateLLMModel))
	llm.DELETE("/models/:provider/:model", s.requireOwner(s.handleDeleteLLMModel))

	llm.GET("/stats", s.requireOwner(s.handleListLLMStats))
	llm.POST("/models/:provider/:model/probe", s.requireOwner(s.handleProbeLLMModel))
	llm.POST("/models/:provider/:model/test", s.requireOwner(s.handleTestLLMModel))
}

// ---------------- Providers ----------------

type providerPayload struct {
	Key          string          `json:"key"`
	Label        string          `json:"label"`
	Type         string          `json:"type"`
	BaseURL      string          `json:"base_url"`
	APIKey       string          `json:"api_key"` // plaintext from admin UI; never read back
	TimeoutMs    int32           `json:"timeout_ms"`
	ExtraHeaders json.RawMessage `json:"extra_headers"`
	Enabled      *bool           `json:"enabled"`
}

func serializeProvider(p store.LlmProvider) map[string]any {
	extra := map[string]string{}
	if len(p.ExtraHeaders) > 0 {
		_ = json.Unmarshal(p.ExtraHeaders, &extra)
	}
	return map[string]any{
		"id":            p.ID,
		"key":           p.Key,
		"label":         p.Label,
		"type":          p.Type,
		"base_url":      p.BaseURL,
		"api_key_set":   strings.TrimSpace(p.ApiKeyEnc) != "",
		"api_key_hint":  hintFromCiphertext(p.ApiKeyEnc),
		"timeout_ms":    p.TimeoutMs,
		"extra_headers": extra,
		"enabled":       p.Enabled,
		"created_at":    p.CreatedAt,
		"updated_at":    p.UpdatedAt,
	}
}

// hintFromCiphertext produces a short, non-secret fingerprint so the UI can
// show that an API key is set without ever revealing it.
func hintFromCiphertext(enc string) string {
	enc = strings.TrimSpace(enc)
	if enc == "" {
		return ""
	}
	if len(enc) <= 8 {
		return "••••"
	}
	return "…" + enc[len(enc)-6:]
}

func (s *Server) handleListLLMProviders(c echo.Context) error {
	providers, err := s.botService.Queries().ListProviders(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list providers failed"})
	}
	out := make([]map[string]any, 0, len(providers))
	for _, p := range providers {
		out = append(out, serializeProvider(p))
	}
	return c.JSON(http.StatusOK, map[string]any{"providers": out})
}

func (s *Server) handleCreateLLMProvider(c echo.Context) error {
	var p providerPayload
	if err := c.Bind(&p); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json body"})
	}
	p.Key = strings.TrimSpace(p.Key)
	p.Label = strings.TrimSpace(p.Label)
	p.Type = strings.TrimSpace(p.Type)
	p.BaseURL = strings.TrimSpace(p.BaseURL)
	if p.Key == "" || p.Label == "" || p.Type == "" || p.BaseURL == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "key, label, type and base_url are required"})
	}
	if p.TimeoutMs <= 0 {
		p.TimeoutMs = 8000
	}
	apiKey := strings.TrimSpace(p.APIKey)
	if apiKey == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "api_key required"})
	}
	enc, err := ai.EncryptAPIKey(apiKey)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "encrypt api key failed"})
	}
	headersJSON := normalizeExtraHeaders(p.ExtraHeaders)
	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}

	ctx := c.Request().Context()
	if _, err := s.botService.Queries().GetProviderByKey(ctx, p.Key); err == nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": "provider key already exists"})
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "probe existing provider failed"})
	}

	created, err := s.botService.Queries().CreateProvider(ctx, store.CreateProviderParams{
		Key:          p.Key,
		Label:        p.Label,
		Type:         p.Type,
		BaseUrl:      p.BaseURL,
		ApiKeyEnc:    enc,
		TimeoutMs:    p.TimeoutMs,
		ExtraHeaders: headersJSON,
		Enabled:      enabled,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("create provider: %v", err)})
	}
	s.reloadAIRegistries(ctx)
	return c.JSON(http.StatusCreated, map[string]any{"provider": serializeProvider(created)})
}

func (s *Server) handleUpdateLLMProvider(c echo.Context) error {
	key := strings.TrimSpace(c.Param("key"))
	if key == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "provider key required"})
	}
	var p providerPayload
	if err := c.Bind(&p); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json body"})
	}
	ctx := c.Request().Context()
	existing, err := s.botService.Queries().GetProviderByKey(ctx, key)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "provider not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load provider failed"})
	}

	params := store.UpdateProviderParams{
		Key:          key,
		Label:        strings.TrimSpace(firstNonEmpty(p.Label, existing.Label)),
		Type:         strings.TrimSpace(firstNonEmpty(p.Type, existing.Type)),
		BaseUrl:      strings.TrimSpace(firstNonEmpty(p.BaseURL, existing.BaseURL)),
		ApiKeyEnc:    existing.ApiKeyEnc,
		TimeoutMs:    existing.TimeoutMs,
		ExtraHeaders: existing.ExtraHeaders,
		Enabled:      existing.Enabled,
	}
	if p.TimeoutMs > 0 {
		params.TimeoutMs = p.TimeoutMs
	}
	if p.ExtraHeaders != nil {
		params.ExtraHeaders = normalizeExtraHeaders(p.ExtraHeaders)
	}
	if p.Enabled != nil {
		params.Enabled = *p.Enabled
	}
	if strings.TrimSpace(p.APIKey) != "" {
		enc, err := ai.EncryptAPIKey(strings.TrimSpace(p.APIKey))
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "encrypt api key failed"})
		}
		params.ApiKeyEnc = enc
	}

	updated, err := s.botService.Queries().UpdateProvider(ctx, params)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("update provider: %v", err)})
	}
	s.reloadAIRegistries(ctx)
	return c.JSON(http.StatusOK, map[string]any{"provider": serializeProvider(updated)})
}

func (s *Server) handleDeleteLLMProvider(c echo.Context) error {
	key := strings.TrimSpace(c.Param("key"))
	if key == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "provider key required"})
	}
	ctx := c.Request().Context()
	// FK cascade deletes models.
	if err := s.botService.Queries().DeleteProvider(ctx, key); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("delete provider: %v", err)})
	}
	s.reloadAIRegistries(ctx)
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleReloadLLMRegistry(c echo.Context) error {
	s.reloadAIRegistries(c.Request().Context())
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// ---------------- Models ----------------

type modelPayload struct {
	ProviderKey          string          `json:"provider_key"`
	ModelKey             string          `json:"model_key"`
	Label                string          `json:"label"`
	APIFormat            string          `json:"api_format"`
	Enabled              *bool           `json:"enabled"`
	SupportsVision       *bool           `json:"supports_vision"`
	SupportsJSON         *bool           `json:"supports_json"`
	SupportsTools        *bool           `json:"supports_tools"`
	CapabilityTags       []string        `json:"capability_tags"`
	Priority             *int32          `json:"priority"`
	Meta                 json.RawMessage `json:"meta"`
	ProbeEnabled         *bool           `json:"probe_enabled"`
	ProbeIntervalSeconds *int            `json:"probe_interval_seconds"`
}

func serializeModel(m store.LlmModel, providerKey string) map[string]any {
	var meta map[string]any
	if len(m.Meta) > 0 {
		_ = json.Unmarshal(m.Meta, &meta)
	}
	return map[string]any{
		"id":                     m.ID,
		"provider_id":            m.ProviderID,
		"provider_key":           providerKey,
		"model_key":              m.ModelKey,
		"ref":                    providerKey + ":" + m.ModelKey,
		"label":                  m.Label,
		"api_format":             m.ApiFormat,
		"enabled":                m.Enabled,
		"supports_vision":        m.SupportsVision,
		"supports_json":          m.SupportsJson,
		"supports_tools":         m.SupportsTools,
		"capability_tags":        m.CapabilityTags,
		"priority":               m.Priority,
		"meta":                   meta,
		"probe_enabled":          m.ProbeEnabled,
		"probe_interval_seconds": m.ProbeIntervalSeconds,
		"created_at":             m.CreatedAt,
		"updated_at":             m.UpdatedAt,
	}
}

func (s *Server) handleListLLMModels(c echo.Context) error {
	ctx := c.Request().Context()
	providers, err := s.botService.Queries().ListProviders(ctx)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list providers failed"})
	}
	keyByID := map[int64]string{}
	for _, p := range providers {
		keyByID[p.ID] = p.Key
	}
	models, err := s.botService.Queries().ListModels(ctx)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list models failed"})
	}
	out := make([]map[string]any, 0, len(models))
	for _, m := range models {
		out = append(out, serializeModel(m, keyByID[m.ProviderID]))
	}
	return c.JSON(http.StatusOK, map[string]any{"models": out})
}

func (s *Server) handleCreateLLMModel(c echo.Context) error {
	var p modelPayload
	if err := c.Bind(&p); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json body"})
	}
	p.ProviderKey = strings.TrimSpace(p.ProviderKey)
	p.ModelKey = strings.TrimSpace(p.ModelKey)
	p.Label = strings.TrimSpace(p.Label)
	p.APIFormat = strings.TrimSpace(p.APIFormat)
	if p.ProviderKey == "" || p.ModelKey == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "provider_key and model_key required"})
	}
	if p.APIFormat == "" {
		p.APIFormat = "openai"
	}
	if p.Label == "" {
		p.Label = p.ModelKey
	}

	ctx := c.Request().Context()
	provider, err := s.botService.Queries().GetProviderByKey(ctx, p.ProviderKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "unknown provider_key"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load provider failed"})
	}

	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	supportsVision := boolPtrOr(p.SupportsVision, false)
	caps := normalizeCapabilityTags(p.CapabilityTags)
	if len(caps) == 0 {
		caps = []string{"moderation"}
	}
	caps = normalizeModelCapabilityTags(caps, supportsVision)
	priority := int32(100)
	if p.Priority != nil {
		priority = *p.Priority
	}
	meta := normalizeMetaJSON(p.Meta)
	probeEnabled := true
	if p.ProbeEnabled != nil {
		probeEnabled = *p.ProbeEnabled
	}
	probeIntervalSeconds := 0
	if p.ProbeIntervalSeconds != nil && *p.ProbeIntervalSeconds > 0 {
		probeIntervalSeconds = *p.ProbeIntervalSeconds
	}

	created, err := s.botService.Queries().CreateModel(ctx, store.CreateModelParams{
		ProviderID:           provider.ID,
		ModelKey:             p.ModelKey,
		Label:                p.Label,
		ApiFormat:            p.APIFormat,
		Enabled:              enabled,
		SupportsVision:       supportsVision,
		SupportsJson:         boolPtrOr(p.SupportsJSON, true),
		SupportsTools:        boolPtrOr(p.SupportsTools, false),
		CapabilityTags:       caps,
		Priority:             priority,
		Meta:                 meta,
		ProbeEnabled:         probeEnabled,
		ProbeIntervalSeconds: int32(probeIntervalSeconds),
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("create model: %v", err)})
	}
	s.reloadAIRegistries(ctx)
	return c.JSON(http.StatusCreated, map[string]any{"model": serializeModel(created, provider.Key)})
}

func (s *Server) handleUpdateLLMModel(c echo.Context) error {
	providerKey := strings.TrimSpace(c.Param("provider"))
	modelKey := strings.TrimSpace(c.Param("model"))
	if providerKey == "" || modelKey == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "provider and model required"})
	}
	var p modelPayload
	if err := c.Bind(&p); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json body"})
	}
	ctx := c.Request().Context()
	provider, err := s.botService.Queries().GetProviderByKey(ctx, providerKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "provider not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load provider failed"})
	}

	// Load existing so unspecified fields stay untouched.
	models, err := s.botService.Queries().ListModelsByProvider(ctx, provider.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list models failed"})
	}
	var existing store.LlmModel
	found := false
	for _, m := range models {
		if m.ModelKey == modelKey {
			existing = m
			found = true
			break
		}
	}
	if !found {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "model not found"})
	}

	params := store.UpdateModelParams{
		ProviderID:           provider.ID,
		ModelKey:             modelKey,
		Label:                firstNonEmpty(p.Label, existing.Label),
		ApiFormat:            firstNonEmpty(p.APIFormat, existing.ApiFormat),
		Enabled:              existing.Enabled,
		SupportsVision:       existing.SupportsVision,
		SupportsJson:         existing.SupportsJson,
		SupportsTools:        existing.SupportsTools,
		CapabilityTags:       existing.CapabilityTags,
		Priority:             existing.Priority,
		Meta:                 existing.Meta,
		ProbeEnabled:         existing.ProbeEnabled,
		ProbeIntervalSeconds: existing.ProbeIntervalSeconds,
	}
	if p.Enabled != nil {
		params.Enabled = *p.Enabled
	}
	if p.SupportsVision != nil {
		params.SupportsVision = *p.SupportsVision
	}
	if p.SupportsJSON != nil {
		params.SupportsJson = *p.SupportsJSON
	}
	if p.SupportsTools != nil {
		params.SupportsTools = *p.SupportsTools
	}
	if p.CapabilityTags != nil {
		params.CapabilityTags = normalizeCapabilityTags(p.CapabilityTags)
	}
	if p.Priority != nil {
		params.Priority = *p.Priority
	}
	if p.Meta != nil {
		params.Meta = normalizeMetaJSON(p.Meta)
	}
	if p.ProbeEnabled != nil {
		params.ProbeEnabled = *p.ProbeEnabled
	}
	if p.ProbeIntervalSeconds != nil {
		if *p.ProbeIntervalSeconds < 0 {
			params.ProbeIntervalSeconds = 0
		} else {
			params.ProbeIntervalSeconds = int32(*p.ProbeIntervalSeconds)
		}
	}

	params.CapabilityTags = normalizeModelCapabilityTags(params.CapabilityTags, params.SupportsVision)

	updated, err := s.botService.Queries().UpdateModel(ctx, params)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("update model: %v", err)})
	}
	s.reloadAIRegistries(ctx)
	return c.JSON(http.StatusOK, map[string]any{"model": serializeModel(updated, provider.Key)})
}

func (s *Server) handleDeleteLLMModel(c echo.Context) error {
	admin, _ := currentAdmin(c)
	providerKey := strings.TrimSpace(c.Param("provider"))
	modelKey := strings.TrimSpace(c.Param("model"))
	if providerKey == "" || modelKey == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "provider and model required"})
	}
	ctx := c.Request().Context()
	queries := s.botService.Queries()
	provider, err := queries.GetProviderByKey(ctx, providerKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "provider not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load provider failed"})
	}
	if _, err := queries.GetModelByRef(ctx, providerKey, modelKey); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "model not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load model failed"})
	}

	target := newLLMModelReferenceTarget(providerKey, modelKey)
	var cleanup llmModelReferenceCleanup
	if err := queries.Transact(ctx, func(tx *store.Queries) error {
		if err := tx.DeleteModel(ctx, provider.ID, modelKey); err != nil {
			return fmt.Errorf("delete model: %w", err)
		}
		var cleanupErr error
		cleanup, cleanupErr = cleanupDeletedLLMModelReferences(ctx, tx, admin, target)
		return cleanupErr
	}); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("delete model: %v", err)})
	}

	if cleanup.refsRemoved() > 0 {
		s.logger.Info("llm model delete cleaned config references",
			zap.String("model_ref", target.Ref),
			zap.Int("global_configs", cleanup.GlobalConfigs),
			zap.Int("group_configs", cleanup.GroupConfigs),
			zap.Int("refs_removed", cleanup.refsRemoved()),
		)
	}
	if cleanup.AuditFailures > 0 {
		s.logger.Warn("llm model delete cleanup audit failed",
			zap.String("model_ref", target.Ref),
			zap.Int("audit_failures", cleanup.AuditFailures),
		)
	}
	s.reloadAIRegistries(ctx)
	return c.JSON(http.StatusOK, map[string]any{"ok": true, "cleanup": cleanup.response()})
}

// ---------------- Stats & tests ----------------

func (s *Server) handleListLLMStats(c echo.Context) error {
	ctx := c.Request().Context()
	stats, err := s.botService.Queries().ListLLMModelStats(ctx)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list stats failed"})
	}
	out := make([]map[string]any, 0, len(stats))
	for _, st := range stats {
		entry := map[string]any{
			"model_id":       st.ModelID,
			"healthy":        st.Healthy,
			"last_check_at":  st.LastCheckAt,
			"last_ok_at":     st.LastOkAt,
			"last_error":     st.LastError,
			"latency_p50_ms": st.LatencyP50Ms,
			"latency_p95_ms": st.LatencyP95Ms,
			"success_1h":     st.Success1h,
			"fail_1h":        st.Fail1h,
			"success_24h":    st.Success24h,
			"fail_24h":       st.Fail24h,
			"updated_at":     st.UpdatedAt,
		}
		out = append(out, entry)
	}
	return c.JSON(http.StatusOK, map[string]any{"stats": out})
}

func (s *Server) handleProbeLLMModel(c echo.Context) error {
	providerKey := strings.TrimSpace(c.Param("provider"))
	modelKey := strings.TrimSpace(c.Param("model"))
	if providerKey == "" || modelKey == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "provider and model required"})
	}
	model, err := s.botService.Queries().GetModelByRef(c.Request().Context(), providerKey, modelKey)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "model not found"})
	}
	client, ok := s.botService.AIProviders().Client(providerKey)
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "provider client not loaded; reload first"})
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), 8*time.Second)
	defer cancel()
	latency, err := client.Probe(ctx, modelKey)
	checkedAt := time.Now().UTC()
	if err != nil {
		_ = s.botService.Queries().UpsertLLMProbeResult(c.Request().Context(), store.UpsertLLMProbeResultParams{
			ModelID:     model.ID,
			Healthy:     false,
			LastCheckAt: &checkedAt,
			LastOkAt:    nil,
			LastError:   err.Error(),
		})
		return c.JSON(http.StatusOK, map[string]any{
			"ok":         false,
			"latency_ms": 0,
			"error":      err.Error(),
		})
	}
	okAt := checkedAt
	_ = s.botService.Queries().UpsertLLMProbeResult(c.Request().Context(), store.UpsertLLMProbeResultParams{
		ModelID:     model.ID,
		Healthy:     true,
		LastCheckAt: &checkedAt,
		LastOkAt:    &okAt,
		LastError:   "",
	})
	return c.JSON(http.StatusOK, map[string]any{
		"ok":         true,
		"latency_ms": latency,
	})
}

type testLLMPayload struct {
	Text        string  `json:"text"`
	System      string  `json:"system"`
	MaxTokens   int     `json:"max_tokens"`
	Temperature float64 `json:"temperature"`
}

// handleTestLLMModel issues a direct chat request against a specific model
// with a minimal moderation-style prompt. Unlike /ai-test this bypasses the
// resolver/fallback chain so operators can verify one model in isolation.
func (s *Server) handleTestLLMModel(c echo.Context) error {
	providerKey := strings.TrimSpace(c.Param("provider"))
	modelKey := strings.TrimSpace(c.Param("model"))
	if providerKey == "" || modelKey == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "provider and model required"})
	}
	var p testLLMPayload
	if err := c.Bind(&p); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json body"})
	}
	p.Text = strings.TrimSpace(p.Text)
	if p.Text == "" {
		p.Text = "ping"
	}
	if p.System == "" {
		p.System = "You are a helpful assistant. Reply briefly."
	}
	if p.MaxTokens <= 0 {
		p.MaxTokens = 64
	}

	model, err := s.botService.Queries().GetModelByRef(c.Request().Context(), providerKey, modelKey)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "model not found"})
	}
	client, ok := s.botService.AIProviders().Client(providerKey)
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "provider client not loaded; reload first"})
	}

	ctx, cancel := context.WithTimeout(c.Request().Context(), 30*time.Second)
	defer cancel()
	result, err := client.Chat(ctx, ai.CheckRequest{
		Model:        modelKey,
		SystemPrompt: p.System,
		Messages: []ai.Message{
			{Role: "user", Content: p.Text},
		},
		MaxTokens:   p.MaxTokens,
		Temperature: p.Temperature,
	})
	checkedAt := time.Now().UTC()
	if err != nil {
		_ = s.botService.Queries().UpsertLLMProbeResult(c.Request().Context(), store.UpsertLLMProbeResultParams{
			ModelID:     model.ID,
			Healthy:     false,
			LastCheckAt: &checkedAt,
			LastOkAt:    nil,
			LastError:   err.Error(),
		})
		return c.JSON(http.StatusOK, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
	}
	okAt := checkedAt
	_ = s.botService.Queries().UpsertLLMProbeResult(c.Request().Context(), store.UpsertLLMProbeResultParams{
		ModelID:     model.ID,
		Healthy:     true,
		LastCheckAt: &checkedAt,
		LastOkAt:    &okAt,
		LastError:   "",
	})
	return c.JSON(http.StatusOK, map[string]any{
		"ok":                true,
		"model":             result.Model,
		"latency_ms":        result.LatencyMs,
		"prompt_tokens":     result.PromptTokens,
		"completion_tokens": result.CompletionTokens,
		"cost_cents":        result.CostCents,
		"reply":             result.Content,
	})
}

// ---------------- helpers ----------------

func (s *Server) reloadAIRegistries(ctx context.Context) {
	providers := s.botService.AIProviders()
	models := s.botService.AIModels()
	if providers != nil {
		if err := providers.Reload(ctx); err != nil {
			s.logger.Warn("reload provider registry failed", zap.Error(err))
		}
	}
	if models != nil {
		if err := models.Reload(ctx); err != nil {
			s.logger.Warn("reload model registry failed", zap.Error(err))
		}
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func boolPtrOr(p *bool, def bool) bool {
	if p != nil {
		return *p
	}
	return def
}

func normalizeExtraHeaders(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	m := map[string]string{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return []byte("{}")
	}
	out, _ := json.Marshal(m)
	return out
}

func normalizeMetaJSON(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	// Round-trip through json to reject invalid payloads.
	var anyV any
	if err := json.Unmarshal(raw, &anyV); err != nil {
		return []byte("{}")
	}
	out, _ := json.Marshal(anyV)
	return out
}

func normalizeCapabilityTags(tags []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

func normalizeModelCapabilityTags(tags []string, supportsVision bool) []string {
	out := normalizeCapabilityTags(tags)
	if supportsVision && !containsCapabilityTag(out, "vision") {
		out = append(out, "vision")
	}
	return out
}

func containsCapabilityTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}
