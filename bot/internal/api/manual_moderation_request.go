package api

import (
	"bytes"
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openclaw/clawguard/internal/store"
)

type manualResponse struct {
	headers http.Header
	code    int
	body    bytes.Buffer
}

func (r *manualResponse) Header() http.Header { return r.headers }
func (r *manualResponse) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
}
func (r *manualResponse) Write(b []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.body.Write(b)
}

var manualRequestKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32,64}$`)

func (s *Server) manualModerationIdempotency(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		key := strings.TrimSpace(c.Request().Header.Get("Idempotency-Key"))
		if key == "" {
			return next(c)
		}
		if !manualRequestKeyPattern.MatchString(key) {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid operation key"})
		}
		admin, ok := currentAdmin(c)
		if !ok {
			return c.NoContent(http.StatusUnauthorized)
		}
		chat, user, err := parseChatAndUser(c)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid target"})
		}
		if !adminCanAccessChat(admin, chat) {
			return c.NoContent(http.StatusForbidden)
		}
		v := store.ManualModerationRequest{AdminID: admin.TelegramID, ChatID: chat, UserID: user, Action: c.Request().URL.Path}
		q := s.botService.Queries()
		claimed, err := q.ClaimManualModerationRequest(c.Request().Context(), key, v)
		if err != nil {
			return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "无法保存操作，请稍后重试"})
		}
		if !claimed {
			prior, err := q.GetManualModerationRequest(c.Request().Context(), key)
			if err != nil {
				return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "无法查询操作结果，请稍后重试"})
			}
			if prior.AdminID != v.AdminID || prior.ChatID != v.ChatID || prior.UserID != v.UserID || prior.Action != v.Action {
				return c.JSON(http.StatusConflict, map[string]string{"error": "操作编号与目标不匹配，请重新发起"})
			}
			if prior.ResponseCode == nil {
				return c.JSON(http.StatusConflict, map[string]string{"error": "该操作仍在执行或结果待确认，请先核查当前状态，不要重复执行"})
			}
			if *prior.ResponseCode == http.StatusNoContent {
				return c.NoContent(http.StatusNoContent)
			}
			return c.Blob(*prior.ResponseCode, echo.MIMEApplicationJSONCharsetUTF8, []byte(prior.ResponseBody))
		}
		// Persist the outcome before acknowledging it; an interrupted request is
		// never dispatched again automatically because Telegram has no request CAS.
		original := c.Response()
		rec := &manualResponse{headers: make(http.Header)}
		c.SetResponse(echo.NewResponse(rec, c.Echo()))
		defer c.SetResponse(original)
		err = next(c)
		if err != nil {
			c.Echo().HTTPErrorHandler(err, c)
		}
		c.SetResponse(original)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := q.CompleteManualModerationRequest(ctx, key, rec.code, rec.body.String()); err != nil {
			return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "操作结果尚未保存，请先核查当前状态，不要重复执行"})
		}
		if rec.code == http.StatusNoContent {
			return c.NoContent(http.StatusNoContent)
		}
		return c.Blob(rec.code, echo.MIMEApplicationJSONCharsetUTF8, rec.body.Bytes())
	}
}
