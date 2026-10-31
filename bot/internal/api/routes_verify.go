package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/auth"
	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/redact"
	"github.com/openclaw/clawguard/internal/store"
)

func (s *Server) registerVerifyRoutes() {
	s.echo.GET("/api/verify/capabilities", s.handleVerifyCapabilities, s.apiRateLimit(s.ipRateLimitKey("verify:capabilities_ip"), turnstileVerifyIPLimit, apiRateLimitWindow))
	s.echo.POST("/api/verify/mini/config", s.handleDualCaptchaConfig, s.apiRateLimit(s.ipRateLimitKey("verify:mini_ip"), turnstileVerifyIPLimit, apiRateLimitWindow))
	s.echo.POST("/api/verify/mini/submit", s.handleDualCaptcha, s.apiRateLimit(s.ipRateLimitKey("verify:mini_ip"), turnstileVerifyIPLimit, apiRateLimitWindow))
	s.echo.POST("/api/verify/mini/recover", s.handleDualCaptcha, s.apiRateLimit(s.ipRateLimitKey("verify:mini_ip"), turnstileVerifyIPLimit, apiRateLimitWindow))
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
	var payload struct {
		Token    string `json:"token"`
		InitData string `json:"init_data"`
	}
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 20000)
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil || len(payload.Token) != 36 {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "验证已失效"})
	}
	if !s.allowDualCaptchaToken(c, payload.Token) {
		return c.JSON(http.StatusTooManyRequests, map[string]string{"error": "请求过于频繁，请稍后重试"})
	}
	pending, err := s.botService.Queries().GetActiveDualCaptcha(c.Request().Context(), payload.Token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "验证已失效"})
		}
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "验证暂不可用"})
	}
	var mode struct {
		MiniApp bool `json:"miniapp"`
	}
	if json.Unmarshal(pending.Payload, &mode) != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "验证已失效"})
	}
	mini := c.Path() == "/api/verify/mini/config"
	if mode.MiniApp != mini {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "验证已失效"})
	}
	result := map[string]any{"turnstile_sitekey": s.cfg.TurnstileSiteKey, "hcaptcha_sitekey": s.cfg.HCaptchaSiteKey}
	if mini {
		identity, err := auth.VerifyWebAppData(payload.InitData, s.cfg.BotToken, time.Now(), 10*time.Minute)
		if errors.Is(err, auth.ErrWebAppDataExpired) {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "身份已过期，请关闭页面并从机器人私聊重新打开。"})
		}
		if err != nil || identity.UserID != pending.UserID {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "无法验证当前账号，请本人从机器人私聊打开。"})
		}
		group, err := s.botService.Queries().GetGroupByChatID(c.Request().Context(), pending.ChatID)
		if err != nil {
			return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "验证暂不可用"})
		}
		result["group_title"] = group.Title
		result["user_id"] = pending.UserID
		result["username"] = pending.Username
		result["expires_at"] = pending.ExpiresAt
		state, err := s.botService.Queries().DualCaptchaState(c.Request().Context(), pending.ID)
		if err != nil {
			return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "验证暂不可用"})
		}
		if state == "conflict" {
			return c.JSON(http.StatusConflict, map[string]string{"error": "验证状态需要核查，请联系群管理员。"})
		}
		result["recovery_pending"] = state == "verified" || state == "applying"
		remaining := time.Until(pending.ExpiresAt).Milliseconds()
		if remaining < 0 {
			remaining = 0
		}
		result["remaining_ms"] = remaining
	}
	if !s.cfg.DualCaptchaConfigured() && result["recovery_pending"] != true {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "双重验证未配置"})
	}
	return c.JSON(http.StatusOK, result)
}

func (s *Server) handleDualCaptcha(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	req := c.Request()
	req.Body = http.MaxBytesReader(c.Response(), req.Body, 36000)
	var payload struct {
		Token      string `json:"token"`
		CFResponse string `json:"cf_response"`
		HResponse  string `json:"h_response"`
		InitData   string `json:"init_data"`
	}
	recoverOnly := c.Path() == "/api/verify/mini/recover"
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil || len(payload.Token) != 36 || (!recoverOnly && (strings.TrimSpace(payload.CFResponse) == "" || strings.TrimSpace(payload.HResponse) == "")) || len(payload.CFResponse) > 2048 || len(payload.HResponse) > 8192 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "请完成并重新提交两项验证"})
	}
	if !s.allowDualCaptchaToken(c, payload.Token) {
		return c.JSON(http.StatusTooManyRequests, map[string]string{"error": "请求过于频繁，请稍后重试"})
	}
	var err error
	if c.Path() == "/api/verify/mini/submit" || recoverOnly {
		identity, identityErr := auth.VerifyWebAppData(payload.InitData, s.cfg.BotToken, time.Now(), 10*time.Minute)
		if errors.Is(identityErr, auth.ErrWebAppDataExpired) {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "身份已过期，请关闭页面并从机器人私聊重新打开。"})
		}
		if identityErr != nil {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "无法验证当前账号，请本人从机器人私聊打开。"})
		}
		if recoverOnly {
			err = s.botService.RecoverMiniAppDualCaptcha(req.Context(), payload.Token, identity.UserID)
		} else {
			err = s.botService.VerifyMiniAppDualCaptcha(req.Context(), payload.Token, payload.CFResponse, payload.HResponse, c.RealIP(), identity.UserID)
		}
	} else {
		err = s.botService.VerifyDualCaptcha(req.Context(), payload.Token, payload.CFResponse, payload.HResponse, c.RealIP())
	}
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
