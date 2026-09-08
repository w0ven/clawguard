package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/redact"
	"github.com/openclaw/clawguard/internal/store"
)

func marshalUserTrustBanReason(rule, matched, source string) []byte {
	raw, _ := json.Marshal(map[string]any{
		"rule":    strings.TrimSpace(rule),
		"matched": strings.TrimSpace(matched),
		"source":  strings.TrimSpace(source),
	})
	return raw
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

var errDangerousGlobalConfigTruncation = errors.New("global config update appears truncated; reload current config and submit the full document")

func (s *Server) registerAdminRoutes() {
	admin := s.echo.Group("/api/admin", s.requireAdminJWT, s.requireCSRF, s.adminWriteRateLimit())
	admin.GET("/groups", s.handleListGroups)
	admin.GET("/groups/:chat_id", s.handleGetGroup)
	admin.PUT("/groups/:chat_id/config", s.handlePutGroupConfig)
	admin.GET("/groups/:chat_id/join-protection", s.handleGetJoinProtection)
	admin.PUT("/groups/:chat_id/join-protection", s.handlePutJoinProtection)
	admin.GET("/authorized-groups", s.handleListAuthorizedGroups)
	admin.POST("/authorized-groups", s.handleCreateAuthorizedGroup)
	admin.PUT("/authorized-groups/:chat_id", s.handleUpdateAuthorizedGroup)
	admin.DELETE("/authorized-groups/:chat_id", s.handleDeleteAuthorizedGroup)
	admin.GET("/global-config", s.handleGetGlobalConfig)
	admin.PUT("/global-config", s.handlePutGlobalConfig)
	admin.GET("/system-state", s.handleGetSystemState)
	admin.PUT("/system-state", s.handlePutSystemState)
	admin.GET("/health", s.handleHealth)
	admin.GET("/events", s.handleListEvents)
	admin.GET("/violations", s.handleListViolations)
	admin.GET("/profile-check-logs", s.handleListProfileCheckLogs)
	admin.DELETE("/profile-check-logs", s.handleDeleteOldProfileCheckLogs)
	admin.GET("/warnings", s.handleListWarnings)
	admin.POST("/warnings/clear", s.handleClearWarnings)
	admin.POST("/ban", s.handleBan, s.apiRateLimit(s.adminRateLimitKey("admin:ban"), adminBanLimit, apiRateLimitWindow))
	admin.POST("/unban", s.handleUnban, s.apiRateLimit(s.adminRateLimitKey("admin:ban"), adminBanLimit, apiRateLimitWindow))
	admin.GET("/audit", s.handleListAudit)
	admin.GET("/stats", s.handleStats)
	admin.GET("/admins", s.handleListAdmins)
	admin.POST("/admins", s.requireOwner(s.handleCreateAdmin))
	admin.PUT("/admins/:id", s.requireOwner(s.handleUpdateAdmin))
	admin.DELETE("/admins/:id", s.requireOwner(s.handleDeleteAdmin))
	admin.GET("/ai-models", s.handleListAIModels)
	admin.GET("/ai-providers", s.handleListAIModels)
	admin.POST("/ai-test", s.handleAITest)
	admin.POST("/ai-prompt-preview", s.handleAIPromptPreview)
	admin.GET("/ai-decisions", s.handleListAIDecisions)
	admin.PUT("/ai-decisions/:id", s.handleUpdateAIDecision)
	admin.GET("/ai-cache-stats", s.handleGetAICacheStats)
	admin.GET("/user-trust", s.handleListUserTrust)
	admin.PUT("/user-trust/:chat_id/:user_id", s.handleUpdateUserTrust)
	admin.GET("/ai-calls", s.handleListAICalls)

	s.registerAdminLLMRoutes(admin)
	s.registerAdminAdKillerRoutes(admin)
	s.registerScheduledMessageRoutes(admin)
	s.registerGroupAssistantRoutes(admin)
	s.registerAssistantGlobalRoutes(admin)
}

func (s *Server) handleListGroups(c echo.Context) error {
	admin, _ := currentAdmin(c)
	chatIDs, _ := adminScopeFilter(admin)
	groups, err := s.botService.Queries().ListManagedGroupsScoped(c.Request().Context(), chatIDs)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list groups failed"})
	}

	if groups == nil {
		groups = make([]store.Group, 0)
	}
	items := make([]map[string]any, 0, len(groups))
	for _, group := range groups {
		items = append(items, serializeGroup(group))
	}
	return c.JSON(http.StatusOK, map[string]any{"groups": items})
}

func (s *Server) handleGetGroup(c echo.Context) error {
	admin, _ := currentAdmin(c)
	chatID, err := parseRequiredInt64(c.Param("chat_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid chat_id"})
	}
	if !adminCanAccessChat(admin, chatID) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
	}

	authorized, err := s.botService.Queries().GetAuthorizedGroupByChatID(c.Request().Context(), chatID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "group not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load group failed"})
	}
	if !authorized.Enabled {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "group not found"})
	}

	group, err := s.botService.Queries().GetGroupByChatID(c.Request().Context(), chatID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "group not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load group failed"})
	}

	policy, err := s.botService.LoadGuardPolicy(c.Request().Context(), chatID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load policy failed"})
	}
	policy.Messages.KeywordReplies = s.botService.MergeKeywordReplyStats(c.Request().Context(), chatID, policy.Messages.KeywordReplies)

	return c.JSON(http.StatusOK, map[string]any{
		"group":          serializeGroup(group),
		"merged_policy":  policy,
		"effective_json": mustRawJSON(policy),
	})
}

func (s *Server) handlePutGroupConfig(c echo.Context) error {
	admin, _ := currentAdmin(c)
	chatID, err := parseRequiredInt64(c.Param("chat_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid chat_id"})
	}
	if !adminCanAccessChat(admin, chatID) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
	}

	authorized, err := s.botService.Queries().GetAuthorizedGroupByChatID(c.Request().Context(), chatID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "group not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load group failed"})
	}
	if !authorized.Enabled {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "group not found"})
	}

	group, err := s.botService.Queries().GetGroupByChatID(c.Request().Context(), chatID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "group not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load group failed"})
	}

	nextConfig, err := normalizeJSONBody(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": redact.ErrorString(err)})
	}
	nextConfig, err = preserveCurrentJoinProtection(group.Config, nextConfig)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": redact.ErrorString(err)})
	}
	if err := validateJoinProtectionConfigDocument(nextConfig); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": redact.ErrorString(err)})
	}
	if err := validateGroupPolicyUpdate(c.Request().Context(), s.botService.Queries(), nextConfig); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": redact.ErrorString(err)})
	}

	updated, err := s.botService.Queries().UpdateGroupConfig(c.Request().Context(), store.UpdateGroupConfigParams{
		ChatID: chatID,
		Config: nextConfig,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "update group config failed"})
	}

	if err := s.writeAudit(c.Request().Context(), admin, "group", &chatID, "update_group_config", group.Config, updated.Config); err != nil {
		s.logger.Warn("write group config audit failed")
	}

	policy, err := s.botService.RefreshGuardPolicySnapshot(c.Request().Context(), chatID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load merged policy failed"})
	}
	policy.Messages.KeywordReplies = s.botService.MergeKeywordReplyStats(c.Request().Context(), chatID, policy.Messages.KeywordReplies)

	return c.JSON(http.StatusOK, map[string]any{
		"group":         serializeGroup(updated),
		"merged_policy": policy,
	})
}

func (s *Server) handleGetJoinProtection(c echo.Context) error {
	admin, _ := currentAdmin(c)
	chatID, err := parseRequiredInt64(c.Param("chat_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid chat_id"})
	}
	if !adminCanAccessChat(admin, chatID) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
	}
	if _, err := s.botService.Queries().GetGroupByChatID(c.Request().Context(), chatID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "group not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load group failed"})
	}
	policy, err := s.botService.LoadGuardPolicy(c.Request().Context(), chatID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load join protection failed"})
	}
	status := s.botService.JoinProtectionStatus(c.Request().Context(), chatID, policy.JoinProtection.Enabled)
	return c.JSON(http.StatusOK, map[string]any{
		"join_protection": policy.JoinProtection,
		"defaults":        config.DefaultJoinProtectionPolicy,
		"status":          status,
	})
}

func (s *Server) handlePutJoinProtection(c echo.Context) error {
	admin, _ := currentAdmin(c)
	chatID, err := parseRequiredInt64(c.Param("chat_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid chat_id"})
	}
	if !adminCanAccessChat(admin, chatID) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
	}
	group, err := s.botService.Queries().GetGroupByChatID(c.Request().Context(), chatID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "group not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load group failed"})
	}

	var next config.JoinProtectionPolicy
	decoder := json.NewDecoder(c.Request().Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&next); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid join protection config"})
	}
	if err := config.ValidateJoinProtectionPolicy(next); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": redact.ErrorString(err)})
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "marshal join protection failed"})
	}
	updated, err := s.botService.Queries().UpdateGroupJoinProtection(c.Request().Context(), store.UpdateGroupJoinProtectionParams{
		ChatID:         chatID,
		JoinProtection: raw,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "update join protection failed"})
	}
	if !next.Enabled {
		if err := s.botService.ClearJoinProtectionState(c.Request().Context(), chatID); err != nil {
			s.logger.Warn("clear disabled join protection runtime state failed", zap.Error(err), zap.Int64("chat_id", chatID))
		}
	}
	if err := s.writeAudit(c.Request().Context(), admin, "group", &chatID, "update_join_protection", group.Config, updated.Config); err != nil {
		s.logger.Warn("write join protection audit failed", zap.Error(err))
	}
	if _, err := s.botService.RefreshGuardPolicySnapshot(c.Request().Context(), chatID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "refresh join protection policy failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"join_protection": next,
		"group":           serializeGroup(updated),
		"status":          s.botService.JoinProtectionStatus(c.Request().Context(), chatID, next.Enabled),
	})
}

func (s *Server) handleGetGlobalConfig(c echo.Context) error {
	admin, _ := currentAdmin(c)
	if !adminHasGlobalAccess(admin) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "global config out of scope"})
	}
	globalConfig, err := s.botService.Queries().GetGlobalConfig(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load global config failed"})
	}
	return c.JSON(http.StatusOK, serializeGlobalConfig(globalConfig))
}

func (s *Server) handleListAuthorizedGroups(c echo.Context) error {
	admin, _ := currentAdmin(c)
	if !adminHasGlobalAccess(admin) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "authorized groups out of scope"})
	}
	items, err := s.botService.Queries().ListAuthorizedGroups(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list authorized groups failed"})
	}
	response := make([]map[string]any, 0, len(items))
	for _, item := range items {
		response = append(response, serializeAuthorizedGroup(item))
	}
	return c.JSON(http.StatusOK, map[string]any{"groups": response})
}

func (s *Server) handleCreateAuthorizedGroup(c echo.Context) error {
	admin, _ := currentAdmin(c)
	if !adminHasGlobalAccess(admin) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "authorized groups out of scope"})
	}
	return createAuthorizedGroupFromContext(c, s.botService.Queries(), func(ctx context.Context, created store.AuthorizedGroup) {
		s.botService.SetAuthorizedGroupCache(ctx, created.ChatID, created.Enabled)
		if auditErr := s.writeAudit(ctx, admin, "authorized_group", &created.ChatID, "upsert_authorized_group", nil, mustRawJSON(serializeAuthorizedGroup(created))); auditErr != nil {
			s.logger.Warn("write authorized group audit failed", zap.Error(auditErr))
		}
	})
}

const authorizedGroupAlreadyExistsMessage = "授权群已存在"

var (
	errAuthorizedGroupAlreadyExists = errors.New(authorizedGroupAlreadyExistsMessage)
	errLoadAuthorizedGroup          = errors.New("load authorized group failed")
	errSaveAuthorizedGroup          = errors.New("save authorized group failed")
)

type authorizedGroupWriter interface {
	GetAuthorizedGroupByChatID(ctx context.Context, chatID int64) (store.AuthorizedGroup, error)
	UpsertAuthorizedGroup(ctx context.Context, arg store.UpsertAuthorizedGroupParams) (store.AuthorizedGroup, error)
}

func createAuthorizedGroupIfAbsent(ctx context.Context, queries authorizedGroupWriter, params store.UpsertAuthorizedGroupParams) (store.AuthorizedGroup, error) {
	_, err := queries.GetAuthorizedGroupByChatID(ctx, params.ChatID)
	if err == nil {
		return store.AuthorizedGroup{}, errAuthorizedGroupAlreadyExists
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return store.AuthorizedGroup{}, fmt.Errorf("%w: %w", errLoadAuthorizedGroup, err)
	}
	created, err := queries.UpsertAuthorizedGroup(ctx, params)
	if err != nil {
		return store.AuthorizedGroup{}, fmt.Errorf("%w: %w", errSaveAuthorizedGroup, err)
	}
	return created, nil
}

func createAuthorizedGroupFromContext(c echo.Context, queries authorizedGroupWriter, afterCreate func(context.Context, store.AuthorizedGroup)) error {
	admin, _ := currentAdmin(c)
	var payload struct {
		ChatID  int64   `json:"chat_id"`
		Title   *string `json:"title"`
		Enabled *bool   `json:"enabled"`
		Notes   *string `json:"notes"`
	}
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json body"})
	}
	if payload.ChatID == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "chat_id required"})
	}

	created, err := createAuthorizedGroupIfAbsent(c.Request().Context(), queries, store.UpsertAuthorizedGroupParams{
		ChatID:       payload.ChatID,
		Title:        trimStringPtr(payload.Title),
		AuthorizedBy: &admin.TelegramID,
		Enabled:      payload.Enabled,
		Notes:        trimStringPtr(payload.Notes),
	})
	if errors.Is(err, errAuthorizedGroupAlreadyExists) {
		return c.JSON(http.StatusConflict, map[string]string{"error": authorizedGroupAlreadyExistsMessage})
	}
	if errors.Is(err, errLoadAuthorizedGroup) {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load authorized group failed"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "save authorized group failed"})
	}
	if afterCreate != nil {
		afterCreate(c.Request().Context(), created)
	}
	return c.JSON(http.StatusOK, map[string]any{"group": serializeAuthorizedGroup(created)})
}

func (s *Server) handleUpdateAuthorizedGroup(c echo.Context) error {
	admin, _ := currentAdmin(c)
	if !adminHasGlobalAccess(admin) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "authorized groups out of scope"})
	}
	chatID, err := parseRequiredInt64(c.Param("chat_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid chat_id"})
	}
	before, err := s.botService.Queries().GetAuthorizedGroupByChatID(c.Request().Context(), chatID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "authorized group not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load authorized group failed"})
	}

	var payload struct {
		Title   *string `json:"title"`
		Enabled *bool   `json:"enabled"`
		Notes   *string `json:"notes"`
	}
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json body"})
	}

	title := stringPtr(before.Title)
	if payload.Title != nil {
		title = trimStringPtr(payload.Title)
	}
	notes := stringPtr(before.Notes)
	if payload.Notes != nil {
		notes = trimStringPtr(payload.Notes)
		if notes == nil {
			notes = stringPtr("")
		}
	}
	enabled := before.Enabled
	if payload.Enabled != nil {
		enabled = *payload.Enabled
	}

	updated, err := s.botService.Queries().UpsertAuthorizedGroup(c.Request().Context(), store.UpsertAuthorizedGroupParams{
		ChatID:       chatID,
		Title:        title,
		AuthorizedBy: before.AuthorizedBy,
		Enabled:      &enabled,
		Notes:        notes,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "update authorized group failed"})
	}
	s.botService.SetAuthorizedGroupCache(c.Request().Context(), chatID, updated.Enabled)

	if auditErr := s.writeAudit(c.Request().Context(), admin, "authorized_group", &chatID, "update_authorized_group", mustRawJSON(serializeAuthorizedGroup(before)), mustRawJSON(serializeAuthorizedGroup(updated))); auditErr != nil {
		s.logger.Warn("write authorized group audit failed", zap.Error(auditErr))
	}

	return c.JSON(http.StatusOK, map[string]any{"group": serializeAuthorizedGroup(updated)})
}

func (s *Server) handleDeleteAuthorizedGroup(c echo.Context) error {
	admin, _ := currentAdmin(c)
	if !adminHasGlobalAccess(admin) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "authorized groups out of scope"})
	}
	chatID, err := parseRequiredInt64(c.Param("chat_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid chat_id"})
	}
	before, err := s.botService.Queries().GetAuthorizedGroupByChatID(c.Request().Context(), chatID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "authorized group not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load authorized group failed"})
	}
	if err := s.botService.Queries().DeleteAuthorizedGroup(c.Request().Context(), chatID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "delete authorized group failed"})
	}
	s.botService.SetAuthorizedGroupCache(c.Request().Context(), chatID, false)
	if leaveErr := s.botService.LeaveChat(chatID); leaveErr != nil {
		s.logger.Warn("leave revoked authorized group failed", zap.Error(leaveErr), zap.Int64("chat_id", chatID))
	}
	if deleteErr := s.botService.Queries().DeleteGroupByChatID(c.Request().Context(), chatID); deleteErr != nil {
		s.logger.Warn("delete revoked group record failed", zap.Error(deleteErr), zap.Int64("chat_id", chatID))
	}
	if auditErr := s.writeAudit(c.Request().Context(), admin, "authorized_group", &chatID, "delete_authorized_group", mustRawJSON(serializeAuthorizedGroup(before)), []byte("null")); auditErr != nil {
		s.logger.Warn("write authorized group audit failed", zap.Error(auditErr))
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) handleGetSystemState(c echo.Context) error {
	admin, _ := currentAdmin(c)
	if !adminHasGlobalAccess(admin) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "system state out of scope"})
	}
	state, err := s.botService.GetSystemState(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load system state failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{"state": serializeSystemState(state)})
}

func (s *Server) handlePutSystemState(c echo.Context) error {
	admin, _ := currentAdmin(c)
	if !adminHasGlobalAccess(admin) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "system state out of scope"})
	}
	current, err := s.botService.GetSystemState(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load system state failed"})
	}

	var payload struct {
		AIPaused       *bool   `json:"ai_paused"`
		ActionsPaused  *bool   `json:"actions_paused"`
		Frozen         *bool   `json:"frozen"`
		AIPausedReason *string `json:"ai_paused_reason"`
	}
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json body"})
	}

	next := current
	if payload.AIPaused != nil {
		next.AIPaused = *payload.AIPaused
	}
	if payload.ActionsPaused != nil {
		next.ActionsPaused = *payload.ActionsPaused
	}
	if payload.Frozen != nil {
		next.Frozen = *payload.Frozen
	}
	if payload.AIPausedReason != nil {
		next.AIPausedReason = strings.TrimSpace(*payload.AIPausedReason)
	}
	if !next.AIPaused {
		next.AIPausedReason = ""
	}
	updated, err := s.botService.UpdateSystemState(c.Request().Context(), store.UpdateSystemStateParams{
		AIPaused:       next.AIPaused,
		ActionsPaused:  next.ActionsPaused,
		Frozen:         next.Frozen,
		AIPausedReason: next.AIPausedReason,
		UpdatedBy:      &admin.TelegramID,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "update system state failed"})
	}
	if auditErr := s.writeAudit(c.Request().Context(), admin, "global", nil, "update_system_state", mustRawJSON(serializeSystemState(current)), mustRawJSON(serializeSystemState(updated))); auditErr != nil {
		s.logger.Warn("write system state audit failed", zap.Error(auditErr))
	}
	return c.JSON(http.StatusOK, map[string]any{"state": serializeSystemState(updated)})
}

func (s *Server) handleListViolations(c echo.Context) error {
	admin, _ := currentAdmin(c)
	chatID, err := parseOptionalInt64(c.QueryParam("chat_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid chat_id"})
	}
	userID, err := parseOptionalInt64(c.QueryParam("user_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid user_id"})
	}
	if chatID != nil && !adminCanAccessChat(admin, *chatID) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
	}
	since, until, err := parseTimeRange(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": redact.ErrorString(err)})
	}

	chatIDs, _ := adminScopeFilter(admin)
	params := store.ListViolationsScopedParams{
		ChatID:  chatID,
		UserID:  userID,
		Rule:    strings.TrimSpace(c.QueryParam("rule")),
		Action:  strings.TrimSpace(c.QueryParam("action")),
		Since:   since,
		Until:   until,
		ChatIds: chatIDs,
		Limit:   parseLimit(c.QueryParam("limit")),
		Offset:  parseOffset(c.QueryParam("offset")),
	}
	total, err := s.botService.Queries().CountViolationsScoped(c.Request().Context(), store.CountViolationsScopedParams{
		ChatID:  params.ChatID,
		UserID:  params.UserID,
		Rule:    params.Rule,
		Action:  params.Action,
		Since:   params.Since,
		Until:   params.Until,
		ChatIds: params.ChatIds,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "count violations failed"})
	}
	items, err := s.botService.Queries().ListViolationsScoped(c.Request().Context(), params)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list violations failed"})
	}
	if items == nil {
		items = make([]store.Violation, 0)
	}
	response := make([]map[string]any, 0, len(items))
	for _, item := range items {
		response = append(response, serializeViolation(item))
	}
	return c.JSON(http.StatusOK, map[string]any{"violations": response, "total": total})
}

func (s *Server) handleListProfileCheckLogs(c echo.Context) error {
	admin, _ := currentAdmin(c)
	chatID, err := parseRequiredInt64(c.QueryParam("chat_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid chat_id"})
	}
	if !adminCanAccessChat(admin, chatID) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
	}

	result, err := parseProfileCheckLogResult(c.QueryParam("result"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": redact.ErrorString(err)})
	}
	mode, err := parseProfileCheckLogMode(c.QueryParam("mode"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": redact.ErrorString(err)})
	}

	page := parsePage(c.QueryParam("page"))
	pageSize := parsePageSize(c.QueryParam("page_size"))
	offset := int32((page - 1) * pageSize)
	search := strings.TrimSpace(c.QueryParam("search"))

	total, err := s.botService.Queries().CountProfileCheckLogs(c.Request().Context(), store.CountProfileCheckLogsParams{
		ChatID:    chatID,
		Search:    search,
		Result:    result,
		CheckMode: mode,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "count profile check logs failed"})
	}

	items, err := s.botService.Queries().ListProfileCheckLogs(c.Request().Context(), store.ListProfileCheckLogsParams{
		ChatID:    chatID,
		Search:    search,
		Result:    result,
		CheckMode: mode,
		Limit:     int32(pageSize),
		Offset:    offset,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list profile check logs failed"})
	}
	if items == nil {
		items = make([]store.ProfileCheckLog, 0)
	}

	response := make([]map[string]any, 0, len(items))
	for _, item := range items {
		response = append(response, serializeProfileCheckLog(item))
	}
	return c.JSON(http.StatusOK, map[string]any{
		"items": response,
		"total": total,
	})
}

func (s *Server) handleDeleteOldProfileCheckLogs(c echo.Context) error {
	admin, _ := currentAdmin(c)
	if !adminHasGlobalAccess(admin) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "profile check log cleanup out of scope"})
	}

	days, err := parseCleanupDays(c.QueryParam("days"), 30)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": redact.ErrorString(err)})
	}

	deleted, err := s.botService.Queries().DeleteOldProfileCheckLogs(c.Request().Context(), int32(days))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "delete profile check logs failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"deleted": deleted,
		"days":    days,
	})
}

func (s *Server) handleListWarnings(c echo.Context) error {
	admin, _ := currentAdmin(c)
	chatID, err := parseOptionalInt64(c.QueryParam("chat_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid chat_id"})
	}
	userID, err := parseOptionalInt64(c.QueryParam("user_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid user_id"})
	}
	if chatID != nil && !adminCanAccessChat(admin, *chatID) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
	}

	chatIDs, _ := adminScopeFilter(admin)
	params := store.ListWarningsScopedParams{
		ChatID:  chatID,
		UserID:  userID,
		ChatIds: chatIDs,
		Limit:   parseLimit(c.QueryParam("limit")),
		Offset:  parseOffset(c.QueryParam("offset")),
	}
	total, err := s.botService.Queries().CountWarningsScoped(c.Request().Context(), store.CountWarningsScopedParams{
		ChatID:  params.ChatID,
		UserID:  params.UserID,
		ChatIds: params.ChatIds,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "count warnings failed"})
	}
	items, err := s.botService.Queries().ListWarningsScoped(c.Request().Context(), params)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list warnings failed"})
	}
	if items == nil {
		items = make([]store.Warning, 0)
	}
	return c.JSON(http.StatusOK, map[string]any{"warnings": func() []map[string]any {
		resp := make([]map[string]any, 0, len(items))
		for _, item := range items {
			resp = append(resp, serializeWarning(item))
		}
		return resp
	}(), "total": total})
}

func (s *Server) handleClearWarnings(c echo.Context) error {
	admin, _ := currentAdmin(c)
	var payload struct {
		ChatID int64 `json:"chat_id"`
		UserID int64 `json:"user_id"`
	}
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json body"})
	}
	if payload.ChatID == 0 || payload.UserID == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "chat_id and user_id required"})
	}
	if !adminCanAccessChat(admin, payload.ChatID) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
	}
	rows, err := s.botService.Queries().ClearWarnings(c.Request().Context(), payload.ChatID, payload.UserID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "clear warnings failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{"cleared": rows})
}

func (s *Server) handleBan(c echo.Context) error {
	admin, _ := currentAdmin(c)
	chatID, userID, err := parseChatAndUser(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": redact.ErrorString(err)})
	}
	if !adminCanAccessChat(admin, chatID) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
	}

	if err := s.botService.BanChatUser(c.Request().Context(), chatID, userID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "ban failed"})
	}

	if _, err := s.botService.Queries().UpsertBannedUser(c.Request().Context(), store.UpsertBannedUserParams{
		UserID:   userID,
		Reason:   stringPtr("manual admin ban"),
		Source:   "manual",
		BannedBy: &admin.TelegramID,
	}); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "record ban failed"})
	}

	return c.NoContent(http.StatusNoContent)
}

func (s *Server) handleUnban(c echo.Context) error {
	admin, _ := currentAdmin(c)
	chatID, userID, err := parseChatAndUser(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": redact.ErrorString(err)})
	}
	if !adminCanAccessChat(admin, chatID) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
	}

	if err := s.botService.UnbanChatUser(c.Request().Context(), chatID, userID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "unban failed"})
	}

	if err := s.botService.Queries().DeleteBannedUser(c.Request().Context(), userID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "clear ban failed"})
	}

	return c.NoContent(http.StatusNoContent)
}

func (s *Server) handleListAudit(c echo.Context) error {
	admin, _ := currentAdmin(c)
	chatID, err := parseOptionalInt64(c.QueryParam("chat_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid chat_id"})
	}
	if chatID != nil && !adminCanAccessChat(admin, *chatID) {
		return c.JSON(http.StatusOK, map[string]any{"audit": []store.ConfigAudit{}})
	}

	items, err := s.botService.Queries().ListAuditByChat(c.Request().Context(), store.ListAuditByChatParams{
		ChatID: chatID,
		Limit:  parseLimit(c.QueryParam("limit")),
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list audit failed"})
	}
	if items == nil {
		items = make([]store.ConfigAudit, 0)
	}
	items = filterAuditByScope(admin, items)
	response := make([]map[string]any, 0, len(items))
	for _, item := range items {
		response = append(response, serializeAudit(item))
	}
	return c.JSON(http.StatusOK, map[string]any{"audit": response})
}

func (s *Server) handleStats(c echo.Context) error {
	admin, _ := currentAdmin(c)
	if !adminHasGlobalAccess(admin) {
		groups, err := s.botService.Queries().ListGroups(c.Request().Context())
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load stats failed"})
		}
		count := int64(0)
		for _, group := range groups {
			if adminCanAccessChat(admin, group.ChatID) {
				count++
			}
		}
		return c.JSON(http.StatusOK, map[string]any{
			"groups_count":         count,
			"active_verifications": 0,
			"today_violations":     0,
		})
	}
	stats, err := s.botService.Queries().GetAdminStats(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load stats failed"})
	}
	return c.JSON(http.StatusOK, serializeStats(stats))
}

func (s *Server) handleHealth(c echo.Context) error {
	ctx := c.Request().Context()
	dbStarted := time.Now()
	dbErr := s.botService.Queries().Ping(ctx)
	dbLatency := time.Since(dbStarted).Milliseconds()
	if dbErr != nil {
		dbLatency = -1
	}

	var redisLatency *int64
	if redisClient := s.botService.Redis(); redisClient != nil {
		start := time.Now()
		if err := redisClient.Ping(ctx).Err(); err == nil {
			value := time.Since(start).Milliseconds()
			redisLatency = &value
		}
	}

	todayCalls, err := s.botService.Queries().GetTodayAICallCount(ctx)
	if err != nil {
		todayCalls = 0
	}
	status := s.botService.Status()
	backlog, backlogErr := s.botService.Queries().GetOperationsBacklog(ctx)
	if backlogErr != nil {
		backlog = store.OperationsBacklog{}
	}
	operations := s.botService.OperationsRuntimeStatus(ctx)

	return c.JSON(http.StatusOK, map[string]any{
		"db_latency_ms":                    dbLatency,
		"redis_latency_ms":                 redisLatency,
		"webhook_last_update_at":           status["webhook_last_update_at"],
		"webhook_seconds_ago":              status["webhook_seconds_ago"],
		"ai_last_ok_at":                    status["ai_last_ok_at"],
		"ai_last_fail_at":                  status["ai_last_fail_at"],
		"ai_last_error":                    redact.Value(status["ai_last_error"]),
		"today_calls":                      todayCalls,
		"uptime_seconds":                   status["uptime_seconds"],
		"active_pending":                   backlog.ActivePending,
		"due_cleanup":                      backlog.DueCleanup,
		"retrying_cleanup":                 backlog.RetryingCleanup,
		"oldest_due_seconds":               backlog.OldestDueCleanupSeconds,
		"join_cleanup_dead":                operations.JoinCleanupDeadLetters,
		"backup_last_at":                   operations.BackupLastAt,
		"backup_seconds_ago":               operations.BackupSecondsAgo,
		"verification_worker_seconds_ago":  operations.VerificationWorkerSecondsAgo,
		"join_recovery_worker_seconds_ago": operations.JoinRecoveryWorkerSecondsAgo,
	})
}

func (s *Server) handleListEvents(c echo.Context) error {
	admin, _ := currentAdmin(c)
	eventType, err := parseEventType(c.QueryParam("type"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": redact.ErrorString(err)})
	}
	scopeChatIDs, includeGlobal := adminScopeFilter(admin)
	limit := parseLimit(c.QueryParam("limit"))
	offset := parseOffset(c.QueryParam("offset"))
	total, err := s.botService.Queries().CountRecentEvents(c.Request().Context(), store.CountRecentEventsParams{
		EventType:     eventType,
		ScopedChatIDs: scopeChatIDs,
		IncludeGlobal: includeGlobal,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "count events failed"})
	}
	items, err := s.botService.Queries().ListEventsPaginated(c.Request().Context(), store.ListEventsPaginatedParams{
		Limit:         limit,
		Offset:        offset,
		EventType:     eventType,
		ScopedChatIDs: scopeChatIDs,
		IncludeGlobal: includeGlobal,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list events failed"})
	}
	response := make([]map[string]any, 0, len(items))
	for _, item := range items {
		response = append(response, map[string]any{
			"type":       item.Type,
			"id":         item.ID,
			"chat_id":    item.ChatID,
			"user_id":    item.UserID,
			"title":      item.Title,
			"detail":     item.Detail,
			"extra":      item.Extra,
			"created_at": item.CreatedAt,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"events":   response,
		"total":    total,
		"has_more": int64(offset)+int64(len(response)) < total,
	})
}

type adminLister interface {
	ListAdmins(ctx context.Context) ([]store.Admin, error)
}

func (s *Server) handleListAdmins(c echo.Context) error {
	return listAdminsFromContext(c, s.botService.Queries())
}

// listAdminsFromContext returns the admin roster narrowed to what the requesting
// admin needs to see. Scope-restricted admins must not be able to enumerate the
// full roster (Telegram IDs, usernames and notes of admins outside their scope).
func listAdminsFromContext(c echo.Context, queries adminLister) error {
	viewer, _ := currentAdmin(c)
	items, err := queries.ListAdmins(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list admins failed"})
	}
	response := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if !adminVisibleToViewer(viewer, item) {
			continue
		}
		response = append(response, serializeAdminForViewer(viewer, item))
	}
	return c.JSON(http.StatusOK, map[string]any{"admins": response})
}

func (s *Server) handleCreateAdmin(c echo.Context) error {
	current, _ := currentAdmin(c)

	var payload struct {
		TelegramID int64    `json:"telegram_id"`
		Username   *string  `json:"username"`
		FirstName  *string  `json:"first_name"`
		Role       string   `json:"role"`
		Notes      *string  `json:"notes"`
		GroupScope *[]int64 `json:"group_scope"`
	}
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json body"})
	}
	if payload.TelegramID == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "telegram_id required"})
	}

	role := strings.TrimSpace(payload.Role)
	if role == "" {
		role = "admin"
	}
	if role != "owner" && role != "admin" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid role"})
	}
	if current.Role != "owner" && role != "admin" {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "only owner can create owner"})
	}

	groupScope, ok := normalizeRequestedGroupScope(current, payload.GroupScope)
	if !ok {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "group_scope out of scope"})
	}

	admin, err := s.botService.Queries().UpsertAdmin(c.Request().Context(), store.UpsertAdminParams{
		TelegramID: payload.TelegramID,
		Username:   trimStringPtr(payload.Username),
		FirstName:  trimStringPtr(payload.FirstName),
		Role:       role,
		Notes:      trimStringPtr(payload.Notes),
		GroupScope: marshalGroupScope(groupScope),
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "upsert admin failed"})
	}

	return c.JSON(http.StatusOK, map[string]any{"admin": serializeAdmin(admin)})
}

func (s *Server) handleUpdateAdmin(c echo.Context) error {
	adminID, err := parseRequiredInt64(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}

	var payload struct {
		Role       string   `json:"role"`
		Notes      *string  `json:"notes"`
		GroupScope *[]int64 `json:"group_scope"`
	}
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json body"})
	}

	role := strings.TrimSpace(payload.Role)
	if role != "" && role != "owner" && role != "admin" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid role"})
	}

	var groupScopeRaw []byte
	if payload.GroupScope != nil {
		groupScopeRaw = marshalGroupScope(*payload.GroupScope)
	}

	updated, err := s.botService.Queries().UpdateAdminDetails(c.Request().Context(), store.UpdateAdminDetailsParams{
		ID:         adminID,
		Role:       role,
		Notes:      trimStringPtr(payload.Notes),
		GroupScope: groupScopeRaw,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "admin not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "update admin failed"})
	}

	return c.JSON(http.StatusOK, map[string]any{"admin": serializeAdmin(updated)})
}

func (s *Server) handleDeleteAdmin(c echo.Context) error {
	current, _ := currentAdmin(c)
	adminID, err := parseRequiredInt64(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	if current.ID == adminID {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "cannot delete yourself"})
	}
	if err := s.botService.Queries().DeleteAdmin(c.Request().Context(), adminID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "delete admin failed"})
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) handleListAIModels(c echo.Context) error {
	providers := s.botService.AIProviders()
	models := s.botService.AIModels()
	if providers == nil || models == nil {
		return c.JSON(http.StatusOK, map[string]any{"providers": []any{}})
	}
	grouped := map[string][]map[string]any{}
	for _, model := range models.List(ai.ModelFilter{}) {
		grouped[model.ProviderKey] = append(grouped[model.ProviderKey], map[string]any{
			"key":             model.ModelKey,
			"label":           model.Label,
			"api_format":      model.APIFormat,
			"supports_vision": model.SupportsVision,
			"supports_json":   model.SupportsJSON,
			"supports_tools":  model.SupportsTools,
			"capability_tags": model.CapabilityTags,
			"priority":        model.Priority,
			"enabled":         model.Enabled,
		})
	}
	items := make([]map[string]any, 0, len(providers.List()))
	for _, provider := range providers.List() {
		items = append(items, map[string]any{
			"name":          provider.Key,
			"label":         provider.Label,
			"type":          provider.Type,
			"base_url":      provider.BaseURL,
			"timeout_ms":    provider.TimeoutMs,
			"enabled":       provider.Enabled,
			"extra_headers": provider.ExtraHeaders,
			"models":        grouped[provider.Key],
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"providers": items})
}

func (s *Server) handleAITest(c echo.Context) error {
	admin, _ := currentAdmin(c)
	var payload struct {
		ChatID        *int64 `json:"chat_id"`
		Text          string `json:"text"`
		Scene         string `json:"scene"`
		ModelRef      string `json:"model_ref"`
		RulesOverride string `json:"rules_override"`
	}
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json body"})
	}
	if strings.TrimSpace(payload.Text) == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "text required"})
	}

	chatID := int64(0)
	if payload.ChatID != nil {
		chatID = *payload.ChatID
		if !adminCanAccessChat(admin, chatID) {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
		}
	}

	policy, err := config.LoadPolicy(c.Request().Context(), s.botService.Queries(), chatID)
	if err != nil {
		policy = config.DefaultPolicy
	}

	scene := "message"
	if strings.TrimSpace(strings.ToLower(payload.Scene)) == "bio" {
		scene = "bio"
	}

	// 实时测试不走 batch，立即 flush
	testPolicy := policy.AI
	if rulesOverride := strings.TrimSpace(payload.RulesOverride); rulesOverride != "" {
		if scene == "bio" {
			testPolicy.BioRules = rulesOverride
		} else {
			testPolicy.MessageRules = rulesOverride
		}
	}
	if modelRef := strings.TrimSpace(payload.ModelRef); modelRef != "" {
		testPolicy.PrimaryModelRef = modelRef
		testPolicy.FallbackModelRefs = nil
		testPolicy.PrimaryProvider = ""
		testPolicy.PrimaryModel = ""
	}
	testPolicy.BatchWindowMs = 1
	output, callErr := s.botService.AIModerator().CheckMessage(c.Request().Context(), ai.CheckInput{
		ChatID:    chatID,
		UserID:    admin.TelegramID,
		Text:      payload.Text,
		Scene:     scene,
		Policy:    testPolicy,
		SkipCache: true,
	})
	if callErr != nil {
		return c.JSON(http.StatusBadGateway, map[string]string{"error": redact.ErrorString(callErr)})
	}
	return c.JSON(http.StatusOK, map[string]any{"result": output})
}

func (s *Server) handleAIPromptPreview(c echo.Context) error {
	var payload struct {
		Scene         string `json:"scene"`
		RulesOverride string `json:"rules_override"`
		SampleText    string `json:"sample_text"`
	}
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json body"})
	}

	scene := "message"
	if strings.TrimSpace(strings.ToLower(payload.Scene)) == "bio" {
		scene = "bio"
	}

	policy, err := config.LoadPolicy(c.Request().Context(), s.botService.Queries(), 0)
	if err != nil {
		policy = config.DefaultPolicy
	}

	rules := policy.AI.MessageRules
	if scene == "bio" {
		rules = policy.AI.BioRules
	}
	if override := strings.TrimSpace(payload.RulesOverride); override != "" {
		rules = override
	}

	return c.JSON(http.StatusOK, map[string]any{
		"prompt": ai.BuildPromptPreview(scene, rules, payload.SampleText),
	})
}

func (s *Server) handleListAIDecisions(c echo.Context) error {
	admin, _ := currentAdmin(c)
	chatID, err := parseOptionalInt64(c.QueryParam("chat_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid chat_id"})
	}
	if chatID != nil && !adminCanAccessChat(admin, *chatID) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
	}
	override := strings.TrimSpace(c.QueryParam("admin_override"))
	if override == "pending" {
		override = ""
	}
	userID, err := parseOptionalInt64(c.QueryParam("user_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid user_id"})
	}
	since, until, err := parseTimeRange(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": redact.ErrorString(err)})
	}
	items, err := s.botService.Queries().ListAIDecisions(c.Request().Context(), store.ListAIDecisionsParams{
		ChatID:        chatID,
		UserID:        userID,
		Verdict:       strings.TrimSpace(c.QueryParam("verdict")),
		AdminOverride: override,
		Category:      strings.TrimSpace(c.QueryParam("category")),
		ActionTaken:   strings.TrimSpace(c.QueryParam("action")),
		Scene:         strings.TrimSpace(c.QueryParam("scene")),
		Since:         since,
		Until:         until,
		Limit:         parseLimit(c.QueryParam("limit")),
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list ai decisions failed"})
	}
	response := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if !adminCanAccessChat(admin, item.ChatID) {
			continue
		}
		response = append(response, serializeAIDecision(item))
	}
	return c.JSON(http.StatusOK, map[string]any{"decisions": response})
}

func (s *Server) handleGetAICacheStats(c echo.Context) error {
	redisClient := s.botService.Redis()
	if redisClient == nil {
		return c.JSON(http.StatusOK, map[string]any{
			"hit":  0,
			"miss": 0,
			"rate": 0.0,
		})
	}

	ctx := c.Request().Context()
	hit, err := redisClient.Get(ctx, "ai:cache:hit").Int64()
	if err != nil && !errors.Is(err, redis.Nil) {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load ai cache hit failed"})
	}
	miss, err := redisClient.Get(ctx, "ai:cache:miss").Int64()
	if err != nil && !errors.Is(err, redis.Nil) {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load ai cache miss failed"})
	}

	total := hit + miss
	rate := 0.0
	if total > 0 {
		rate = float64(hit) / float64(total)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"hit":  hit,
		"miss": miss,
		"rate": rate,
	})
}

func (s *Server) handleUpdateAIDecision(c echo.Context) error {
	admin, _ := currentAdmin(c)
	id, err := parseRequiredInt64(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	var payload struct {
		AdminOverride *string `json:"admin_override"`
	}
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json body"})
	}
	ctx := c.Request().Context()
	previous, err := s.botService.Queries().GetAIDecisionByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "decision not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load ai decision failed"})
	}
	if !adminCanAccessChat(admin, previous.ChatID) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
	}

	nextOverride := trimStringPtr(payload.AdminOverride)
	updated, err := s.botService.Queries().UpdateAIDecisionOverride(ctx, id, nextOverride)
	if err != nil {
		if err == pgx.ErrNoRows {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "decision not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "update ai decision failed"})
	}

	response := map[string]any{
		"decision":             serializeAIDecision(updated),
		"unban_performed":      false,
		"unmute_performed":     false,
		"trust_score_changed":  0.0,
		"side_effect_warnings": []string{},
	}

	if stringPtrEqual(previous.AdminOverride, updated.AdminOverride) || updated.AdminOverride == nil {
		return c.JSON(http.StatusOK, response)
	}

	warnings := make([]string, 0, 2)
	action := strings.ToLower(strings.TrimSpace(updated.ActionTaken))
	switch *updated.AdminOverride {
	case "false_positive":
		if strings.Contains(action, "ban") {
			if err := s.botService.UnbanChatUser(ctx, updated.ChatID, updated.UserID); err != nil {
				s.logger.Warn("ai decision auto unban failed", zap.Error(err), zap.Int64("decision_id", updated.ID), zap.Int64("chat_id", updated.ChatID), zap.Int64("user_id", updated.UserID))
				warnings = append(warnings, fmt.Sprintf("override saved, unban failed: %v", err))
			} else {
				response["unban_performed"] = true
				if err := s.botService.Queries().DeleteBannedUser(ctx, updated.UserID); err != nil {
					s.logger.Warn("ai decision clear banned user failed", zap.Error(err), zap.Int64("decision_id", updated.ID), zap.Int64("user_id", updated.UserID))
					warnings = append(warnings, fmt.Sprintf("override saved, clear ban record failed: %v", err))
				}
			}
		}
		if strings.Contains(action, "mute") {
			if err := s.botService.UnmuteChatUser(ctx, updated.ChatID, updated.UserID); err != nil {
				s.logger.Warn("ai decision auto unmute failed", zap.Error(err), zap.Int64("decision_id", updated.ID), zap.Int64("chat_id", updated.ChatID), zap.Int64("user_id", updated.UserID))
				warnings = append(warnings, fmt.Sprintf("override saved, unmute failed: %v", err))
			} else {
				response["unmute_performed"] = true
			}
		}
		// false_positive: AI 冤枉了用户，被错误处罚，需要补偿性加分。
		previousScore := 0.5
		existingTrust, trustErr := s.botService.Queries().GetUserTrust(ctx, updated.ChatID, updated.UserID)
		if trustErr == nil {
			previousScore = existingTrust.Score
		} else if !errors.Is(trustErr, pgx.ErrNoRows) {
			s.logger.Warn("ai decision load user trust failed", zap.Error(trustErr), zap.Int64("decision_id", updated.ID), zap.Int64("chat_id", updated.ChatID), zap.Int64("user_id", updated.UserID))
		}
		trust, err := s.botService.Queries().AdjustUserTrustScore(ctx, store.AdjustUserTrustScoreParams{
			ChatID: updated.ChatID,
			UserID: updated.UserID,
			Delta:  0.1,
		})
		if err != nil {
			s.logger.Warn("ai decision trust score update failed", zap.Error(err), zap.Int64("decision_id", updated.ID), zap.Int64("chat_id", updated.ChatID), zap.Int64("user_id", updated.UserID))
			warnings = append(warnings, fmt.Sprintf("override saved, trust score update failed: %v", err))
		} else {
			response["trust_score_changed"] = trust.Score - previousScore
			response["user_trust_score"] = trust.Score
		}
	case "confirm":
		// confirm: AI 判对了，管理员确认处罚成立，继续扣分。
		previousScore := 0.5
		existingTrust, trustErr := s.botService.Queries().GetUserTrust(ctx, updated.ChatID, updated.UserID)
		if trustErr == nil {
			previousScore = existingTrust.Score
		} else if !errors.Is(trustErr, pgx.ErrNoRows) {
			s.logger.Warn("ai decision load user trust failed", zap.Error(trustErr), zap.Int64("decision_id", updated.ID), zap.Int64("chat_id", updated.ChatID), zap.Int64("user_id", updated.UserID))
		}
		trust, err := s.botService.Queries().AdjustUserTrustScore(ctx, store.AdjustUserTrustScoreParams{
			ChatID: updated.ChatID,
			UserID: updated.UserID,
			Delta:  -0.1,
		})
		if err != nil {
			s.logger.Warn("ai decision trust score update failed", zap.Error(err), zap.Int64("decision_id", updated.ID), zap.Int64("chat_id", updated.ChatID), zap.Int64("user_id", updated.UserID))
			warnings = append(warnings, fmt.Sprintf("override saved, trust score update failed: %v", err))
		} else {
			response["trust_score_changed"] = trust.Score - previousScore
			response["user_trust_score"] = trust.Score
		}
	case "false_negative":
		// false_negative: AI 漏判，管理员补处罚，说明用户风险更高，继续扣分。
		previousScore := 0.5
		existingTrust, trustErr := s.botService.Queries().GetUserTrust(ctx, updated.ChatID, updated.UserID)
		if trustErr == nil {
			previousScore = existingTrust.Score
		} else if !errors.Is(trustErr, pgx.ErrNoRows) {
			s.logger.Warn("ai decision load user trust failed", zap.Error(trustErr), zap.Int64("decision_id", updated.ID), zap.Int64("chat_id", updated.ChatID), zap.Int64("user_id", updated.UserID))
		}
		trust, err := s.botService.Queries().AdjustUserTrustScore(ctx, store.AdjustUserTrustScoreParams{
			ChatID: updated.ChatID,
			UserID: updated.UserID,
			Delta:  -0.2,
		})
		if err != nil {
			s.logger.Warn("ai decision trust score update failed", zap.Error(err), zap.Int64("decision_id", updated.ID), zap.Int64("chat_id", updated.ChatID), zap.Int64("user_id", updated.UserID))
			warnings = append(warnings, fmt.Sprintf("override saved, trust score update failed: %v", err))
		} else {
			response["trust_score_changed"] = trust.Score - previousScore
			response["user_trust_score"] = trust.Score
		}
	}

	response["side_effect_warnings"] = warnings
	return c.JSON(http.StatusOK, response)
}

func (s *Server) handleListUserTrust(c echo.Context) error {
	admin, _ := currentAdmin(c)
	chatID, err := parseOptionalInt64(c.QueryParam("chat_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid chat_id"})
	}
	userID, err := parseOptionalInt64(c.QueryParam("user_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid user_id"})
	}
	if chatID != nil && !adminCanAccessChat(admin, *chatID) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
	}
	status, err := parseTrustStatus(c.QueryParam("status"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid status"})
	}
	username := normalizeUserTrustQuery(c.QueryParam("username"))
	joinedSince, err := parseOptionalTimestamp(c.QueryParam("joined_since"), false)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid joined_since"})
	}
	joinedUntil, err := parseOptionalTimestamp(c.QueryParam("joined_until"), true)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid joined_until"})
	}
	limit := parseLimit(c.QueryParam("limit"))
	offset := parseOffset(c.QueryParam("offset"))

	scopeChatIDs, scopeGlobal := adminScopeFilter(admin)
	total, err := s.botService.Queries().CountUserTrust(c.Request().Context(), chatID, userID, status, username, joinedSince, joinedUntil, scopeGlobal, scopeChatIDs)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "count user trust failed"})
	}
	items, err := s.botService.Queries().ListUserTrustPaginated(c.Request().Context(), store.ListUserTrustPaginatedParams{
		ChatID:       chatID,
		UserID:       userID,
		Status:       status,
		Username:     username,
		JoinedSince:  joinedSince,
		JoinedUntil:  joinedUntil,
		Limit:        limit,
		Offset:       offset,
		ScopeGlobal:  scopeGlobal,
		ScopeChatIDs: scopeChatIDs,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list user trust failed"})
	}
	response := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if !adminCanAccessChat(admin, item.ChatID) {
			continue
		}
		response = append(response, serializeUserTrust(item))
	}

	counts := map[string]int64{}
	for _, key := range []string{"all", "new", "trusted", "suspicious", "banned", "archived"} {
		countStatus := ""
		if key != "all" {
			countStatus = key
		}
		count, err := s.botService.Queries().CountUserTrust(c.Request().Context(), chatID, userID, countStatus, username, joinedSince, joinedUntil, scopeGlobal, scopeChatIDs)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "count user trust status failed"})
		}
		counts[key] = count
	}

	return c.JSON(http.StatusOK, map[string]any{
		"items":    response,
		"total":    total,
		"has_more": int64(offset)+int64(len(response)) < total,
		"counts":   counts,
	})
}

func (s *Server) handleUpdateUserTrust(c echo.Context) error {
	admin, _ := currentAdmin(c)
	chatID, err := parseRequiredInt64(c.Param("chat_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid chat_id"})
	}
	if !adminCanAccessChat(admin, chatID) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
	}
	userID, err := parseRequiredInt64(c.Param("user_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid user_id"})
	}
	var payload struct {
		Status string  `json:"status"`
		Score  float64 `json:"score"`
		Notes  *string `json:"notes"`
	}
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json body"})
	}
	previous, err := s.botService.Queries().GetUserTrust(c.Request().Context(), chatID, userID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "user trust not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load user trust failed"})
	}
	var graduatedAt *time.Time
	var bannedAt *time.Time
	var bannedReason []byte
	nextStatus := strings.TrimSpace(payload.Status)
	if strings.TrimSpace(payload.Status) == "trusted" {
		now := time.Now()
		graduatedAt = &now
	}
	if nextStatus == "banned" {
		now := time.Now()
		bannedAt = &now
		bannedReason = marshalUserTrustBanReason("manual_admin_ban", strings.TrimSpace(stringValue(payload.Notes)), "manual_admin")
	}
	var updated store.UserTrust
	if previous.Status == "banned" && nextStatus != "banned" {
		updated, err = s.botService.Queries().UnbanUserTrust(c.Request().Context(), store.UnbanUserTrustParams{
			ChatID: chatID,
			UserID: userID,
			Score:  payload.Score,
			Notes:  trimStringPtr(payload.Notes),
		})
	} else {
		updated, err = s.botService.Queries().UpdateUserTrustStatus(c.Request().Context(), store.UpdateUserTrustStatusParams{
			ChatID:       chatID,
			UserID:       userID,
			Status:       nextStatus,
			Score:        payload.Score,
			GraduatedAt:  graduatedAt,
			BannedAt:     bannedAt,
			BannedReason: bannedReason,
			Notes:        trimStringPtr(payload.Notes),
		})
	}
	if err != nil {
		if err == pgx.ErrNoRows {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "user trust not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "update user trust failed"})
	}
	response := map[string]any{"trust": serializeUserTrust(updated)}
	switch {
	case nextStatus == "banned":
		if err := s.botService.BanChatUser(c.Request().Context(), chatID, userID); err != nil {
			s.logger.Warn("user trust status ban sync failed", zap.Error(err), zap.Int64("chat_id", chatID), zap.Int64("user_id", userID))
			response["telegram_action_error"] = redact.ErrorString(err)
		}
	case previous.Status == "banned" && nextStatus != "banned":
		if err := s.botService.UnbanChatUser(c.Request().Context(), chatID, userID); err != nil {
			s.logger.Warn("user trust status unban sync failed", zap.Error(err), zap.Int64("chat_id", chatID), zap.Int64("user_id", userID))
			response["telegram_action_error"] = redact.ErrorString(err)
		}
	}
	if previous.Status != "trusted" && updated.Status == "trusted" {
		if err := s.botService.RestoreTrustedChatPermissions(c.Request().Context(), updated); err != nil {
			s.logger.Warn("user trust status trusted permission restore failed", zap.Error(err), zap.Int64("chat_id", chatID), zap.Int64("user_id", userID))
			response["telegram_permission_error"] = redact.ErrorString(err)
		}
	}
	return c.JSON(http.StatusOK, response)
}

func (s *Server) handleListAICalls(c echo.Context) error {
	admin, _ := currentAdmin(c)
	scopeChatIDs, _ := adminScopeFilter(admin)
	todayCalls, err := s.botService.Queries().GetTodayAICallCountScoped(c.Request().Context(), scopeChatIDs)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load ai call summary failed"})
	}
	daily, err := s.botService.Queries().ListDailyAICallsLast30Days(c.Request().Context(), scopeChatIDs)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load ai daily calls failed"})
	}
	perModel, err := s.botService.Queries().ListAICallsPerModelLast30Days(c.Request().Context(), scopeChatIDs)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load ai model calls failed"})
	}
	currentModelRefs, err := currentLLMModelRefs(c.Request().Context(), s.botService.Queries())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load llm models failed"})
	}
	perScene, err := s.botService.Queries().ListAICallsPerSceneLast30Days(c.Request().Context(), scopeChatIDs)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load ai scene calls failed"})
	}
	perChat, err := s.botService.Queries().ListAICallsPerChatLast30Days(c.Request().Context(), scopeChatIDs, 10)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load ai chat calls failed"})
	}

	dailyResponse := make([]map[string]any, 0, len(daily))
	for _, item := range daily {
		dailyResponse = append(dailyResponse, map[string]any{
			"date":  item.Date.Format("2006-01-02"),
			"calls": item.Calls,
		})
	}
	modelResponse := summarizeAICallsByCurrentModels(perModel, currentModelRefs)
	sceneResponse := make([]map[string]any, 0, len(perScene))
	for _, item := range perScene {
		sceneResponse = append(sceneResponse, map[string]any{
			"scene": item.Scene,
			"calls": item.Calls,
		})
	}
	chatResponse := make([]map[string]any, 0, len(perChat))
	for _, item := range perChat {
		chatResponse = append(chatResponse, map[string]any{
			"chat_id": item.ChatID,
			"title":   item.Title,
			"calls":   item.Calls,
		})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"today_calls": todayCalls,
		"daily":       dailyResponse,
		"per_model":   modelResponse,
		"per_scene":   sceneResponse,
		"per_chat":    chatResponse,
	})
}

func (s *Server) writeAudit(ctx context.Context, admin store.Admin, scope string, chatID *int64, action string, before, after []byte) error {
	return writeAuditWithQueries(ctx, s.botService.Queries(), admin, scope, chatID, action, before, after)
}

func writeAuditWithQueries(ctx context.Context, queries *store.Queries, admin store.Admin, scope string, chatID *int64, action string, before, after []byte) error {
	diff := simpleJSONDiff(before, after)
	_, err := queries.InsertAuditEntry(ctx, store.InsertAuditEntryParams{
		Scope:   scope,
		ChatID:  chatID,
		AdminID: admin.ID,
		Action:  action,
		Before:  before,
		After:   after,
		Diff:    &diff,
	})
	return err
}

func normalizeJSONBody(c echo.Context) ([]byte, error) {
	var payload map[string]any
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("invalid json body")
	}
	sanitizeKeywordReplyRuntimeFields(payload)
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal json body")
	}
	return raw, nil
}

func validateGroupPolicyUpdate(ctx context.Context, queries *store.Queries, groupConfig []byte) error {
	if err := config.ValidatePolicyDocument(groupConfig); err != nil {
		return err
	}
	documents := make([][]byte, 0, 2)
	globalConfig, err := queries.GetGlobalConfig(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load global config for validation: %w", err)
	}
	if err == nil {
		documents = append(documents, globalConfig.Config)
	}
	documents = append(documents, groupConfig)
	policy, err := config.MergePolicyDocuments(documents...)
	if err != nil {
		return err
	}
	return config.ValidateGuardPolicy(policy)
}

func validateGlobalPolicyUpdate(ctx context.Context, queries *store.Queries, globalConfig []byte) error {
	if err := config.ValidatePolicyDocument(globalConfig); err != nil {
		return err
	}
	policy, err := config.MergePolicyDocuments(globalConfig)
	if err != nil {
		return err
	}
	if err := config.ValidateGuardPolicy(policy); err != nil {
		return err
	}

	groups, err := queries.ListGroups(ctx)
	if err != nil {
		return fmt.Errorf("list groups for config validation: %w", err)
	}
	for _, group := range groups {
		policy, err := config.MergePolicyDocuments(globalConfig, group.Config)
		if err != nil {
			return fmt.Errorf("group %d config merge failed: %w", group.ChatID, err)
		}
		if err := config.ValidateGuardPolicy(policy); err != nil {
			return fmt.Errorf("group %d effective config is invalid: %w", group.ChatID, err)
		}
	}
	return nil
}

func validateJoinProtectionConfigDocument(raw []byte) error {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("invalid json body")
	}
	section, ok := document["join_protection"]
	if !ok {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(section, &fields); err != nil {
		return fmt.Errorf("join_protection must be an object")
	}
	known := map[string]struct{}{
		"enabled":                           {},
		"join_threshold":                    {},
		"join_window_seconds":               {},
		"protection_duration_seconds":       {},
		"temporary_ban_seconds":             {},
		"admin_notify_interval_seconds":     {},
		"max_pending_verifications":         {},
		"telegram_failure_cooldown_seconds": {},
	}
	for key := range fields {
		if _, ok := known[key]; !ok {
			return fmt.Errorf("unknown join_protection field %s", key)
		}
	}
	policy := config.DefaultJoinProtectionPolicy
	if err := json.Unmarshal(section, &policy); err != nil {
		return fmt.Errorf("invalid join_protection config")
	}
	for key, field := range fields {
		if key == "enabled" {
			continue
		}
		var value int
		if err := json.Unmarshal(field, &value); err != nil || value <= 0 {
			return fmt.Errorf("%s must be a positive integer", key)
		}
	}
	return config.ValidateJoinProtectionPolicy(policy)
}

func preserveCurrentJoinProtection(currentRaw, nextRaw []byte) ([]byte, error) {
	var current map[string]json.RawMessage
	if err := json.Unmarshal(currentRaw, &current); err != nil {
		return nil, fmt.Errorf("invalid current group config")
	}
	currentJoinProtection, ok := current["join_protection"]
	if !ok {
		return nextRaw, nil
	}
	var next map[string]json.RawMessage
	if err := json.Unmarshal(nextRaw, &next); err != nil {
		return nil, fmt.Errorf("invalid json body")
	}
	next["join_protection"] = currentJoinProtection
	raw, err := json.Marshal(next)
	if err != nil {
		return nil, fmt.Errorf("marshal group config")
	}
	return raw, nil
}

func rejectDangerousGlobalConfigTruncation(beforeRaw, nextRaw []byte) error {
	var before map[string]any
	if err := json.Unmarshal(beforeRaw, &before); err != nil {
		return nil
	}
	if !hasSubstantialGlobalConfig(before) {
		return nil
	}

	var next map[string]any
	if err := json.Unmarshal(nextRaw, &next); err != nil {
		return nil
	}
	if !isTinyGlobalConfigPayload(next) {
		return nil
	}
	if !globalConfigDropsExistingFields(before, next) {
		return nil
	}
	return errDangerousGlobalConfigTruncation
}

func hasSubstantialGlobalConfig(config map[string]any) bool {
	if len(config) == 0 {
		return false
	}
	if len(config) > 1 || countJSONFields(config) >= 8 {
		return true
	}
	ai, ok := objectField(config, "ai")
	return ok && hasAnyField(ai, "message_rules", "bio_rules")
}

func isTinyGlobalConfigPayload(config map[string]any) bool {
	return countJSONFields(config) <= 4
}

func globalConfigDropsExistingFields(before, next map[string]any) bool {
	for key := range before {
		if _, ok := next[key]; !ok {
			return true
		}
	}

	beforeAI, ok := objectField(before, "ai")
	if !ok {
		return false
	}
	nextAI, ok := objectField(next, "ai")
	if !ok {
		return len(beforeAI) > 0
	}
	for key := range beforeAI {
		if _, ok := nextAI[key]; !ok {
			return true
		}
	}
	return false
}

func objectField(parent map[string]any, key string) (map[string]any, bool) {
	if parent == nil {
		return nil, false
	}
	value, ok := parent[key]
	if !ok {
		return nil, false
	}
	child, ok := value.(map[string]any)
	if !ok || child == nil {
		return nil, false
	}
	return child, true
}

func hasAnyField(values map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := values[key]; ok {
			return true
		}
	}
	return false
}

func countJSONFields(value any) int {
	switch typed := value.(type) {
	case map[string]any:
		total := len(typed)
		for _, child := range typed {
			total += countJSONFields(child)
		}
		return total
	case []any:
		total := 0
		for _, child := range typed {
			total += countJSONFields(child)
		}
		return total
	default:
		return 0
	}
}

func sanitizeKeywordReplyRuntimeFields(payload map[string]any) {
	if payload == nil {
		return
	}
	messages, ok := payload["messages"].(map[string]any)
	if !ok || messages == nil {
		return
	}
	items, ok := messages["keyword_replies"].([]any)
	if !ok {
		return
	}
	for _, item := range items {
		rule, ok := item.(map[string]any)
		if !ok || rule == nil {
			continue
		}
		delete(rule, "trigger_count")
		delete(rule, "last_triggered_at")
	}
}

func simpleJSONDiff(before, after []byte) string {
	beforeMap := map[string]any{}
	afterMap := map[string]any{}
	_ = json.Unmarshal(before, &beforeMap)
	_ = json.Unmarshal(after, &afterMap)

	changed := map[string]struct{}{}
	for key, value := range beforeMap {
		afterValue, ok := afterMap[key]
		if !ok || !jsonEqual(value, afterValue) {
			changed[key] = struct{}{}
		}
	}
	for key, value := range afterMap {
		beforeValue, ok := beforeMap[key]
		if !ok || !jsonEqual(beforeValue, value) {
			changed[key] = struct{}{}
		}
	}

	keys := make([]string, 0, len(changed))
	for key := range changed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "no top-level changes"
	}
	return "changed keys: " + strings.Join(keys, ", ")
}

func jsonEqual(left, right any) bool {
	leftRaw, _ := json.Marshal(left)
	rightRaw, _ := json.Marshal(right)
	return string(leftRaw) == string(rightRaw)
}

func parseRequiredInt64(value string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(value), 10, 64)
}

func parseOptionalInt64(value string) (*int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func parseOptionalTimestamp(value string, endOfDay bool) (*string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}

	if unixSeconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		parsed := time.Unix(unixSeconds, 0).UTC().Format(time.RFC3339)
		return &parsed, nil
	}

	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		formatted := parsed.UTC().Format(time.RFC3339)
		return &formatted, nil
	}

	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		if endOfDay {
			parsed = parsed.Add(24*time.Hour - time.Second)
		}
		formatted := parsed.UTC().Format(time.RFC3339)
		return &formatted, nil
	}

	return nil, fmt.Errorf("invalid timestamp")
}

func normalizeUserTrustQuery(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "@")
	return strings.TrimSpace(value)
}

func parseTimeRange(c echo.Context) (*string, *string, error) {
	since := strings.TrimSpace(c.QueryParam("since"))
	until := strings.TrimSpace(c.QueryParam("until"))
	if since != "" {
		if _, err := time.Parse(time.RFC3339, since); err != nil {
			return nil, nil, fmt.Errorf("invalid since")
		}
	}
	if until != "" {
		if _, err := time.Parse(time.RFC3339, until); err != nil {
			return nil, nil, fmt.Errorf("invalid until")
		}
	}
	var sincePtr, untilPtr *string
	if since != "" {
		sincePtr = &since
	}
	if until != "" {
		untilPtr = &until
	}
	return sincePtr, untilPtr, nil
}

func parseLimit(value string) int32 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
	if err != nil || parsed <= 0 || parsed > 200 {
		return 50
	}
	return int32(parsed)
}

func parseOffset(value string) int32 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
	if err != nil || parsed < 0 {
		return 0
	}
	return int32(parsed)
}

func parsePage(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 1
	}
	return parsed
}

func parsePageSize(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 || parsed > 100 {
		return 20
	}
	return parsed
}

func parseCleanupDays(value string, fallback int) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("invalid days")
	}
	return parsed, nil
}

func parseEventType(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", "all":
		return "", nil
	case "violation", "ai_decision", "config_change":
		return strings.TrimSpace(value), nil
	default:
		return "", fmt.Errorf("invalid type")
	}
}

func parseTrustStatus(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", "all":
		return "", nil
	case "new", "trusted", "suspicious", "banned", "archived":
		return strings.TrimSpace(value), nil
	default:
		return "", fmt.Errorf("invalid status")
	}
}

func parseProfileCheckLogResult(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", "all":
		return "", nil
	case "pass", "hit", "skip", "error":
		return strings.TrimSpace(value), nil
	default:
		return "", fmt.Errorf("invalid result")
	}
}

func parseProfileCheckLogMode(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", "all":
		return "", nil
	case "keyword", "ai", "on_message_keyword", "on_message_ai":
		return strings.TrimSpace(value), nil
	default:
		return "", fmt.Errorf("invalid mode")
	}
}

func parseChatAndUser(c echo.Context) (int64, int64, error) {
	chatID, err := parseRequiredInt64(c.QueryParam("chat_id"))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid chat_id")
	}
	userID, err := parseRequiredInt64(c.QueryParam("user_id"))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid user_id")
	}
	return chatID, userID, nil
}

func serializeGroup(group store.Group) map[string]any {
	config := json.RawMessage("{}")
	if len(group.Config) > 0 {
		config = json.RawMessage(group.Config)
	}
	return map[string]any{
		"id":           group.ID,
		"chat_id":      group.ChatID,
		"title":        group.Title,
		"type":         group.Type,
		"member_count": group.MemberCount,
		"enabled":      group.Enabled,
		"joined_at":    group.JoinedAt,
		"config":       config,
	}
}

func serializeAuthorizedGroup(group store.AuthorizedGroup) map[string]any {
	return map[string]any{
		"chat_id":       group.ChatID,
		"title":         group.Title,
		"authorized_at": group.AuthorizedAt,
		"authorized_by": group.AuthorizedBy,
		"enabled":       group.Enabled,
		"notes":         group.Notes,
	}
}

func serializeSystemState(state store.SystemState) map[string]any {
	return map[string]any{
		"id":               state.ID,
		"ai_paused":        state.AIPaused,
		"actions_paused":   state.ActionsPaused,
		"frozen":           state.Frozen,
		"ai_paused_reason": state.AIPausedReason,
		"updated_at":       state.UpdatedAt,
		"updated_by":       state.UpdatedBy,
	}
}

func mustRawJSON(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

func serializeAudit(item store.ConfigAudit) map[string]any {
	return map[string]any{
		"id":         item.ID,
		"scope":      item.Scope,
		"chat_id":    item.ChatID,
		"admin_id":   item.AdminID,
		"action":     item.Action,
		"before":     decodeAuditValue(item.Before),
		"after":      decodeAuditValue(item.After),
		"diff":       item.Diff,
		"created_at": item.CreatedAt,
	}
}

func serializeAIDecision(item store.AIDecision) map[string]any {
	return map[string]any{
		"id":             item.ID,
		"chat_id":        item.ChatID,
		"user_id":        item.UserID,
		"message_id":     item.MessageID,
		"message_text":   item.MessageText,
		"model":          item.Model,
		"prompt_version": item.PromptVersion,
		"verdict":        item.Verdict,
		"confidence":     item.Confidence,
		"category":       item.Category,
		"reason":         item.Reason,
		"action_taken":   item.ActionTaken,
		"admin_override": item.AdminOverride,
		"latency_ms":     item.LatencyMs,
		"scene":          item.Scene,
		"created_at":     item.CreatedAt,
	}
}

func serializeUserTrust(item store.UserTrust) map[string]any {
	var bannedReason any
	if len(item.BannedReason) > 0 {
		bannedReason = json.RawMessage(item.BannedReason)
	}
	return map[string]any{
		"chat_id":          item.ChatID,
		"user_id":          item.UserID,
		"username":         item.Username,
		"first_name":       item.FirstName,
		"last_name":        item.LastName,
		"joined_at":        item.JoinedAt,
		"updated_at":       item.UpdatedAt,
		"status":           item.Status,
		"score":            item.Score,
		"messages_checked": item.MessagesChecked,
		"messages_clean":   item.MessagesClean,
		"graduated_at":     item.GraduatedAt,
		"banned_at":        item.BannedAt,
		"banned_reason":    bannedReason,
		"notes":            item.Notes,
	}
}

func decodeAuditValue(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	return value
}

func decodeGroupScope(raw []byte) []int64 {
	if len(raw) == 0 {
		return []int64{}
	}
	var scope []int64
	if err := json.Unmarshal(raw, &scope); err != nil {
		return []int64{}
	}
	if scope == nil {
		return []int64{}
	}
	return scope
}

func stringPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func marshalGroupScope(scope []int64) []byte {
	if scope == nil {
		scope = []int64{}
	}
	raw, _ := json.Marshal(scope)
	return raw
}

func adminHasGlobalAccess(admin store.Admin) bool {
	return admin.Role == "owner" || len(decodeGroupScope(admin.GroupScope)) == 0
}

func adminCanAccessChat(admin store.Admin, chatID int64) bool {
	if admin.Role == "owner" {
		return true
	}
	scope := decodeGroupScope(admin.GroupScope)
	if len(scope) == 0 {
		return true
	}
	for _, allowed := range scope {
		if allowed == chatID {
			return true
		}
	}
	return false
}

func adminScopeFilter(admin store.Admin) ([]int64, bool) {
	if adminHasGlobalAccess(admin) {
		// Keep a typed, non-nil slice for pgx array parameters even when scope is global.
		// The SQL short-circuits on scope_global=true, but pgx still has to encode
		// the array argument for $N::BIGINT[]. A nil slice can fail at runtime.
		return []int64{}, true
	}
	return decodeGroupScope(admin.GroupScope), false
}

// intersectGroupScopes returns the chat IDs present in both scopes, preserving
// the order of b and dropping duplicates.
func intersectGroupScopes(a, b []int64) []int64 {
	allowed := make(map[int64]struct{}, len(a))
	for _, chatID := range a {
		allowed[chatID] = struct{}{}
	}
	result := make([]int64, 0, len(b))
	seen := make(map[int64]struct{}, len(b))
	for _, chatID := range b {
		if _, ok := allowed[chatID]; !ok {
			continue
		}
		if _, dup := seen[chatID]; dup {
			continue
		}
		seen[chatID] = struct{}{}
		result = append(result, chatID)
	}
	return result
}

// adminVisibleToViewer decides whether target belongs in viewer's admin roster.
// It only narrows disclosure; it does not change any authorization decision.
//
//   - Global viewers (owner, or an admin whose group_scope is empty, which this
//     project treats as "no restriction") keep seeing the full roster.
//   - A scope-restricted admin sees itself, every global admin/owner (they can
//     act on its groups, so it must know who they are), and every other
//     restricted admin whose scope overlaps its own.
//   - Restricted admins with no overlapping group are hidden entirely.
func adminVisibleToViewer(viewer, target store.Admin) bool {
	if adminHasGlobalAccess(viewer) {
		return true
	}
	if viewer.ID == target.ID {
		return true
	}
	if adminHasGlobalAccess(target) {
		return true
	}
	viewerScope := decodeGroupScope(viewer.GroupScope)
	targetScope := decodeGroupScope(target.GroupScope)
	return len(intersectGroupScopes(viewerScope, targetScope)) > 0
}

// serializeAdminForViewer renders a visible admin, redacting the fields a
// scope-restricted viewer has no need for. Global viewers and the viewer's own
// record keep the unchanged payload.
func serializeAdminForViewer(viewer, target store.Admin) map[string]any {
	item := serializeAdmin(target)
	if adminHasGlobalAccess(viewer) || viewer.ID == target.ID {
		return item
	}
	// notes are free-form operator annotations about another person; a restricted
	// admin only needs to know who can act on its groups, not why they exist.
	item["notes"] = nil
	// Never disclose chat IDs the viewer has no access to. Global targets keep an
	// empty scope so the UI still renders them as unrestricted.
	if !adminHasGlobalAccess(target) {
		item["group_scope"] = intersectGroupScopes(decodeGroupScope(viewer.GroupScope), decodeGroupScope(target.GroupScope))
	}
	return item
}

func filterAuditByScope(admin store.Admin, items []store.ConfigAudit) []store.ConfigAudit {
	filtered := make([]store.ConfigAudit, 0, len(items))
	for _, item := range items {
		if item.ChatID == nil {
			if adminHasGlobalAccess(admin) {
				filtered = append(filtered, item)
			}
			continue
		}
		if adminCanAccessChat(admin, *item.ChatID) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func normalizeRequestedGroupScope(current store.Admin, requested *[]int64) ([]int64, bool) {
	if current.Role == "owner" {
		return requestedOrEmpty(requested), true
	}
	currentScope := decodeGroupScope(current.GroupScope)
	if len(currentScope) == 0 {
		return requestedOrEmpty(requested), true
	}
	if requested == nil || len(*requested) == 0 {
		return currentScope, true
	}

	allowed := make(map[int64]struct{}, len(currentScope))
	for _, chatID := range currentScope {
		allowed[chatID] = struct{}{}
	}
	for _, chatID := range *requested {
		if _, ok := allowed[chatID]; !ok {
			return nil, false
		}
	}
	return *requested, true
}

func requestedOrEmpty(scope *[]int64) []int64 {
	if scope == nil {
		return []int64{}
	}
	return *scope
}

func trimStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func serializeViolation(v store.Violation) map[string]any {
	return map[string]any{
		"id":           v.ID,
		"chat_id":      v.ChatID,
		"user_id":      v.UserID,
		"username":     v.Username,
		"rule":         v.Rule,
		"matched":      v.Matched,
		"action":       v.Action,
		"message_text": v.MessageText,
		"created_at":   v.CreatedAt,
	}
}

func truncateProfileCheckLogBio(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := *value
	if utf8.RuneCountInString(trimmed) <= 200 {
		return &trimmed
	}
	trimmed = string([]rune(trimmed)[:200]) + "…"
	return &trimmed
}

func serializeProfileCheckLog(item store.ProfileCheckLog) map[string]any {
	return map[string]any{
		"id":            item.ID,
		"chat_id":       item.ChatID,
		"user_id":       item.UserID,
		"user_name":     item.UserName,
		"username":      item.Username,
		"bio":           truncateProfileCheckLogBio(item.Bio),
		"check_mode":    item.CheckMode,
		"result":        item.Result,
		"matched_rule":  item.MatchedRule,
		"ai_confidence": item.AiConfidence,
		"ai_verdict":    item.AiVerdict,
		"created_at":    item.CreatedAt,
	}
}

func serializeWarning(w store.Warning) map[string]any {
	return map[string]any{
		"id":          w.ID,
		"chat_id":     w.ChatID,
		"user_id":     w.UserID,
		"reason":      w.Reason,
		"issued_by":   w.IssuedBy,
		"created_at":  w.CreatedAt,
		"consumed_at": w.ConsumedAt,
	}
}

func serializeStats(s store.AdminStats) map[string]any {
	return map[string]any{
		"groups_count":         s.GroupsCount,
		"active_verifications": s.ActiveVerifications,
		"today_violations":     s.TodayViolations,
	}
}
