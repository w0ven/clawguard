package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	telegramLoginIPLimit      = 30
	telegramLoginUserLimit    = 10
	logoutIPLimit             = 60
	turnstileVerifyIPLimit    = 20
	turnstileVerifyTokenLimit = 5
	adminBanLimit             = 30
	adminWriteLimit           = 120

	apiRateLimitWindow = time.Minute // minutes: all API rate limit counts are per rolling Redis TTL window.
)

type rateLimiter struct {
	redis  redis.Cmdable
	logger *zap.Logger
}

func newRateLimiter(redisClient redis.Cmdable, logger *zap.Logger) rateLimiter {
	if logger == nil {
		logger = zap.NewNop()
	}
	return rateLimiter{redis: redisClient, logger: logger}
}

func (r rateLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	key = strings.TrimSpace(key)
	if key == "" || limit <= 0 || window <= 0 {
		return true, nil
	}
	if r.redis == nil {
		r.logger.Warn("api rate limit redis unavailable; failing open", zap.String("key", key))
		return true, nil
	}

	count, err := r.redis.Incr(ctx, key).Result()
	if err != nil {
		r.logger.Warn("api rate limit incr failed; failing open", zap.String("key", key), zap.Error(err))
		return true, err
	}
	if count == 1 {
		if err := r.redis.Expire(ctx, key, window).Err(); err != nil {
			r.logger.Warn("api rate limit expire failed; failing open", zap.String("key", key), zap.Error(err))
			return true, err
		}
	}
	return count <= int64(limit), nil
}

func (r rateLimiter) retryAfter(ctx context.Context, key string, fallback time.Duration) int {
	seconds := int((fallback + time.Second - 1) / time.Second)
	if r.redis == nil || strings.TrimSpace(key) == "" {
		return seconds
	}
	ttl, err := r.redis.TTL(ctx, key).Result()
	if err != nil || ttl <= 0 {
		return seconds
	}
	return int((ttl + time.Second - 1) / time.Second)
}

func (r rateLimiter) middleware(keyFn func(echo.Context) string, limit int, window time.Duration) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			key := keyFn(c)
			allowed, _ := r.Allow(c.Request().Context(), key, limit, window)
			if allowed {
				return next(c)
			}
			retryAfter := r.retryAfter(c.Request().Context(), key, window)
			c.Response().Header().Set(echo.HeaderRetryAfter, strconv.Itoa(retryAfter))
			return c.JSON(http.StatusTooManyRequests, map[string]string{"error": "rate limited"})
		}
	}
}

func (s *Server) apiRateLimit(keyFn func(echo.Context) string, limit int, window time.Duration) echo.MiddlewareFunc {
	return newRateLimiter(s.redisClient(), s.logger).middleware(keyFn, limit, window)
}

func (s *Server) redisClient() redis.Cmdable {
	if s == nil || s.botService == nil {
		return nil
	}
	return s.botService.Redis()
}

func (s *Server) rateLimitKey(prefix string, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return "api_rate_limit:" + prefix + ":" + value
}

func (s *Server) ipRateLimitKey(prefix string) func(echo.Context) string {
	return func(c echo.Context) string {
		return s.rateLimitKey(prefix, c.RealIP())
	}
}

func (s *Server) telegramLoginUserRateLimitKey(c echo.Context) string {
	id := strings.TrimSpace(c.FormValue("id"))
	if id == "" {
		id = strings.TrimSpace(c.QueryParam("id"))
	}
	return s.rateLimitKey("auth:telegram_user", id)
}

func (s *Server) turnstileTokenRateLimitKey(c echo.Context) string {
	return s.rateLimitKey("verify:turnstile_token", c.Param("token"))
}

func (s *Server) adminRateLimitKey(prefix string) func(echo.Context) string {
	return func(c echo.Context) string {
		admin, ok := currentAdmin(c)
		if !ok {
			return ""
		}
		return s.rateLimitKey(prefix, strconv.FormatInt(admin.ID, 10))
	}
}

func (s *Server) adminWriteRateLimit() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			switch c.Request().Method {
			case http.MethodPost, http.MethodPut, http.MethodDelete:
			default:
				return next(c)
			}
			path := c.Path()
			if path == "/api/admin/ban" || path == "/api/admin/unban" {
				return next(c)
			}
			return s.apiRateLimit(s.adminRateLimitKey("admin:write"), adminWriteLimit, apiRateLimitWindow)(next)(c)
		}
	}
}
