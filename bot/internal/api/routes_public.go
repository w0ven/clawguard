package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/store"
)

type magicExchangeRequest struct {
	Token string `json:"token"`
}

type magicExchangePayload struct {
	TelegramID  int64  `json:"telegram_id"`
	ScopeChatID int64  `json:"scope_chat_id"`
	CreatedAt   string `json:"created_at"`
}

func (s *Server) registerPublicRoutes() {
	s.echo.POST("/api/public/magic/exchange", s.handleMagicExchange)
}

func (s *Server) handleMagicExchange(c echo.Context) error {
	var body magicExchangeRequest
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json body"})
	}
	body.Token = strings.TrimSpace(body.Token)
	if body.Token == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "token required"})
	}

	ctx := c.Request().Context()
	key := "clawguard:magic:" + body.Token
	tx := s.botService.Redis().TxPipeline()
	getCmd := tx.Get(ctx, key)
	tx.Del(ctx, key)
	_, err := tx.Exec(ctx)
	if err != nil {
		if err == redis.Nil || getCmd.Err() == redis.Nil {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid token"})
		}
		s.logger.Warn("magic token exchange failed", zap.Error(err))
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "token exchange failed"})
	}
	raw, err := getCmd.Result()
	if err != nil {
		if err == redis.Nil {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid token"})
		}
		s.logger.Warn("magic token read failed", zap.Error(err))
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "token exchange failed"})
	}

	var payload magicExchangePayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid token"})
	}

	admin, err := s.botService.Queries().GetAdminByTelegramID(ctx, payload.TelegramID)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid token"})
	}
	if payload.ScopeChatID != 0 {
		authorized, err := s.botService.IsAuthorizedGroup(ctx, payload.ScopeChatID)
		if err != nil {
			s.logger.Warn("magic token authorized group check failed", zap.Error(err), zap.Int64("chat_id", payload.ScopeChatID))
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "token exchange failed"})
		}
		if !authorized || !adminCanAccessChat(admin, payload.ScopeChatID) {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
		}
	}

	tokenString, err := s.signAdminJWT(admin)
	if err != nil {
		s.logger.Warn("sign magic login jwt failed", zap.Error(err), zap.Int64("admin_id", admin.ID))
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "sign token failed"})
	}
	csrfToken, err := newCSRFToken()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "csrf token failed"})
	}

	redirectTo := "/dashboard"
	if payload.ScopeChatID != 0 {
		redirectTo = fmt.Sprintf("/groups/%d", payload.ScopeChatID)
	}

	diff := "expires in 60min"
	if _, err := s.botService.Queries().InsertAuditEntry(ctx, store.InsertAuditEntryParams{
		Scope:   "magic_link",
		ChatID:  chatIDPtr(payload.ScopeChatID),
		AdminID: admin.ID,
		Action:  "magic_token_exchanged",
		Before:  mustRawJSON(payload),
		After:   mustRawJSON(map[string]any{"redirect_to": redirectTo}),
		Diff:    &diff,
	}); err != nil {
		s.logger.Warn("write magic token exchange audit failed", zap.Error(err), zap.Int64("admin_id", admin.ID))
	}

	return c.JSON(http.StatusOK, map[string]any{
		"jwt":         tokenString,
		"csrf_token":  csrfToken,
		"redirect_to": redirectTo,
	})
}

func chatIDPtr(chatID int64) *int64 {
	if chatID == 0 {
		return nil
	}
	return &chatID
}

func scopeChatIDPtr(chatID int64) *int64 {
	return chatIDPtr(chatID)
}
