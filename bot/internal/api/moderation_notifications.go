package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
)

func (s *Server) handleModerationNotifications(c echo.Context) error {
	admin, _ := currentAdmin(c)
	chat, err := strconv.ParseInt(strings.TrimSpace(c.QueryParam("chat_id")), 10, 64)
	if err != nil || chat == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "chat_id required"})
	}
	if !adminCanAccessChat(admin, chat) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
	}
	var before int64
	if raw := strings.TrimSpace(c.QueryParam("before")); raw != "" {
		before, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || before <= 0 {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid cursor"})
		}
	}
	limit := parseLimit(c.QueryParam("limit"))
	if limit > 100 {
		limit = 100
	}
	items, err := s.botService.Queries().ListModerationNotifications(c.Request().Context(), chat, before, limit)
	if err != nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "通知状态暂不可用"})
	}
	backlog, err := s.botService.Queries().CountModerationNotificationBacklog(c.Request().Context(), chat)
	if err != nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "通知状态暂不可用"})
	}
	return c.JSON(http.StatusOK, map[string]any{"notifications": items, "backlog": backlog})
}
