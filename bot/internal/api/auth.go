package api

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/store"
)

const (
	adminCookieName     = "cg_admin"
	csrfCookieName      = "cg_csrf"
	loginPollCookieName = "cg_login_poll"
	adminContextKey     = "cg_admin_actor"

	telegramMiniAppMaxAge   = 5 * time.Minute
	webLoginChallengeTTL    = 5 * time.Minute
	webLoginChallengeCodeCh = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

	// Official Telegram Production Ed25519 public key for Mini App third-party signature verification.
	// Unlike BOT_TOKEN HMAC, the private key is held exclusively by Telegram's servers;
	// possessing BOT_TOKEN never enables offline forgery of an admin login payload.
	telegramWebAppEd25519PubHex = "e7bf03a2fa4602af4580703d88dda5bb59f32ed8b02a56c187fe7d34caed242d"
)

func defaultTelegramWebAppPublicKey() ed25519.PublicKey {
	raw, err := hex.DecodeString(telegramWebAppEd25519PubHex)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		panic("invalid embedded Telegram WebApp Ed25519 public key")
	}
	return ed25519.PublicKey(raw)
}

type adminClaims struct {
	Role string `json:"role"`
	TgID int64  `json:"tg_id"`
	jwt.RegisteredClaims
}

type telegramMiniAppRequest struct {
	InitData string `json:"init_data"`
}

type telegramMiniAppUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
	PhotoURL  string `json:"photo_url"`
}

type webLoginChallengeRecord struct {
	ChallengeID    string `json:"challenge_id"`
	Code           string `json:"code"`
	PollSecretHash string `json:"poll_secret_hash"`
	RequesterIP    string `json:"requester_ip"`
	Status         string `json:"status"`
	AdminID        int64  `json:"admin_id,omitempty"`
	ApproverIP     string `json:"approver_ip,omitempty"`
	CreatedAt      int64  `json:"created_at"`
}

func (s *Server) registerAuthRoutes() {
	s.echo.GET("/api/auth/telegram-login", s.handleTelegramLoginDisabled, s.apiRateLimit(s.ipRateLimitKey("auth:telegram_login_ip"), telegramLoginIPLimit, apiRateLimitWindow), s.apiRateLimit(s.telegramLoginUserRateLimitKey, telegramLoginUserLimit, apiRateLimitWindow))
	s.echo.POST("/api/auth/telegram-login", s.handleTelegramLoginDisabled, middleware.BodyLimit("64K"), s.apiRateLimit(s.ipRateLimitKey("auth:telegram_login_ip"), telegramLoginIPLimit, apiRateLimitWindow), s.apiRateLimit(s.telegramLoginUserRateLimitKey, telegramLoginUserLimit, apiRateLimitWindow))
	s.echo.POST("/api/auth/miniapp", s.handleTelegramMiniApp, middleware.BodyLimit("64K"), s.apiRateLimit(s.ipRateLimitKey("auth:miniapp_ip"), telegramLoginIPLimit, apiRateLimitWindow))
	s.echo.POST("/api/auth/web-challenge", s.handleCreateWebLoginChallenge, middleware.BodyLimit("4K"), s.apiRateLimit(s.ipRateLimitKey("auth:web_challenge_ip"), magicExchangeIPLimit, apiRateLimitWindow))
	s.echo.POST("/api/auth/web-approve", s.handleApproveWebLoginChallenge, middleware.BodyLimit("64K"), s.apiRateLimit(s.ipRateLimitKey("auth:web_approve_ip"), telegramLoginIPLimit, apiRateLimitWindow))
	s.echo.POST("/api/auth/web-poll", s.handlePollWebLoginChallenge, middleware.BodyLimit("4K"), s.apiRateLimit(s.ipRateLimitKey("auth:web_poll_ip"), logoutIPLimit, apiRateLimitWindow))
	s.echo.POST("/api/auth/logout", s.handleLogout, s.apiRateLimit(s.ipRateLimitKey("auth:logout_ip"), logoutIPLimit, apiRateLimitWindow))
	s.echo.GET("/api/auth/me", s.requireAdminJWT(s.handleMe))
}

func (s *Server) handleTelegramLoginDisabled(c echo.Context) error {
	return c.JSON(http.StatusForbidden, map[string]string{
		"error": "Telegram Login Widget 单因素登录已停用（防止 Bot Token 泄露被离线冒充），请通过 Telegram 客户端确认登录或从 Bot 内管理面板打开",
	})
}

func (s *Server) authenticateVerifiedMiniAppAdmin(ctx context.Context, rawInitData string) (store.Admin, error) {
	pubKey := s.webAppPubKey
	if len(pubKey) != ed25519.PublicKeySize {
		pubKey = defaultTelegramWebAppPublicKey()
	}
	user, sigDigest, err := validateTelegramMiniAppEd25519(rawInitData, s.cfg.BotToken, pubKey, time.Now())
	if err != nil {
		return store.Admin{}, &echo.HTTPError{Code: http.StatusUnauthorized, Message: "invalid mini app authorization"}
	}
	if err := s.consumeMiniAppSignatureOnce(ctx, sigDigest); err != nil {
		return store.Admin{}, err
	}

	queries := s.botService.Queries()
	if _, err := queries.GetAdminByTelegramID(ctx, user.ID); err != nil {
		return store.Admin{}, &echo.HTTPError{Code: http.StatusForbidden, Message: "admin not allowed"}
	}
	admin, err := queries.TouchAdminLogin(ctx, store.TouchAdminLoginParams{
		TelegramID: user.ID,
		Username:   stringPtr(user.Username),
		FirstName:  stringPtr(strings.TrimSpace(user.FirstName + " " + user.LastName)),
		PhotoURL:   stringPtr(user.PhotoURL),
	})
	if err != nil {
		return store.Admin{}, &echo.HTTPError{Code: http.StatusInternalServerError, Message: "update admin failed"}
	}
	return admin, nil
}

func (s *Server) consumeMiniAppSignatureOnce(ctx context.Context, sigDigest string) error {
	rdb := s.redisClient()
	if rdb == nil || sigDigest == "" {
		return &echo.HTTPError{Code: http.StatusServiceUnavailable, Message: "authentication replay store unavailable"}
	}
	ok, err := rdb.SetNX(ctx, "clawguard:miniapp_sig:"+sigDigest, "1", telegramMiniAppMaxAge+time.Minute).Result()
	if err != nil {
		s.logger.Warn("record miniapp signature replay key failed", zap.Error(err))
		return &echo.HTTPError{Code: http.StatusServiceUnavailable, Message: "authentication check failed"}
	}
	if !ok {
		return &echo.HTTPError{Code: http.StatusUnauthorized, Message: "mini app authorization already used"}
	}
	return nil
}

func writeAuthHTTPError(c echo.Context, err error) error {
	if he, ok := err.(*echo.HTTPError); ok {
		msg, _ := he.Message.(string)
		if msg == "" {
			msg = http.StatusText(he.Code)
		}
		return c.JSON(he.Code, map[string]string{"error": msg})
	}
	return c.JSON(http.StatusUnauthorized, map[string]string{"error": "authentication failed"})
}

func (s *Server) handleTelegramMiniApp(c echo.Context) error {
	var body telegramMiniAppRequest
	if err := c.Bind(&body); err != nil || len(body.InitData) > 8192 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid init data"})
	}
	admin, err := s.authenticateVerifiedMiniAppAdmin(c.Request().Context(), body.InitData)
	if err != nil {
		return writeAuthHTTPError(c, err)
	}

	tokenString, err := s.signAdminJWT(admin)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "sign token failed"})
	}
	csrfToken, err := newCSRFToken()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "csrf token failed"})
	}
	setAdminCookie(c, tokenString)
	setCSRFCookie(c, csrfToken)
	s.recordAdminLoginAudit(c, admin, "miniapp_login", nil)
	return c.JSON(http.StatusOK, map[string]any{"admin": serializeAdmin(admin), "csrf_token": csrfToken, "redirect_to": "/dashboard"})
}

func (s *Server) handleCreateWebLoginChallenge(c echo.Context) error {
	rdb := s.redisClient()
	if rdb == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "login service unavailable"})
	}

	challengeID, err := randomHexToken(16)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "create challenge failed"})
	}
	pollSecret, err := newCSRFToken()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "create challenge failed"})
	}

	ctx := c.Request().Context()
	var code string
	for attempt := 0; attempt < 5; attempt++ {
		candidate, genErr := generateVerificationCode(6)
		if genErr != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "create challenge failed"})
		}
		reserved, setErr := rdb.SetNX(ctx, webLoginCodeKey(candidate), challengeID, webLoginChallengeTTL).Result()
		if setErr != nil {
			return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "store challenge failed"})
		}
		if reserved {
			code = candidate
			break
		}
	}
	if code == "" {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "allocate challenge code failed"})
	}

	rec := webLoginChallengeRecord{
		ChallengeID:    challengeID,
		Code:           code,
		PollSecretHash: sha256HexString(pollSecret),
		RequesterIP:    c.RealIP(),
		Status:         "pending",
		CreatedAt:      time.Now().Unix(),
	}
	raw, _ := json.Marshal(rec)
	if err := rdb.Set(ctx, webLoginChallengeKey(challengeID), raw, webLoginChallengeTTL).Err(); err != nil {
		_ = rdb.Del(ctx, webLoginCodeKey(code)).Err()
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "store challenge failed"})
	}

	setLoginPollCookie(c, pollSecret)
	botUsername := strings.TrimPrefix(strings.TrimSpace(s.cfg.BotUsername), "@")
	var deepLink string
	if botUsername != "" {
		deepLink = fmt.Sprintf("https://t.me/%s?start=weblogin_%s", botUsername, code)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"challenge_id": challengeID,
		"code":         code,
		"bot_username": botUsername,
		"tg_deep_link": deepLink,
		"expires_in":   int(webLoginChallengeTTL.Seconds()),
	})
}

func (s *Server) handleApproveWebLoginChallenge(c echo.Context) error {
	var body struct {
		Code     string `json:"code"`
		InitData string `json:"init_data"`
	}
	if err := c.Bind(&body); err != nil || len(body.InitData) > 8192 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	code := normalizeLoginCode(body.Code)
	if len(code) != 6 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid verification code"})
	}

	rdb := s.redisClient()
	if rdb == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "login service unavailable"})
	}

	ctx := c.Request().Context()
	admin, err := s.authenticateVerifiedMiniAppAdmin(ctx, body.InitData)
	if err != nil {
		return writeAuthHTTPError(c, err)
	}

	challengeID, err := rdb.Get(ctx, webLoginCodeKey(code)).Result()
	if err != nil {
		if err == redis.Nil {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "核对码不存在或已过期"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load challenge failed"})
	}

	raw, err := rdb.Get(ctx, webLoginChallengeKey(challengeID)).Result()
	if err != nil {
		if err == redis.Nil {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "登录请求已过期"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load challenge failed"})
	}

	var rec webLoginChallengeRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil || !hmac.Equal([]byte(rec.Code), []byte(code)) {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid challenge state"})
	}
	if rec.Status != "pending" {
		return c.JSON(http.StatusConflict, map[string]string{"error": "该登录请求已被处理"})
	}

	ttl, ttlErr := rdb.TTL(ctx, webLoginChallengeKey(challengeID)).Result()
	if ttlErr != nil || ttl <= 0 {
		ttl = time.Minute
	}

	rec.Status = "approved"
	rec.AdminID = admin.ID
	rec.ApproverIP = c.RealIP()
	updatedRaw, _ := json.Marshal(rec)
	if err := rdb.Set(ctx, webLoginChallengeKey(challengeID), updatedRaw, ttl).Err(); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "approve challenge failed"})
	}
	_ = rdb.Del(ctx, webLoginCodeKey(code)).Err()

	return c.JSON(http.StatusOK, map[string]any{
		"ok":           true,
		"requester_ip": rec.RequesterIP,
	})
}

func (s *Server) handlePollWebLoginChallenge(c echo.Context) error {
	var body struct {
		ChallengeID string `json:"challenge_id"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	challengeID := strings.TrimSpace(body.ChallengeID)
	if len(challengeID) != 32 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid challenge_id"})
	}
	pollCookie, err := c.Cookie(loginPollCookieName)
	if err != nil || strings.TrimSpace(pollCookie.Value) == "" {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing login session cookie"})
	}

	rdb := s.redisClient()
	if rdb == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "login service unavailable"})
	}

	ctx := c.Request().Context()
	key := webLoginChallengeKey(challengeID)
	raw, err := rdb.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			clearLoginPollCookie(c)
			return c.JSON(http.StatusGone, map[string]string{"error": "登录请求已过期，请刷新重新发起"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "poll challenge failed"})
	}

	var rec webLoginChallengeRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid challenge"})
	}
	callerHash := sha256HexString(strings.TrimSpace(pollCookie.Value))
	if !hmac.Equal([]byte(callerHash), []byte(rec.PollSecretHash)) {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "challenge binding mismatch"})
	}

	if rec.Status != "approved" || rec.AdminID == 0 {
		return c.JSON(http.StatusOK, map[string]any{"status": "pending"})
	}

	// Consume atomically so the same approval can never mint two JWT sessions.
	deleted, delErr := rdb.Del(ctx, key).Result()
	if delErr != nil || deleted == 0 {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "challenge already consumed"})
	}
	_ = rdb.Del(ctx, webLoginCodeKey(rec.Code)).Err()
	clearLoginPollCookie(c)

	admin, err := s.botService.Queries().GetAdminByID(ctx, rec.AdminID)
	if err != nil || !validAdminGroupScope(admin) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "admin not allowed"})
	}

	tokenString, err := s.signAdminJWT(admin)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "sign token failed"})
	}
	csrfToken, err := newCSRFToken()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "csrf token failed"})
	}

	setAdminCookie(c, tokenString)
	setCSRFCookie(c, csrfToken)
	s.recordAdminLoginAudit(c, admin, "web_challenge_login", map[string]any{
		"approver_ip": rec.ApproverIP,
	})
	return c.JSON(http.StatusOK, map[string]any{
		"status":      "approved",
		"admin":       serializeAdmin(admin),
		"csrf_token":  csrfToken,
		"redirect_to": "/dashboard",
	})
}

func validateTelegramMiniAppEd25519(raw, botToken string, pubKey ed25519.PublicKey, now time.Time) (telegramMiniAppUser, string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > 8192 {
		return telegramMiniAppUser{}, "", fmt.Errorf("invalid init data")
	}
	botIDStr, _, hasColon := strings.Cut(strings.TrimSpace(botToken), ":")
	botID, err := strconv.ParseInt(botIDStr, 10, 64)
	if !hasColon || err != nil || botID <= 0 {
		return telegramMiniAppUser{}, "", fmt.Errorf("invalid bot token configuration")
	}
	if len(pubKey) != ed25519.PublicKeySize {
		return telegramMiniAppUser{}, "", fmt.Errorf("missing telegram public key")
	}

	values, err := url.ParseQuery(trimmed)
	if err != nil || len(values) == 0 {
		return telegramMiniAppUser{}, "", fmt.Errorf("invalid query encoding")
	}
	for key, list := range values {
		if key == "" || strings.ContainsAny(key, "\n\r=") || len(list) != 1 {
			return telegramMiniAppUser{}, "", fmt.Errorf("duplicate or malformed parameter")
		}
	}

	providedHash, err := hex.DecodeString(values.Get("hash"))
	if err != nil || len(providedHash) != sha256.Size {
		return telegramMiniAppUser{}, "", fmt.Errorf("invalid hash")
	}
	sigRaw := strings.TrimSpace(values.Get("signature"))
	if sigRaw == "" {
		return telegramMiniAppUser{}, "", fmt.Errorf("missing telegram ed25519 signature")
	}
	sigBytes, err := decodeTelegramSignature(sigRaw)
	if err != nil || len(sigBytes) != ed25519.SignatureSize {
		return telegramMiniAppUser{}, "", fmt.Errorf("invalid telegram ed25519 signature")
	}

	authDate, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil || authDate <= 0 {
		return telegramMiniAppUser{}, "", fmt.Errorf("invalid auth date")
	}
	age := now.Sub(time.Unix(authDate, 0))
	if age < -time.Minute || age > telegramMiniAppMaxAge {
		return telegramMiniAppUser{}, "", fmt.Errorf("authorization expired")
	}

	hmacKeys := make([]string, 0, len(values))
	edKeys := make([]string, 0, len(values))
	for key := range values {
		if key == "hash" {
			continue
		}
		hmacKeys = append(hmacKeys, key)
		if key != "signature" {
			edKeys = append(edKeys, key)
		}
	}
	sort.Strings(hmacKeys)
	sort.Strings(edKeys)

	hmacParts := make([]string, 0, len(hmacKeys))
	for _, key := range hmacKeys {
		hmacParts = append(hmacParts, key+"="+values.Get(key))
	}
	secretMAC := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secretMAC.Write([]byte(botToken))
	checkMAC := hmac.New(sha256.New, secretMAC.Sum(nil))
	_, _ = checkMAC.Write([]byte(strings.Join(hmacParts, "\n")))
	if !hmac.Equal(providedHash, checkMAC.Sum(nil)) {
		return telegramMiniAppUser{}, "", fmt.Errorf("hmac signature mismatch")
	}

	edParts := make([]string, 0, len(edKeys))
	for _, key := range edKeys {
		edParts = append(edParts, key+"="+values.Get(key))
	}
	edCheckString := botIDStr + ":WebAppData\n" + strings.Join(edParts, "\n")
	if !ed25519.Verify(pubKey, []byte(edCheckString), sigBytes) {
		return telegramMiniAppUser{}, "", fmt.Errorf("ed25519 signature mismatch")
	}

	var user telegramMiniAppUser
	if err := json.Unmarshal([]byte(values.Get("user")), &user); err != nil || user.ID <= 0 || user.IsBot {
		return telegramMiniAppUser{}, "", fmt.Errorf("invalid user")
	}

	sigDigest := sha256HexString(sigRaw + ":" + strconv.FormatInt(authDate, 10) + ":" + strconv.FormatInt(user.ID, 10))
	return user, sigDigest, nil
}

func decodeTelegramSignature(raw string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(raw); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(raw)
}

func webLoginChallengeKey(challengeID string) string {
	return "clawguard:web_login:" + challengeID
}

func webLoginCodeKey(code string) string {
	return "clawguard:web_login_code:" + code
}

func normalizeLoginCode(raw string) string {
	cleaned := strings.ToUpper(strings.TrimSpace(raw))
	cleaned = strings.ReplaceAll(cleaned, "-", "")
	cleaned = strings.ReplaceAll(cleaned, " ", "")
	return cleaned
}

func generateVerificationCode(length int) (string, error) {
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, length)
	for i, b := range buf {
		out[i] = webLoginChallengeCodeCh[int(b)&31]
	}
	return string(out), nil
}

func randomHexToken(numBytes int) (string, error) {
	buf := make([]byte, numBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func sha256HexString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s *Server) requireAdminJWT(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		tokenString := readAdminToken(c)
		if tokenString == "" {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing token"})
		}

		token, err := jwt.ParseWithClaims(tokenString, &adminClaims{}, func(token *jwt.Token) (any, error) {
			if token.Method.Alg() != jwt.SigningMethodHS256.Alg() {
				return nil, fmt.Errorf("unexpected signing method %s", token.Method.Alg())
			}
			return []byte(s.cfg.JWTSecret), nil
		})
		if err != nil || !token.Valid {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid token"})
		}

		claims, ok := token.Claims.(*adminClaims)
		if !ok {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid claims"})
		}
		if claims.ID == "" {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid token id"})
		}
		if revoked, err := s.isAdminJWTRevoked(c.Request().Context(), claims.ID); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "token check failed"})
		} else if revoked {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "token revoked"})
		}

		adminID, err := strconv.ParseInt(claims.Subject, 10, 64)
		if err != nil {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid subject"})
		}

		admin, err := s.botService.Queries().GetAdminByID(c.Request().Context(), adminID)
		if err != nil {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "admin not found"})
		}
		if !validAdminGroupScope(admin) {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "invalid administrator group scope"})
		}

		c.Set(adminContextKey, admin)
		return next(c)
	}
}

func (s *Server) requireOwner(next echo.HandlerFunc) echo.HandlerFunc {
	return s.requireAdminJWT(func(c echo.Context) error {
		admin, ok := currentAdmin(c)
		if !ok || admin.Role != "owner" {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "owner only"})
		}
		return next(c)
	})
}

func (s *Server) handleLogout(c echo.Context) error {
	tokenString := readAdminToken(c)
	if tokenString != "" {
		claims := &adminClaims{}
		token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
			if token.Method.Alg() != jwt.SigningMethodHS256.Alg() {
				return nil, fmt.Errorf("unexpected signing method %s", token.Method.Alg())
			}
			return []byte(s.cfg.JWTSecret), nil
		})
		if err == nil && token.Valid && claims.ID != "" {
			if err := s.revokeAdminJWT(c.Request().Context(), claims); err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "logout failed"})
			}
		}
	}
	clearAdminCookie(c)
	clearCSRFCookie(c)
	clearLoginPollCookie(c)
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) handleMe(c echo.Context) error {
	admin, ok := currentAdmin(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not logged in"})
	}
	return c.JSON(http.StatusOK, map[string]any{"admin": serializeAdmin(admin)})
}

func (s *Server) signAdminJWT(admin store.Admin) (string, error) {
	jti, err := newCSRFToken()
	if err != nil {
		return "", err
	}
	claims := adminClaims{
		Role: admin.Role,
		TgID: admin.TelegramID,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Subject:   strconv.FormatInt(admin.ID, 10),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(s.cfg.JWTSecret))
}

func adminJWTRevokedKey(jti string) string {
	return "admin_jwt_revoked:" + jti
}

func (s *Server) isAdminJWTRevoked(ctx context.Context, jti string) (bool, error) {
	rdb := s.botService.Redis()
	if rdb == nil || jti == "" {
		return false, nil
	}
	count, err := rdb.Exists(ctx, adminJWTRevokedKey(jti)).Result()
	if err != nil {
		s.logger.Warn("check revoked admin jwt failed", zap.Error(err))
		return false, err
	}
	return count > 0, nil
}

func (s *Server) revokeAdminJWT(ctx context.Context, claims *adminClaims) error {
	rdb := s.botService.Redis()
	if rdb == nil || claims == nil || claims.ID == "" || claims.ExpiresAt == nil {
		return nil
	}
	ttl := time.Until(claims.ExpiresAt.Time)
	if ttl <= 0 {
		return nil
	}
	if err := rdb.Set(ctx, adminJWTRevokedKey(claims.ID), "1", ttl).Err(); err != nil {
		s.logger.Warn("revoke admin jwt failed", zap.Error(err))
		return err
	}
	return nil
}

func currentAdmin(c echo.Context) (store.Admin, bool) {
	admin, ok := c.Get(adminContextKey).(store.Admin)
	return admin, ok
}

func readAdminToken(c echo.Context) string {
	if cookie, err := c.Cookie(adminCookieName); err == nil && cookie.Value != "" {
		return cookie.Value
	}

	authz := c.Request().Header.Get("Authorization")
	if after, ok := strings.CutPrefix(authz, "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	return ""
}

func setAdminCookie(c echo.Context, token string) {
	c.SetCookie(&http.Cookie{
		Name:     adminCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   86400,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearAdminCookie(c echo.Context) {
	c.SetCookie(&http.Cookie{
		Name:     adminCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func setCSRFCookie(c echo.Context, token string) {
	c.SetCookie(&http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   86400,
		HttpOnly: false,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearCSRFCookie(c echo.Context) {
	c.SetCookie(&http.Cookie{
		Name:     csrfCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: false,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func setLoginPollCookie(c echo.Context, token string) {
	c.SetCookie(&http.Cookie{
		Name:     loginPollCookieName,
		Value:    token,
		Path:     "/api/auth",
		MaxAge:   int(webLoginChallengeTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearLoginPollCookie(c echo.Context) {
	c.SetCookie(&http.Cookie{
		Name:     loginPollCookieName,
		Value:    "",
		Path:     "/api/auth",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func newCSRFToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (s *Server) requireCSRF(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		method := c.Request().Method
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			return next(c)
		}
		if authz := strings.TrimSpace(c.Request().Header.Get("Authorization")); strings.HasPrefix(authz, "Bearer ") {
			if _, err := c.Cookie(adminCookieName); err != nil {
				return next(c)
			}
		}
		cookie, err := c.Cookie(csrfCookieName)
		if err != nil || cookie.Value == "" {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "missing csrf token"})
		}
		header := strings.TrimSpace(c.Request().Header.Get("X-CSRF-Token"))
		if header == "" || !hmac.Equal([]byte(header), []byte(cookie.Value)) {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "invalid csrf token"})
		}
		return next(c)
	}
}

func serializeAdmin(admin store.Admin) map[string]any {
	groupScope := json.RawMessage("[]")
	if len(admin.GroupScope) > 0 {
		groupScope = json.RawMessage(admin.GroupScope)
	}

	return map[string]any{
		"id":            admin.ID,
		"telegram_id":   admin.TelegramID,
		"username":      admin.Username,
		"first_name":    admin.FirstName,
		"photo_url":     admin.PhotoURL,
		"role":          admin.Role,
		"notes":         admin.Notes,
		"group_scope":   groupScope,
		"created_at":    admin.CreatedAt,
		"last_login_at": admin.LastLoginAt,
	}
}

func (s *Server) recordAdminLoginAudit(c echo.Context, admin store.Admin, action string, extra map[string]any) {
	ua := strings.TrimSpace(c.Request().UserAgent())
	if len(ua) > 256 {
		ua = ua[:256]
	}
	payload := map[string]any{
		"admin_id":    admin.ID,
		"telegram_id": admin.TelegramID,
		"username":    admin.Username,
		"role":        admin.Role,
		"ip":          c.RealIP(),
		"user_agent":  ua,
	}
	for k, v := range extra {
		payload[k] = v
	}
	after := mustRawJSON(payload)
	if err := s.writeAudit(c.Request().Context(), admin, "auth", nil, action, []byte("null"), after); err != nil {
		s.logger.Warn("write admin login audit failed", zap.String("action", action), zap.Error(err))
	}
}

func stringPtr(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}
