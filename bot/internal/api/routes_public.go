package api

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func (s *Server) registerPublicRoutes() {
	s.echo.POST("/api/public/magic/exchange", s.handleMagicExchangeDisabled, middleware.BodyLimit("64K"), s.apiRateLimit(s.ipRateLimitKey("auth:magic_exchange_ip"), magicExchangeIPLimit, apiRateLimitWindow))
}

func (s *Server) handleMagicExchangeDisabled(c echo.Context) error {
	return c.JSON(http.StatusGone, map[string]string{
		"error": "一次性明文登录链接已停用，请通过 Telegram 客户端确认登录或从 Bot 内管理面板进入",
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
