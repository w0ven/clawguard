package api

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	assistantbot "github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/store"
)

type groupAssistantAccessError struct {
	status int
	msg    string
}

func (e *groupAssistantAccessError) Error() string { return e.msg }

func (s *Server) registerGroupAssistantRoutes(admin *echo.Group) {
	group := admin.Group("/groups/:chat_id/assistant")
	group.GET("", s.handleGetGroupAssistant)
	group.PUT("", s.handlePutGroupAssistant)
	group.GET("/status", s.handleGetGroupAssistantStatus)
	group.GET("/model-pool", s.handleGetGroupAssistantPool)
	group.PUT("/model-pool", s.handlePutGroupAssistantPool)
	group.GET("/tools", s.handleListGroupAssistantTools)
	group.GET("/recent-senders", s.handleListGroupAssistantRecentSenders)
	group.GET("/memories", s.handleListGroupAssistantMemories)
	group.POST("/memories", s.handleCreateGroupAssistantMemory)
	group.GET("/memories/:id", s.handleGetGroupAssistantMemory)
	group.PUT("/memories/:id", s.handleUpdateGroupAssistantMemory)
	group.POST("/memories/:id/forget", s.handleForgetGroupAssistantMemory)
	group.GET("/memories/:id/versions", s.handleListGroupAssistantMemoryVersions)
	group.GET("/conflicts", s.handleListGroupAssistantConflicts)
	group.POST("/conflicts/:id/resolve", s.handleResolveGroupAssistantConflict)
	group.GET("/history", s.handleListGroupAssistantHistory)
	group.GET("/dispatches", s.handleListGroupAssistantDispatches)
}

func (s *Server) assistantAccess(c echo.Context) (store.Admin, int64, error) {
	admin, _ := currentAdmin(c)
	chatID, err := parseRequiredInt64(c.Param("chat_id"))
	if err != nil {
		return admin, 0, &groupAssistantAccessError{status: http.StatusBadRequest, msg: "invalid chat_id"}
	}
	if !adminCanAccessChat(admin, chatID) {
		return admin, 0, &groupAssistantAccessError{status: http.StatusForbidden, msg: "group out of scope"}
	}
	authorized, err := s.botService.Queries().GetAuthorizedGroupByChatID(c.Request().Context(), chatID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return admin, 0, &groupAssistantAccessError{status: http.StatusNotFound, msg: "group not found"}
		}
		return admin, 0, fmt.Errorf("load authorized group: %w", err)
	}
	if !authorized.Enabled {
		return admin, 0, &groupAssistantAccessError{status: http.StatusNotFound, msg: "group not found"}
	}
	if _, err := s.botService.Queries().GetGroupByChatID(c.Request().Context(), chatID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return admin, 0, &groupAssistantAccessError{status: http.StatusNotFound, msg: "group not found"}
		}
		return admin, 0, fmt.Errorf("load group: %w", err)
	}
	return admin, chatID, nil
}

func assistantAccessResponse(c echo.Context, err error) error {
	var scoped *groupAssistantAccessError
	if errors.As(err, &scoped) {
		return c.JSON(scoped.status, map[string]string{"error": scoped.msg})
	}
	return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load assistant scope failed"})
}

const assistantbotDefaultRetentionDays int32 = 7

func defaultGroupAssistantPolicy(chatID int64) store.GroupAssistantPolicy {
	return store.GroupAssistantPolicy{ChatID: chatID, Version: 0, TriggerMode: "mention_or_reply", FollowupWindowSec: 300,
		MaxFollowupTurns: 5, Temperature: 0.3, HistoryLimit: 30, RetentionDays: 7,
		CollectionPolicy: "history_7d_and_long_term_summary", ToolAllowlist: []string{"knowledge_query", "conversation_recall", "webfetch_readonly"}, AllowDomains: []string{},
		MaxQueueDepth: 10, MaxQueueWaitSec: 15, ColdTopicIdleMinutes: 180, ColdTopicQuietStart: 0, ColdTopicQuietEnd: 8}
}

func serializeGroupAssistantPolicy(v store.GroupAssistantPolicy) map[string]any {
	return map[string]any{
		"chat_id": v.ChatID, "version": v.Version, "chat_enabled": v.ChatEnabled, "learning_enabled": v.LearningEnabled,
		"trigger_mode": v.TriggerMode, "followup_window_sec": v.FollowupWindowSec, "max_followup_turns": v.MaxFollowupTurns,
		"chat_model_ref": v.ChatModelRef, "learning_model_ref": v.LearningModelRef, "temperature": v.Temperature,
		"system_prompt": v.SystemPrompt, "history_limit": v.HistoryLimit, "retention_days": v.RetentionDays,
		"collection_policy": v.CollectionPolicy, "tool_allowlist": v.ToolAllowlist, "allow_domains": v.AllowDomains,
		"max_queue_depth": v.MaxQueueDepth, "max_queue_wait_sec": v.MaxQueueWaitSec,
		"proactive_interject_enabled": v.ProactiveInterjectEnabled, "proactive_cold_topic_enabled": v.ProactiveColdTopicEnabled,
		"cold_topic_idle_minutes": v.ColdTopicIdleMinutes, "cold_topic_quiet_start": v.ColdTopicQuietStart, "cold_topic_quiet_end": v.ColdTopicQuietEnd,
		"mimic_target_user_id": v.MimicTargetUserID, "mimic_target_user_name": v.MimicTargetUserName, "mimic_profile_text": v.MimicProfileText,
		"mimic_sample_count": v.MimicSampleCount, "mimic_distilled_at_count": v.MimicDistilledAtCount,
		"updated_by": v.UpdatedBy, "created_at": v.CreatedAt, "updated_at": v.UpdatedAt,
	}
}

func serializeGroupAssistantPool(pool store.GroupAssistantPool) map[string]any {
	var config any
	if len(pool.Config) == 0 || json.Unmarshal(pool.Config, &config) != nil {
		config = map[string]any{"task_assignments": map[string]any{}, "endpoints": []any{}}
	}
	return map[string]any{"version": pool.Version, "strategy": pool.Strategy, "config": config, "updated_at": pool.UpdatedAt}
}

func (s *Server) handleGetGroupAssistant(c echo.Context) error {
	_, chatID, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	policy, err := s.botService.Assistant().Policy(c.Request().Context(), chatID)
	if errors.Is(err, pgx.ErrNoRows) {
		policy = defaultGroupAssistantPolicy(chatID)
		err = nil
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load assistant policy failed"})
	}
	pool, poolErr := s.botService.Assistant().Pool(c.Request().Context(), chatID)
	poolResponse := map[string]any{"version": 0, "strategy": "primary-overflow", "config": map[string]any{"task_assignments": map[string]any{}, "endpoints": []any{}}}
	if poolErr == nil {
		poolResponse = serializeGroupAssistantPool(pool)
	} else if !errors.Is(poolErr, pgx.ErrNoRows) {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load assistant pool failed"})
	}
	readiness := s.botService.Assistant().ChatReadiness(c.Request().Context(), chatID)
	return c.JSON(http.StatusOK, map[string]any{"policy": serializeGroupAssistantPolicy(policy), "model_pool": poolResponse,
		"readiness": readiness,
		"defaults":  map[string]any{"disabled": true, "retention_days": 7, "history_retention": "7 days", "remote_quota": "unknown"}})
}

type groupAssistantPolicyRequest struct {
	ExpectedVersion           int64    `json:"expected_version"`
	ChatEnabled               bool     `json:"chat_enabled"`
	LearningEnabled           bool     `json:"learning_enabled"`
	TriggerMode               string   `json:"trigger_mode"`
	FollowupWindowSec         int32    `json:"followup_window_sec"`
	MaxFollowupTurns          int32    `json:"max_followup_turns"`
	ChatModelRef              string   `json:"chat_model_ref"`
	LearningModelRef          string   `json:"learning_model_ref"`
	Temperature               float64  `json:"temperature"`
	SystemPrompt              string   `json:"system_prompt"`
	HistoryLimit              int32    `json:"history_limit"`
	RetentionDays             int32    `json:"retention_days"`
	CollectionPolicy          string   `json:"collection_policy"`
	ToolAllowlist             []string `json:"tool_allowlist"`
	AllowDomains              []string `json:"allow_domains"`
	MaxQueueDepth             int32    `json:"max_queue_depth"`
	MaxQueueWaitSec           int32    `json:"max_queue_wait_sec"`
	ProactiveInterjectEnabled bool     `json:"proactive_interject_enabled"`
	ProactiveColdTopicEnabled bool     `json:"proactive_cold_topic_enabled"`
	ColdTopicIdleMinutes      *int32   `json:"cold_topic_idle_minutes"`
	ColdTopicQuietStart       *int32   `json:"cold_topic_quiet_start"`
	ColdTopicQuietEnd         *int32   `json:"cold_topic_quiet_end"`
	MimicTargetUserID         int64    `json:"mimic_target_user_id"`
	MimicTargetUserName       string   `json:"mimic_target_user_name"`
	MimicProfileText          string   `json:"mimic_profile_text"`
	MimicSampleCount          int32    `json:"mimic_sample_count"`
	MimicDistilledAtCount     int32    `json:"mimic_distilled_at_count"`
}

func validateGroupAssistantPolicyRequest(v *groupAssistantPolicyRequest) error {
	idleMinutes := int32(180)
	quietStart, quietEnd := int32(0), int32(8)
	if v.ColdTopicIdleMinutes != nil {
		idleMinutes = *v.ColdTopicIdleMinutes
	}
	if v.ColdTopicQuietStart != nil {
		quietStart = *v.ColdTopicQuietStart
	}
	if v.ColdTopicQuietEnd != nil {
		quietEnd = *v.ColdTopicQuietEnd
	}
	if idleMinutes < 180 {
		return fmt.Errorf("cold_topic_idle_minutes must be at least 180")
	}
	if quietStart < 0 || quietStart > 23 || quietEnd < 0 || quietEnd > 23 {
		return fmt.Errorf("cold_topic quiet hours out of range")
	}
	v.ColdTopicIdleMinutes = &idleMinutes
	v.ColdTopicQuietStart = &quietStart
	v.ColdTopicQuietEnd = &quietEnd
	if v.MimicTargetUserID < 0 {
		return fmt.Errorf("mimic_target_user_id cannot be negative")
	}
	if len([]rune(v.MimicTargetUserName)) > 80 || len([]rune(v.MimicProfileText)) > 1200 {
		return fmt.Errorf("mimic profile or target name too long")
	}
	if v.MimicSampleCount < 0 || v.MimicSampleCount > 1000 || v.MimicDistilledAtCount < 0 || v.MimicDistilledAtCount > 1000 {
		return fmt.Errorf("mimic counters out of range")
	}
	if v.MimicTargetUserID == 0 {
		v.MimicTargetUserName, v.MimicProfileText = "", ""
		v.MimicSampleCount, v.MimicDistilledAtCount = 0, 0
	}
	if v.TriggerMode == "" {
		v.TriggerMode = "mention_or_reply"
	}
	if v.TriggerMode != "mention_or_reply" && v.TriggerMode != "mention_only" {
		return fmt.Errorf("invalid trigger_mode")
	}
	if strings.TrimSpace(v.CollectionPolicy) == "" {
		v.CollectionPolicy = "history_7d_and_long_term_summary"
	}
	if v.CollectionPolicy != "history_7d_and_long_term_summary" {
		return fmt.Errorf("invalid collection_policy")
	}
	if v.FollowupWindowSec < 30 || v.FollowupWindowSec > 3600 {
		return fmt.Errorf("followup_window_sec out of range")
	}
	if v.MaxFollowupTurns < 1 || v.MaxFollowupTurns > 20 {
		return fmt.Errorf("max_followup_turns out of range")
	}
	if v.Temperature < 0 || v.Temperature > 2 {
		return fmt.Errorf("temperature out of range")
	}
	if len([]rune(v.SystemPrompt)) > 8000 {
		return fmt.Errorf("system_prompt too long")
	}
	if v.HistoryLimit < 1 || v.HistoryLimit > 200 {
		return fmt.Errorf("history_limit out of range")
	}
	if v.RetentionDays < 1 || v.RetentionDays > 30 {
		return fmt.Errorf("retention_days out of range")
	}
	if v.MaxQueueDepth < 0 || v.MaxQueueDepth > 100 || v.MaxQueueWaitSec < 1 || v.MaxQueueWaitSec > 60 {
		return fmt.Errorf("queue settings out of range")
	}
	known := map[string]struct{}{"knowledge_query": {}, "conversation_recall": {}, "webfetch_readonly": {}}
	if len(v.ToolAllowlist) == 0 {
		v.ToolAllowlist = []string{"knowledge_query", "conversation_recall", "webfetch_readonly"}
	}
	seen := map[string]struct{}{}
	for _, name := range v.ToolAllowlist {
		name = strings.TrimSpace(name)
		if _, ok := known[name]; !ok {
			return fmt.Errorf("unknown read-only tool")
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicate read-only tool")
		}
		seen[name] = struct{}{}
	}
	if len(v.AllowDomains) > 50 {
		return fmt.Errorf("too many allowed domains")
	}
	for _, domain := range v.AllowDomains {
		normalizedDomain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
		if normalizedDomain == "" || len(normalizedDomain) > 253 || strings.ContainsAny(normalizedDomain, "/:@") || !strings.Contains(normalizedDomain, ".") || net.ParseIP(normalizedDomain) != nil {
			return fmt.Errorf("invalid allowed domain")
		}
	}
	return nil
}

func autoAssistantEndpointID(task, modelRef string, existing map[string]struct{}) string {
	digest := sha256.Sum256([]byte(task + "\x00" + strings.TrimSpace(modelRef)))
	base := fmt.Sprintf("auto-%s-%x", task, digest[:6])
	id := base
	for i := 2; ; i++ {
		if _, exists := existing[id]; !exists {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, i)
	}
}

func buildAutoAssistantPool(current store.GroupAssistantPool, poolErr error, chatModelRef, learningModelRef string) (assistantbot.AssistantPoolConfig, int64, bool, error) {
	cfg := assistantbot.AssistantPoolConfig{Strategy: "primary-overflow", TaskAssignments: map[string]assistantbot.AssistantTaskAssignment{}, Endpoints: []assistantbot.AssistantPoolEndpoint{}}
	version := int64(0)
	poolExists := poolErr == nil
	if poolErr != nil && !errors.Is(poolErr, pgx.ErrNoRows) {
		return cfg, 0, false, poolErr
	}
	if poolExists {
		version = current.Version
		if len(current.Config) > 0 {
			if err := json.Unmarshal(current.Config, &cfg); err != nil {
				return cfg, version, true, fmt.Errorf("invalid stored assistant pool: %w", err)
			}
		}
		if cfg.Strategy == "" {
			cfg.Strategy = "primary-overflow"
		}
		if cfg.TaskAssignments == nil {
			cfg.TaskAssignments = map[string]assistantbot.AssistantTaskAssignment{}
		}
		if cfg.Endpoints == nil {
			cfg.Endpoints = []assistantbot.AssistantPoolEndpoint{}
		}
	}
	refs := []struct {
		task string
		ref  string
	}{
		{task: "chat", ref: strings.TrimSpace(chatModelRef)},
		{task: "learning", ref: strings.TrimSpace(learningModelRef)},
	}
	ids := make(map[string]struct{}, len(cfg.Endpoints))
	for _, endpoint := range cfg.Endpoints {
		ids[endpoint.ID] = struct{}{}
	}
	changed := false
	for _, item := range refs {
		if item.ref == "" {
			continue
		}
		assignment := cfg.TaskAssignments[item.task]
		if strings.TrimSpace(assignment.Primary) != "" {
			continue
		}
		id := autoAssistantEndpointID(item.task, item.ref, ids)
		ids[id] = struct{}{}
		cfg.Endpoints = append(cfg.Endpoints, assistantbot.AssistantPoolEndpoint{
			ID: id, Name: item.ref, ModelRef: item.ref, Role: "primary", Priority: 0,
			MaxConcurrency: 2, TimeoutMs: 30000, CooldownSeconds: 30, SupportsTools: item.task == "chat",
		})
		assignment.Primary = id
		cfg.TaskAssignments[item.task] = assignment
		changed = true
	}
	return cfg, version, changed, nil
}

func (s *Server) handlePutGroupAssistant(c echo.Context) error {
	admin, chatID, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	var req groupAssistantPolicyRequest
	if err := bindAssistantJSON(c, &req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid assistant policy"})
	}
	if req.ExpectedVersion < 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "expected_version cannot be negative"})
	}
	if err := validateGroupAssistantPolicyRequest(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if req.ChatModelRef != "" {
		if err := s.botService.Assistant().ValidateModelRef(req.ChatModelRef, true); err != nil {
			if req.ChatEnabled {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": "还不能启用聊天，请先选择已声明能调用技能的聊天模型。"})
			}
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "聊天模型不可用，请选择已启用且已声明能调用技能的模型"})
		}
	}
	if req.LearningModelRef != "" {
		if err := s.botService.Assistant().ValidateModelRef(req.LearningModelRef, false); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid learning_model_ref: " + err.Error()})
		}
	}
	updated, policyErr := s.botService.Assistant().Policy(c.Request().Context(), chatID)
	if policyErr != nil && !errors.Is(policyErr, pgx.ErrNoRows) {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load assistant policy failed"})
	}
	chatModelRef := strings.TrimSpace(req.ChatModelRef)
	learningModelRef := strings.TrimSpace(req.LearningModelRef)
	currentPool, poolErr := s.botService.Assistant().Pool(c.Request().Context(), chatID)
	autoCfg, poolVersion, autoPool, err := buildAutoAssistantPool(currentPool, poolErr, chatModelRef, learningModelRef)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "模型池配置无效，请重新保存模型设置"})
	}
	readiness := s.botService.Assistant().ChatReadinessForPool(autoCfg)
	if req.ChatEnabled && !readiness.CanChat {
		blocker := "还不能启用聊天，请先选择已声明能调用技能的聊天模型。"
		if len(readiness.Blockers) > 0 {
			blocker = readiness.Blockers[0]
		}
		return c.JSON(http.StatusBadRequest, map[string]string{"error": blocker})
	}
	arg := store.UpsertGroupAssistantPolicyParams{ChatID: chatID, ExpectedVersion: req.ExpectedVersion,
		ChatEnabled: req.ChatEnabled, LearningEnabled: req.LearningEnabled, TriggerMode: req.TriggerMode,
		FollowupWindowSec: req.FollowupWindowSec, MaxFollowupTurns: req.MaxFollowupTurns, ChatModelRef: chatModelRef,
		LearningModelRef: learningModelRef, Temperature: req.Temperature, SystemPrompt: strings.TrimSpace(req.SystemPrompt),
		HistoryLimit: req.HistoryLimit, RetentionDays: req.RetentionDays, CollectionPolicy: strings.TrimSpace(req.CollectionPolicy),
		ToolAllowlist: req.ToolAllowlist, AllowDomains: req.AllowDomains, MaxQueueDepth: req.MaxQueueDepth, MaxQueueWaitSec: req.MaxQueueWaitSec,
		ProactiveInterjectEnabled: req.ProactiveInterjectEnabled, ProactiveColdTopicEnabled: req.ProactiveColdTopicEnabled,
		ColdTopicIdleMinutes: *req.ColdTopicIdleMinutes, ColdTopicQuietStart: *req.ColdTopicQuietStart, ColdTopicQuietEnd: *req.ColdTopicQuietEnd,
		MimicTargetUserID: req.MimicTargetUserID, MimicTargetUserName: strings.TrimSpace(req.MimicTargetUserName), MimicProfileText: strings.TrimSpace(req.MimicProfileText),
		MimicSampleCount: req.MimicSampleCount, MimicDistilledAtCount: req.MimicDistilledAtCount, UpdatedBy: &admin.TelegramID}
	if arg.CollectionPolicy == "" {
		arg.CollectionPolicy = "history_7d_and_long_term_summary"
	}
	if req.ExpectedVersion == 0 && policyErr == nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": "assistant policy already exists; submit expected_version"})
	}
	var result store.GroupAssistantPolicy
	save := func(q *store.Queries) error {
		var saveErr error
		result, saveErr = q.UpsertGroupAssistantPolicy(c.Request().Context(), arg)
		if saveErr != nil {
			return saveErr
		}
		if autoPool {
			raw, encodeErr := jsonMarshal(autoCfg)
			if encodeErr != nil {
				return encodeErr
			}
			_, saveErr = q.UpsertGroupAssistantPool(c.Request().Context(), store.UpsertGroupAssistantPoolParams{
				ChatID: chatID, ExpectedVersion: poolVersion, Strategy: autoCfg.Strategy, Config: raw, UpdatedBy: &admin.TelegramID,
			})
		}
		return saveErr
	}
	if autoPool {
		err = s.botService.Queries().Transact(c.Request().Context(), save)
	} else {
		err = save(s.botService.Queries())
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			current := serializeGroupAssistantPolicy(updated)
			return c.JSON(http.StatusConflict, map[string]any{"error": "assistant policy version conflict", "current": current})
		}
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "save assistant policy failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{"policy": serializeGroupAssistantPolicy(result)})
}

func (s *Server) handleGetGroupAssistantPool(c echo.Context) error {
	_, chatID, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	pool, err := s.botService.Assistant().Pool(c.Request().Context(), chatID)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusOK, map[string]any{"version": 0, "strategy": "primary-overflow", "config": map[string]any{"task_assignments": map[string]any{}, "endpoints": []any{}}})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load assistant pool failed"})
	}
	return c.JSON(http.StatusOK, serializeGroupAssistantPool(pool))
}

type groupAssistantPoolRequest struct {
	ExpectedVersion int64                                           `json:"expected_version"`
	Strategy        string                                          `json:"strategy"`
	TaskAssignments map[string]assistantbot.AssistantTaskAssignment `json:"task_assignments"`
	Endpoints       []assistantbot.AssistantPoolEndpoint            `json:"endpoints"`
	MaxQueueDepth   int                                             `json:"max_queue_depth"`
	MaxQueueWaitSec int                                             `json:"max_queue_wait_sec"`
}

func (s *Server) handlePutGroupAssistantPool(c echo.Context) error {
	admin, chatID, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	var req groupAssistantPoolRequest
	if err := bindAssistantJSON(c, &req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid model pool config"})
	}
	if req.ExpectedVersion < 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "expected_version cannot be negative"})
	}
	cfg := assistantbot.AssistantPoolConfig{Strategy: req.Strategy, TaskAssignments: req.TaskAssignments, Endpoints: req.Endpoints,
		MaxQueueDepth: req.MaxQueueDepth, MaxQueueWaitSec: req.MaxQueueWaitSec}
	if err := s.botService.Assistant().ValidatePoolConfig(cfg, true); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	raw, err := jsonMarshal(cfg)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "encode model pool config failed"})
	}
	result, err := s.botService.Assistant().Pool(c.Request().Context(), chatID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load assistant pool failed"})
	}
	if req.ExpectedVersion == 0 && err == nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": "assistant pool already exists; submit expected_version"})
	}
	pool, err := s.botService.Queries().UpsertGroupAssistantPool(c.Request().Context(), store.UpsertGroupAssistantPoolParams{ChatID: chatID,
		ExpectedVersion: req.ExpectedVersion, Strategy: cfg.Strategy, Config: raw, UpdatedBy: &admin.TelegramID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusConflict, map[string]any{"error": "assistant pool version conflict", "current": result.Version})
		}
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "save assistant pool failed"})
	}
	return c.JSON(http.StatusOK, serializeGroupAssistantPool(pool))
}

func (s *Server) handleGetGroupAssistantStatus(c echo.Context) error {
	_, chatID, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	status, err := s.botService.Assistant().Status(c.Request().Context(), chatID)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusOK, map[string]any{"chat_id": chatID, "active_strategy": "primary-overflow", "endpoints_status": []any{}, "queue_depth": 0,
			"remote_quota_note": "未知（未观测）", "can_chat": false, "blockers": []string{"还不能回复：先选聊天模型"}})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load assistant status failed"})
	}
	return c.JSON(http.StatusOK, status)
}

func (s *Server) handleListGroupAssistantTools(c echo.Context) error {
	_, chatID, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	policy, err := s.botService.Assistant().Policy(c.Request().Context(), chatID)
	if errors.Is(err, pgx.ErrNoRows) {
		policy = defaultGroupAssistantPolicy(chatID)
	} else if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load assistant policy failed"})
	}
	tools := []map[string]any{
		{"name": "knowledge_query", "read_only": true, "enabled": toolEnabled(policy.ToolAllowlist, "knowledge_query"), "scope": "current_group"},
		{"name": "conversation_recall", "read_only": true, "enabled": toolEnabled(policy.ToolAllowlist, "conversation_recall"), "scope": "current_group_retention"},
		{"name": "webfetch_readonly", "read_only": true, "enabled": toolEnabled(policy.ToolAllowlist, "webfetch_readonly"), "scope": "public_allowlisted_domains", "allow_domains": policy.AllowDomains},
	}
	return c.JSON(http.StatusOK, map[string]any{"chat_id": chatID, "tools": tools, "write_tools": []any{}, "server_bound_scope": true})
}

func toolEnabled(list []string, name string) bool {
	if len(list) == 0 {
		return true
	}
	for _, item := range list {
		if strings.TrimSpace(item) == name {
			return true
		}
	}
	return false
}

func (s *Server) handleListGroupAssistantRecentSenders(c echo.Context) error {
	_, chatID, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	limit := parseAssistantLimit(c.QueryParam("limit"), 50)
	items, err := s.botService.Queries().ListGroupAssistantRecentSenders(c.Request().Context(), chatID, limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list recent senders failed"})
	}
	senders := make([]map[string]any, 0, len(items))
	for _, item := range items {
		senders = append(senders, map[string]any{
			"user_id": item.SenderID, "user_name": item.SenderName,
			"message_count": item.MessageCount, "last_seen_at": item.LastSeenAt,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"chat_id": chatID, "senders": senders})
}

func serializeAssistantMemory(v store.GroupAssistantMemory) map[string]any {
	return map[string]any{"id": v.ID, "chat_id": v.ChatID, "subject": v.Subject, "content": v.Content, "memory_type": v.MemoryType,
		"authority_level": v.AuthorityLevel, "valid_scope": v.ValidScope, "source": map[string]any{"source_type": v.SourceType,
			"source_message_id": v.SourceMessageID, "source_chat_id": v.SourceChatID, "operator_id": v.SourceOperatorID,
			"operator_name": v.SourceOperatorName, "snippet": v.SourceSnippet, "created_at": v.SourceCreatedAt, "verified": v.SourceVerified,
			"currently_verified": v.SourceVerified == "verified"},
		"expires_at": v.ExpiresAt, "active": v.Active, "forgotten_at": v.ForgottenAt, "version": v.Version,
		"created_at": v.CreatedAt, "updated_at": v.UpdatedAt}
}

func (s *Server) handleListGroupAssistantMemories(c echo.Context) error {
	_, chatID, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	includeInactive := c.QueryParam("include_inactive") == "true"
	limit := parseAssistantLimit(c.QueryParam("limit"), 100)
	items, err := s.botService.Queries().ListGroupAssistantMemories(c.Request().Context(), chatID, includeInactive, strings.TrimSpace(c.QueryParam("q")), limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list assistant memories failed"})
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		item.SourceVerified = s.botService.Assistant().VerifyMemorySource(item)
		out = append(out, serializeAssistantMemory(item))
	}
	return c.JSON(http.StatusOK, map[string]any{"chat_id": chatID, "memories": out, "retention_notice": "忘记仅移除本地召回，不删除Telegram远端原消息"})
}

type assistantMemoryRequest struct {
	ExpectedVersion int64      `json:"expected_version"`
	Subject         string     `json:"subject"`
	Content         string     `json:"content"`
	ValidScope      string     `json:"valid_scope"`
	ExpiresAt       *time.Time `json:"expires_at"`
	SourceType      string     `json:"source_type"`
	SourceMessageID *int64     `json:"source_message_id"`
	SourceSnippet   string     `json:"source_snippet"`
}

func validateAdminMemoryRequest(v *assistantMemoryRequest) error {
	if v.SourceMessageID != nil {
		return fmt.Errorf("source_message_id is server-managed and cannot be supplied")
	}
	v.Subject = strings.TrimSpace(v.Subject)
	v.Content = strings.TrimSpace(v.Content)
	v.ValidScope = strings.TrimSpace(v.ValidScope)
	if v.Subject == "" || len([]rune(v.Subject)) > 200 || v.Content == "" || len([]rune(v.Content)) > 4000 {
		return fmt.Errorf("subject or content invalid")
	}
	if v.ValidScope == "" || len([]rune(v.ValidScope)) > 300 || len([]rune(v.SourceSnippet)) > 1000 {
		return fmt.Errorf("scope or source snippet too long")
	}
	if v.ExpiresAt == nil {
		expiry := time.Now().UTC().Add(365 * 24 * time.Hour)
		v.ExpiresAt = &expiry
	}
	now := time.Now().UTC()
	if v.ExpiresAt.Before(now) || v.ExpiresAt.After(now.Add(366*24*time.Hour)) {
		return fmt.Errorf("expires_at out of range")
	}
	scope, expiry, ok := assistantbot.NormalizeAssistantScope(v.ValidScope, now, *v.ExpiresAt, assistantbotDefaultRetentionDays)
	if !ok {
		return fmt.Errorf("valid_scope is not a recognized bounded scope")
	}
	v.ValidScope = scope
	v.ExpiresAt = &expiry
	return nil
}

func (s *Server) handleCreateGroupAssistantMemory(c echo.Context) error {
	admin, chatID, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	var req assistantMemoryRequest
	if err := bindAssistantJSON(c, &req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid memory"})
	}
	if err := validateAdminMemoryRequest(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	sourceType := strings.TrimSpace(req.SourceType)
	if sourceType == "" {
		sourceType = "admin_base"
	}
	if sourceType != "admin_base" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "admin memory source_type must be admin_base; Telegram sources are server-verified"})
	}
	hash := assistantMemoryHashScoped(chatID, req.Subject, req.Content, req.ValidScope, "admin_base")
	memory, err := s.botService.Queries().CreateGroupAssistantMemory(c.Request().Context(), store.CreateGroupAssistantMemoryParams{ChatID: chatID, Subject: req.Subject,
		Content: req.Content, MemoryType: "base", AuthorityLevel: "admin_base", ValidScope: req.ValidScope, SourceType: sourceType,
		SourceMessageID: nil, SourceOperatorID: &admin.TelegramID, SourceOperatorName: stringValue(admin.Username), SourceSnippet: strings.TrimSpace(req.SourceSnippet),
		SourceCreatedAt: timePtrAPI(time.Now().UTC()), SourceVerified: "verified", ExpiresAt: *req.ExpiresAt, DedupeHash: hash, SourceContentHash: "", ChangedBy: &admin.TelegramID})
	if err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": "memory already exists or could not be saved"})
	}
	return c.JSON(http.StatusCreated, map[string]any{"memory": serializeAssistantMemory(memory)})
}

func (s *Server) handleGetGroupAssistantMemory(c echo.Context) error {
	_, chatID, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid memory id"})
	}
	memory, err := s.botService.Queries().GetGroupAssistantMemory(c.Request().Context(), id, chatID)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "memory not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load memory failed"})
	}
	memory.SourceVerified = s.botService.Assistant().VerifyMemorySource(memory)
	return c.JSON(http.StatusOK, map[string]any{"memory": serializeAssistantMemory(memory)})
}

func assistantMemorySourceNeedsHash(sourceType string) bool {
	switch strings.TrimSpace(sourceType) {
	case "telegram_approved_message", "telegram_admin_explicit_correction", "telegram_edited_message", "telegram_message":
		return true
	default:
		return false
	}
}

func (s *Server) handleUpdateGroupAssistantMemory(c echo.Context) error {
	admin, chatID, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid memory id"})
	}
	var req assistantMemoryRequest
	if err := bindAssistantJSON(c, &req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid memory"})
	}
	if req.ExpectedVersion <= 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "expected_version required"})
	}
	if err := validateAdminMemoryRequest(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	current, err := s.botService.Queries().GetGroupAssistantMemory(c.Request().Context(), id, chatID)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "memory not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load memory failed"})
	}
	sourceMessageID := current.SourceMessageID
	sourceChatID := current.SourceChatID
	sourceContentHash := current.SourceContentHash
	if assistantMemorySourceNeedsHash(current.SourceType) && sourceMessageID != nil && (sourceChatID == nil || sourceContentHash == "") {
		return c.JSON(http.StatusConflict, map[string]string{"error": "saved Telegram source verification is unavailable"})
	}
	_, err = s.botService.Queries().UpdateGroupAssistantMemory(c.Request().Context(), store.UpdateGroupAssistantMemoryParams{ID: id, ChatID: chatID, ExpectedVersion: req.ExpectedVersion,
		Subject: req.Subject, Content: req.Content, MemoryType: "base", AuthorityLevel: "admin_explicit", ValidScope: req.ValidScope,
		SourceType: "admin_explicit", SourceMessageID: sourceMessageID, SourceChatID: sourceChatID, SourceOperatorID: &admin.TelegramID, SourceOperatorName: stringValue(admin.Username),
		SourceSnippet: strings.TrimSpace(req.SourceSnippet), SourceCreatedAt: timePtrAPI(time.Now().UTC()), SourceVerified: "verified", SourceContentHash: sourceContentHash, ExpiresAt: *req.ExpiresAt,
		DedupeHash: assistantMemoryHashScoped(chatID, req.Subject, req.Content, req.ValidScope, "admin_explicit"), ChangedBy: &admin.TelegramID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response := map[string]any{"error": "memory version conflict", "current": serializeAssistantMemory(current)}
			return c.JSON(http.StatusConflict, response)
		}
		if errors.Is(err, store.ErrGroupAssistantSourceInvalid) {
			return c.JSON(http.StatusConflict, map[string]string{"error": "Telegram source changed or is no longer approved; reload memory"})
		}
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "update memory failed"})
	}
	updated, readErr := s.botService.Queries().GetGroupAssistantMemory(c.Request().Context(), id, chatID)
	if readErr != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load updated memory failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{"memory": serializeAssistantMemory(updated)})
}

func (s *Server) handleForgetGroupAssistantMemory(c echo.Context) error {
	admin, chatID, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid memory id"})
	}
	if err := s.botService.Queries().ForgetGroupAssistantMemory(c.Request().Context(), id, chatID, admin.TelegramID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "memory not found or already forgotten"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "forget memory failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{"forgotten": true, "remote_telegram_deleted": false, "notice": "仅移除群助手本地召回范围"})
}

func (s *Server) handleListGroupAssistantMemoryVersions(c echo.Context) error {
	_, chatID, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid memory id"})
	}
	if _, err = s.botService.Queries().GetGroupAssistantMemory(c.Request().Context(), id, chatID); errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "memory not found"})
	} else if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load memory failed"})
	}
	items, err := s.botService.Queries().ListGroupAssistantMemoryVersions(c.Request().Context(), id, chatID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list memory versions failed"})
	}
	versions := make([]map[string]any, 0, len(items))
	for _, item := range items {
		versions = append(versions, map[string]any{"id": item.ID, "memory_id": item.MemoryID, "version": item.Version, "content": item.Content,
			"memory_type": item.MemoryType, "authority_level": item.AuthorityLevel, "valid_scope": item.ValidScope, "source_type": item.SourceType,
			"source_message_id": item.SourceMessageID, "source_snippet": item.SourceSnippet, "changed_by": item.ChangedBy,
			"change_kind": item.ChangeKind, "created_at": item.CreatedAt})
	}
	return c.JSON(http.StatusOK, map[string]any{"versions": versions})
}

func serializeAssistantConflict(v store.GroupAssistantConflict) map[string]any {
	return map[string]any{"id": v.ID, "chat_id": v.ChatID, "memory_id": v.MemoryID, "subject": v.Subject, "candidate_content": v.CandidateContent,
		"candidate_scope": v.CandidateScope, "candidate_authority": v.CandidateAuthority, "source": map[string]any{"type": v.SourceType, "message_id": v.SourceMessageID, "chat_id": v.SourceChatID, "snippet": v.SourceSnippet},
		"status": v.Status, "resolved_by": v.ResolvedBy, "resolved_at": v.ResolvedAt, "created_at": v.CreatedAt}
}

func (s *Server) handleListGroupAssistantConflicts(c echo.Context) error {
	_, chatID, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	items, err := s.botService.Queries().ListGroupAssistantConflicts(c.Request().Context(), chatID, strings.TrimSpace(c.QueryParam("status")), parseAssistantLimit(c.QueryParam("limit"), 100))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list assistant conflicts failed"})
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, serializeAssistantConflict(item))
	}
	return c.JSON(http.StatusOK, map[string]any{"conflicts": out})
}

type assistantConflictResolveRequest struct {
	Accept                bool   `json:"accept"`
	ExpectedMemoryVersion int64  `json:"expected_memory_version"`
	ResolutionMode        string `json:"resolution_mode"`
}

func (s *Server) handleResolveGroupAssistantConflict(c echo.Context) error {
	admin, chatID, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid conflict id"})
	}
	var req assistantConflictResolveRequest
	if err := bindAssistantJSON(c, &req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid conflict decision"})
	}
	if req.ExpectedMemoryVersion < 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "expected_memory_version cannot be negative"})
	}
	if req.Accept {
		if req.ExpectedMemoryVersion <= 0 {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "expected_memory_version required when accepting"})
		}
		if req.ResolutionMode != "admin_explicit_correction" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "resolution_mode must be admin_explicit_correction when accepting"})
		}
	} else if req.ResolutionMode != "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "resolution_mode is only valid for an explicit admin acceptance"})
	}
	currentConflict, err := s.botService.Queries().GetGroupAssistantConflict(c.Request().Context(), id, chatID)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "conflict not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load conflict failed"})
	}
	item, err := s.botService.Queries().ResolveGroupAssistantConflictWithOptions(c.Request().Context(), store.ResolveGroupAssistantConflictParams{
		ID: id, ChatID: chatID, ActorID: admin.TelegramID, ActorName: stringValue(admin.Username),
		ExpectedMemoryVersion: req.ExpectedMemoryVersion, Accept: req.Accept, ResolutionMode: req.ResolutionMode,
		DedupeHash: assistantMemoryHashScoped(chatID, currentConflict.Subject, currentConflict.CandidateContent, currentConflict.CandidateScope, "admin_explicit"),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "conflict not found"})
	}
	if errors.Is(err, store.ErrGroupAssistantConflictMemoryGone) {
		return c.JSON(http.StatusConflict, map[string]string{"error": "memory is forgotten, inactive, or expired; conflict was not accepted"})
	}
	if errors.Is(err, store.ErrGroupAssistantConflictMemoryChanged) {
		return c.JSON(http.StatusConflict, map[string]string{"error": "memory version changed; reload conflict and memory"})
	}
	if errors.Is(err, store.ErrGroupAssistantConflictAuthority) {
		return c.JSON(http.StatusConflict, map[string]string{"error": "ordinary candidate cannot lower authoritative memory; use explicit admin correction"})
	}
	if err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": "resolve conflict failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{"conflict": serializeAssistantConflict(item), "resolution_mode": req.ResolutionMode, "admin_confirmation": req.Accept})
}

func serializeAssistantMessage(v store.GroupAssistantMessage) map[string]any {
	return map[string]any{"id": v.ID, "chat_id": v.ChatID, "thread_id": v.ThreadID, "telegram_message_id": v.TelegramMessageID,
		"sender_id": v.SenderID, "sender_name": v.SenderName, "role": v.Role, "text": v.Text, "approved": v.Approved, "delivered": v.Delivered,
		"expires_at": v.ExpiresAt, "source": map[string]any{"type": v.SourceType, "id": v.SourceID}, "created_at": v.CreatedAt}
}

func (s *Server) handleListGroupAssistantHistory(c echo.Context) error {
	_, chatID, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	var thread *int32
	if raw := strings.TrimSpace(c.QueryParam("thread_id")); raw != "" {
		parsed, parseErr := strconv.ParseInt(raw, 10, 32)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid thread_id"})
		}
		value := int32(parsed)
		thread = &value
	}
	var sender *int64
	if raw := strings.TrimSpace(c.QueryParam("sender_id")); raw != "" {
		parsed, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid sender_id"})
		}
		sender = &parsed
	}
	items, err := s.botService.Queries().ListGroupAssistantMessages(c.Request().Context(), store.ListGroupAssistantMessagesParams{ChatID: chatID, ThreadID: thread, SenderID: sender, Query: strings.TrimSpace(c.QueryParam("q")), Limit: parseAssistantLimit(c.QueryParam("limit"), 100)})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list assistant history failed"})
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, serializeAssistantMessage(item))
	}
	retentionDays := int32(7)
	if policy, policyErr := s.botService.Assistant().Policy(c.Request().Context(), chatID); policyErr == nil && policy.RetentionDays > 0 {
		retentionDays = policy.RetentionDays
	}
	return c.JSON(http.StatusOK, map[string]any{"chat_id": chatID, "history": out, "retention_days": retentionDays, "expired_auto_removed": true})
}

func (s *Server) handleListGroupAssistantDispatches(c echo.Context) error {
	_, chatID, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	items, err := s.botService.Queries().ListGroupAssistantDispatches(c.Request().Context(), chatID, parseAssistantLimit(c.QueryParam("limit"), 50))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list assistant dispatches failed"})
	}
	dispatches := make([]map[string]any, 0, len(items))
	for _, item := range items {
		dispatches = append(dispatches, map[string]any{"id": item.ID, "chat_id": item.ChatID, "request_id": item.RequestID, "task_type": item.TaskType,
			"endpoint_id": item.EndpointID, "model_ref": item.ModelRef, "reason": item.Reason, "status": item.Status, "error": item.ErrorText,
			"latency_ms": item.LatencyMs, "created_at": item.CreatedAt})
	}
	return c.JSON(http.StatusOK, map[string]any{"dispatches": dispatches})
}

func bindAssistantJSON(c echo.Context, target any) error {
	decoder := json.NewDecoder(c.Request().Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	return nil
}

func parseAssistantLimit(raw string, fallback int32) int32 {
	if value, err := strconv.ParseInt(raw, 10, 32); err == nil && value > 0 && value <= 200 {
		return int32(value)
	}
	return fallback
}

func assistantMemoryHash(chatID int64, subject, content, authority string) string {
	return assistantMemoryHashScoped(chatID, subject, content, "", authority)
}

func assistantMemoryHashScoped(chatID int64, subject, content, scope, authority string) string {
	return fmt.Sprintf("%x", sha256Bytes(fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s", chatID, subject, scope, content, authority)))
}
func sha256Bytes(text string) []byte        { sum := sha256.Sum256([]byte(text)); return sum[:] }
func timePtrAPI(value time.Time) *time.Time { return &value }
func jsonMarshal(value any) ([]byte, error) { return json.Marshal(value) }
