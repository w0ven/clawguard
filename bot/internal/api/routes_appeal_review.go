package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
)

func (s *Server) registerAppealReviewRoutes() {
	g := s.echo.Group("/api/admin/appeals", s.requireAdminJWT, s.requireCSRF, s.adminWriteRateLimit())
	g.GET("", s.listAppealReviews)
	g.GET("/:id", s.getAppealReview)
	g.POST("/:id/decision", s.decideAppealReview)
	g.POST("/:id/retry", s.retryAppealReview)
}
func (s *Server) listAppealReviews(c echo.Context) error {
	admin, _ := currentAdmin(c)
	scope, global := adminScopeFilter(admin)
	chat := int64(0)
	var err error
	if v := strings.TrimSpace(c.QueryParam("chat_id")); v != "" {
		chat, err = strconv.ParseInt(v, 10, 64)
		if err != nil || chat == 0 {
			return c.JSON(400, map[string]string{"error": "invalid group"})
		}
		if !adminCanAccessChat(admin, chat) {
			return c.JSON(403, map[string]string{"error": "group out of scope"})
		}
	}
	var from, to *time.Time
	for i, raw := range []string{c.QueryParam("from"), c.QueryParam("to")} {
		if raw != "" {
			v, e := time.Parse(time.RFC3339, raw)
			if e != nil {
				return c.JSON(400, map[string]string{"error": "invalid time"})
			}
			if i == 0 {
				from = &v
			} else {
				to = &v
			}
		}
	}
	if from != nil && to != nil && !from.Before(*to) {
		return c.JSON(400, map[string]string{"error": "invalid time range"})
	}
	offset := 0
	if raw := c.QueryParam("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 || offset > 100000 {
			return c.JSON(400, map[string]string{"error": "invalid offset"})
		}
	}
	status := strings.TrimSpace(c.QueryParam("status"))
	switch status {
	case "", "pending", "upheld", "executing", "failed", "manual_review", "completed":
	default:
		return c.JSON(400, map[string]string{"error": "invalid status"})
	}
	q := s.botService.Queries()
	items, err := q.ListAppealReviews(c.Request().Context(), global, scope, chat, status, from, to, 21, offset)
	if err != nil {
		return c.JSON(503, map[string]string{"error": "申诉记录暂不可用"})
	}
	count, err := q.CountPendingAppealReviews(c.Request().Context(), global, scope)
	if err != nil {
		return c.JSON(503, map[string]string{"error": "申诉记录暂不可用"})
	}
	more := len(items) > 20
	if more {
		items = items[:20]
	}
	return c.JSON(http.StatusOK, map[string]any{"appeals": items, "pending_count": count, "has_more": more})
}
func (s *Server) getAppealReview(c echo.Context) error {
	admin, _ := currentAdmin(c)
	scope, global := adminScopeFilter(admin)
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		return c.JSON(400, map[string]string{"error": "invalid id"})
	}
	v, err := s.botService.Queries().GetAppealReview(c.Request().Context(), id, global, scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(404, map[string]string{"error": "appeal not found"})
	}
	if err != nil {
		return c.JSON(503, map[string]string{"error": "申诉记录暂不可用"})
	}
	return c.JSON(200, map[string]any{"appeal": v})
}
