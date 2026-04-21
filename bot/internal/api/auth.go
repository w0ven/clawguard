package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"

	"github.com/openclaw/clawguard/internal/store"
)

const adminCookieName = "cg_admin"
const adminContextKey = "cg_admin_actor"

type adminClaims struct {
	Role string `json:"role"`
	TgID int64  `json:"tg_id"`
	jwt.RegisteredClaims
}

type telegramLoginPayload struct {
	ID        int64
	FirstName string
	LastName  string
	Username  string
	PhotoURL  string
	AuthDate  int64
	Hash      string
}

func (s *Server) registerAuthRoutes() {
	s.echo.GET("/api/auth/telegram-login", s.handleTelegramLogin)
	s.echo.POST("/api/auth/telegram-login", s.handleTelegramLogin)
	s.echo.POST("/api/auth/logout", s.handleLogout)
	s.echo.GET("/api/auth/me", s.requireAdminJWT(s.handleMe))
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

		adminID, err := strconv.ParseInt(claims.Subject, 10, 64)
		if err != nil {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid subject"})
		}

		admin, err := s.botService.Queries().GetAdminByID(c.Request().Context(), adminID)
		if err != nil {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "admin not found"})
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

func (s *Server) handleTelegramLogin(c echo.Context) error {
	payload, err := parseTelegramLoginPayload(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if err := validateTelegramLogin(payload, s.cfg.BotToken); err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": err.Error()})
	}

	queries := s.botService.Queries()
	admin, err := queries.GetAdminByTelegramID(c.Request().Context(), payload.ID)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "admin not allowed"})
	}

	admin, err = queries.TouchAdminLogin(c.Request().Context(), store.TouchAdminLoginParams{
		TelegramID: payload.ID,
		Username:   stringPtr(payload.Username),
		FirstName:  stringPtr(strings.TrimSpace(strings.TrimSpace(payload.FirstName + " " + payload.LastName))),
		PhotoURL:   stringPtr(payload.PhotoURL),
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "update admin failed"})
	}

	tokenString, err := s.signAdminJWT(admin)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "sign token failed"})
	}

	setAdminCookie(c, tokenString)

	if c.Request().Method == http.MethodGet {
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	return c.JSON(http.StatusOK, map[string]any{
		"admin": serializeAdmin(admin),
	})
}

func (s *Server) handleLogout(c echo.Context) error {
	clearAdminCookie(c)
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
	claims := adminClaims{
		Role: admin.Role,
		TgID: admin.TelegramID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(admin.ID, 10),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(s.cfg.JWTSecret))
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
		SameSite: http.SameSiteLaxMode,
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
		SameSite: http.SameSiteLaxMode,
	})
}

func parseTelegramLoginPayload(c echo.Context) (telegramLoginPayload, error) {
	id, err := strconv.ParseInt(paramValue(c, "id"), 10, 64)
	if err != nil {
		return telegramLoginPayload{}, fmt.Errorf("invalid id")
	}
	authDate, err := strconv.ParseInt(paramValue(c, "auth_date"), 10, 64)
	if err != nil {
		return telegramLoginPayload{}, fmt.Errorf("invalid auth_date")
	}

	payload := telegramLoginPayload{
		ID:        id,
		FirstName: paramValue(c, "first_name"),
		LastName:  paramValue(c, "last_name"),
		Username:  paramValue(c, "username"),
		PhotoURL:  paramValue(c, "photo_url"),
		AuthDate:  authDate,
		Hash:      paramValue(c, "hash"),
	}
	if payload.Hash == "" {
		return telegramLoginPayload{}, fmt.Errorf("missing hash")
	}
	return payload, nil
}

func paramValue(c echo.Context, key string) string {
	if value := strings.TrimSpace(c.FormValue(key)); value != "" {
		return value
	}
	return strings.TrimSpace(c.QueryParam(key))
}

func validateTelegramLogin(payload telegramLoginPayload, botToken string) error {
	now := time.Now().Unix()
	if payload.AuthDate <= 0 || now-payload.AuthDate > 86400 || payload.AuthDate-now > 300 {
		return fmt.Errorf("authorization expired")
	}

	fields := map[string]string{
		"auth_date":  strconv.FormatInt(payload.AuthDate, 10),
		"first_name": payload.FirstName,
		"id":         strconv.FormatInt(payload.ID, 10),
		"last_name":  payload.LastName,
		"photo_url":  payload.PhotoURL,
		"username":   payload.Username,
	}

	keys := make([]string, 0, len(fields))
	for key, value := range fields {
		if value == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var parts []string
	for _, key := range keys {
		parts = append(parts, key+"="+fields[key])
	}
	dataCheckString := strings.Join(parts, "\n")

	botHash := sha256.Sum256([]byte(botToken))
	mac := hmac.New(sha256.New, botHash[:])
	_, _ = mac.Write([]byte(dataCheckString))
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(strings.ToLower(payload.Hash))) {
		return fmt.Errorf("invalid telegram signature")
	}
	return nil
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

func stringPtr(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}
