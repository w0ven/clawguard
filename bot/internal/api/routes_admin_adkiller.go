package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/adkiller"
	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/redact"
	"github.com/openclaw/clawguard/internal/store"
)

func (s *Server) registerAdminAdKillerRoutes(admin *echo.Group) {
	admin.GET("/adkiller", s.requireOwner(s.handleGetAdKillerSecret))
	admin.PUT("/adkiller", s.requireOwner(s.handlePutAdKillerSecret))
	admin.POST("/adkiller/test", s.requireOwner(s.handleTestAdKiller))
}

type adkillerSecretPayload struct {
	APIKey string `json:"api_key"`
	Clear  bool   `json:"clear"`
}

type adkillerTestPayload struct {
	Text string `json:"text"`
}

func serializeAdKillerSecret(secret store.AdkillerSecret) map[string]any {
	enc := strings.TrimSpace(secret.ApiKeyEnc)
	return map[string]any{
		"api_key_set":  enc != "",
		"api_key_hint": hintFromCiphertext(enc),
		"updated_at":   secret.UpdatedAt,
	}
}

func (s *Server) handleGetAdKillerSecret(c echo.Context) error {
	secret, err := s.botService.Queries().GetAdkillerSecret(c.Request().Context())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusOK, map[string]any{
				"api_key_set":  false,
				"api_key_hint": "",
			})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load adkiller secret failed"})
	}
	return c.JSON(http.StatusOK, serializeAdKillerSecret(secret))
}

func (s *Server) handlePutAdKillerSecret(c echo.Context) error {
	var payload adkillerSecretPayload
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json body"})
	}

	ctx := c.Request().Context()
	var (
		updated store.AdkillerSecret
		err     error
	)
	if payload.Clear {
		updated, err = s.botService.Queries().ClearAdkillerSecret(ctx)
	} else {
		apiKey := strings.TrimSpace(payload.APIKey)
		if apiKey == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "api_key required"})
		}
		enc, encErr := ai.EncryptAPIKey(apiKey)
		if encErr != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "encrypt api key failed"})
		}
		updated, err = s.botService.Queries().UpsertAdkillerSecret(ctx, enc)
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "save adkiller secret failed"})
	}
	if reloadErr := s.botService.ReloadAdKillerSecret(ctx); reloadErr != nil {
		s.logger.Warn("reload adkiller secret failed", zap.Error(reloadErr))
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "reload adkiller secret failed"})
	}
	return c.JSON(http.StatusOK, serializeAdKillerSecret(updated))
}

func (s *Server) handleTestAdKiller(c echo.Context) error {
	var payload adkillerTestPayload
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json body"})
	}
	text := strings.TrimSpace(payload.Text)
	if text == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "text required"})
	}

	client := s.botService.AdKiller()
	if client == nil || !client.HasAPIKey() {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "adkiller api key is not configured"})
	}

	policy, err := config.LoadPolicy(c.Request().Context(), s.botService.Queries(), 0)
	if err != nil {
		policy = config.DefaultPolicy
	}
	cfg := policy.AI.AdKiller
	timeout := time.Duration(cfg.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = adkiller.DefaultTimeout
	}

	ctx, cancel := context.WithTimeout(c.Request().Context(), timeout)
	defer cancel()
	result, scoreErr := client.Score(ctx, text)
	if scoreErr != nil {
		httpStatus := http.StatusBadGateway
		code := ""
		var apiErr *adkiller.APIError
		if errors.As(scoreErr, &apiErr) && apiErr != nil {
			httpStatus = apiErr.Status
			code = apiErr.Code
		}
		return c.JSON(http.StatusOK, map[string]any{
			"ok":          false,
			"error":       redact.ErrorString(scoreErr),
			"error_code":  code,
			"http_status": httpStatus,
			"latency_ms":  int(result.Latency.Milliseconds()),
			"retry_after": result.RetryAfter,
			"min_score":   cfg.MinScore,
			"timeout_ms":  cfg.TimeoutMs,
			"enabled":     cfg.Enabled,
		})
	}

	bands := make([]adkiller.ScoreBand, 0, len(cfg.ScoreBands))
	for _, band := range cfg.ScoreBands {
		bands = append(bands, adkiller.ScoreBand{
			MinScore: band.MinScore,
			MaxScore: band.MaxScore,
			Action:   band.Action,
		})
	}
	action := adkiller.ResolveAction(result.Score, bands)
	outcome := adkiller.ActionLabel(action)
	if action != adkiller.ActionNone {
		outcome = "确认广告，按「" + adkiller.ActionLabel(action) + "」处理"
	}
	return c.JSON(http.StatusOK, map[string]any{
		"ok":               true,
		"score":            result.Score,
		"level":            result.Level,
		"primary_category": result.PrimaryCategory,
		"mapped_category":  adkiller.MapCategory(result.PrimaryCategory),
		"confirmed_ad":     action != adkiller.ActionNone,
		"action":           action,
		"outcome":          outcome,
		"min_score":        cfg.MinScore,
		"timeout_ms":       cfg.TimeoutMs,
		"enabled":          cfg.Enabled,
		"enabled_chat_ids": cfg.EnabledChatIDs,
		"score_bands":      cfg.ScoreBands,
		"truncated":        result.Truncated,
		"latency_ms":       int(result.Latency.Milliseconds()),
		"rate_remaining":   result.RateRemaining,
		"dimensions":       result.Dimensions,
	})
}
