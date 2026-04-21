package api

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/bot"
)

func (s *Server) registerVerifyRoutes() {
	s.echo.POST("/api/verify/turnstile/:token", s.handleVerifyTurnstile)
}

func (s *Server) handleVerifyTurnstile(c echo.Context) error {
	token := c.Param("token")
	if token == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing token"})
	}

	var payload struct {
		CFResponse string `json:"cf_response"`
	}
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json body"})
	}
	if payload.CFResponse == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing cf_response"})
	}

	if err := s.botService.VerifyTurnstileToken(c.Request().Context(), token, payload.CFResponse, c.RealIP()); err != nil {
		switch {
		case errors.Is(err, bot.ErrTurnstileTokenNotFound):
			return c.JSON(http.StatusNotFound, map[string]string{"error": "verification token not found or already used"})
		case errors.Is(err, bot.ErrTurnstileVerifyFailed):
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		case errors.Is(err, bot.ErrTurnstileNotConfigured):
			return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "turnstile not configured"})
		default:
			s.logger.Error("verify turnstile token", zap.Error(err))
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "verification failed"})
		}
	}

	return c.JSON(http.StatusOK, map[string]bool{"success": true})
}
