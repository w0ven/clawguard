package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/redact"
	"github.com/openclaw/clawguard/internal/store"
)

func (s *Server) registerVerifyRoutes() {
	s.echo.GET("/api/verify/capabilities", s.handleVerifyCapabilities, s.apiRateLimit(s.ipRateLimitKey("verify:capabilities_ip"), turnstileVerifyIPLimit, apiRateLimitWindow))
	s.echo.POST("/api/verify/dual/config", s.handleDualCaptchaConfig,
		s.apiRateLimit(s.ipRateLimitKey("verify:dual_ip"), turnstileVerifyIPLimit, apiRateLimitWindow))
	s.echo.POST("/api/verify/dual/submit", s.handleDualCaptcha,
		s.apiRateLimit(s.ipRateLimitKey("verify:dual_ip"), turnstileVerifyIPLimit, apiRateLimitWindow))
	s.echo.POST(
		"/api/verify/turnstile/:token",
		s.handleVerifyTurnstile,
		s.apiRateLimit(s.ipRateLimitKey("verify:turnstile_ip"), turnstileVerifyIPLimit, apiRateLimitWindow),
		s.apiRateLimit(s.turnstileTokenRateLimitKey, turnstileVerifyTokenLimit, apiRateLimitWindow),
	)
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
			return c.JSON(http.StatusBadRequest, map[string]string{"error": redact.ErrorString(err)})
		case errors.Is(err, bot.ErrTurnstileNotConfigured):
			return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "turnstile not configured"})
		default:
			s.logger.Error("verify turnstile token", zap.Error(err))
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "verification failed"})
		}
	}

	return c.JSON(http.StatusOK, map[string]bool{"success": true})
}

func dualCaptchaRateHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Server) allowDualCaptchaToken(c echo.Context, token string) bool {
	key := s.rateLimitKey("verify:dual_token", dualCaptchaRateHash(token))
	allowed, _ := newRateLimiter(s.redisClient(), s.logger).Allow(c.Request().Context(), key, turnstileVerifyTokenLimit, apiRateLimitWindow)
	return allowed
}

func (s *Server) handleVerifyCapabilities(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, map[string]bool{"dual_captcha_available": s.cfg.DualCaptchaConfigured()})
}

func (s *Server) handleDualCaptchaConfig(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	if !s.cfg.DualCaptchaConfigured() {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "双重验证未配置"})
	}
	var payload struct {
		Token string `json:"token"`
	}
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 512)
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil || len(payload.Token) != 36 {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "验证已失效"})
	}
	if !s.allowDualCaptchaToken(c, payload.Token) {
		return c.JSON(http.StatusTooManyRequests, map[string]string{"error": "请求过于频繁，请稍后重试"})
	}
	if _, err := s.botService.Queries().GetActiveDualCaptcha(c.Request().Context(), payload.Token); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "验证已失效"})
		}
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "验证暂不可用"})
	}
	return c.JSON(http.StatusOK, map[string]string{"turnstile_sitekey": s.cfg.TurnstileSiteKey, "hcaptcha_sitekey": s.cfg.HCaptchaSiteKey})
}

func (s *Server) handleDualCaptcha(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	req := c.Request()
	req.Body = http.MaxBytesReader(c.Response(), req.Body, 16384)
	var payload struct {
		Token      string `json:"token"`
		CFResponse string `json:"cf_response"`
		HResponse  string `json:"h_response"`
	}
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil || len(payload.Token) != 36 || strings.TrimSpace(payload.CFResponse) == "" || strings.TrimSpace(payload.HResponse) == "" || len(payload.CFResponse) > 2048 || len(payload.HResponse) > 8192 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "请完成并重新提交两项验证"})
	}
	if !s.allowDualCaptchaToken(c, payload.Token) {
		return c.JSON(http.StatusTooManyRequests, map[string]string{"error": "请求过于频繁，请稍后重试"})
	}
	err := s.botService.VerifyDualCaptcha(req.Context(), payload.Token, payload.CFResponse, payload.HResponse, c.RealIP())
	switch {
	case err == nil:
		return c.JSON(http.StatusOK, map[string]bool{"success": true})
	case errors.Is(err, pgx.ErrNoRows):
		return c.JSON(http.StatusNotFound, map[string]string{"error": "验证已失效"})
	case errors.Is(err, store.ErrDualCaptchaBusy):
		return c.JSON(http.StatusConflict, map[string]string{"error": "验证正在处理，请稍后重试"})
	case errors.Is(err, bot.ErrDualCaptchaConflict):
		return c.JSON(http.StatusConflict, map[string]string{"error": "验证状态冲突，Telegram 权限可能已变化，请联系管理员核查"})
	case errors.Is(err, bot.ErrDualCaptchaUnavailable):
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "验证暂不可用，请刷新两项后重试"})
	default:
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "验证失败，请刷新两项后重试"})
	}
}
