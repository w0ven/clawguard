package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/openclaw/clawguard/internal/store"
)

func (s *Server) registerGlobalExemptRoutes(admin *echo.Group) {
	admin.GET("/global-exempt-users", s.listGlobalExemptUsers)
	admin.PUT("/global-exempt-users/:user_id", s.putGlobalExemptUser)
	admin.DELETE("/global-exempt-users/:user_id", s.deleteGlobalExemptUser)
}

func (s *Server) listGlobalExemptUsers(c echo.Context) error {
	admin, _ := currentAdmin(c)
	if !adminHasGlobalAccess(admin) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "global exemptions out of scope"})
	}
	items, err := s.botService.Queries().ListGlobalExemptUsers(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "无法加载全局豁免名单"})
	}
	return c.JSON(http.StatusOK, map[string]any{"users": items})
}

func parseExemptUserID(c echo.Context) (int64, error) {
	raw := c.Param("user_id")
	if raw == "" {
		return 0, errors.New("empty id")
	}
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0, errors.New("invalid id")
		}
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}

func (s *Server) putGlobalExemptUser(c echo.Context) error {
	admin, _ := currentAdmin(c)
	if !adminHasGlobalAccess(admin) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "global exemptions out of scope"})
	}
	id, err := parseExemptUserID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "用户 ID 必须为正整数"})
	}
	var input struct {
		Note      string     `json:"note"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 4096))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&input); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "请求格式无效"})
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "请求格式无效"})
	}
	input.Note = strings.TrimSpace(input.Note)
	if len(input.Note) > 500 || (input.ExpiresAt != nil && !input.ExpiresAt.After(time.Now())) {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "备注过长或到期时间不是未来时间"})
	}
	ctx := c.Request().Context()
	var result store.GlobalExemptUser
	err = s.botService.Queries().Transact(ctx, func(q *store.Queries) error {
		if e := q.LockGlobalExemptUser(ctx, id); e != nil {
			return e
		}
		before, e := q.GetGlobalExemptUser(ctx, id)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		result, e = q.UpsertGlobalExemptUser(ctx, id, input.Note, input.ExpiresAt)
		if e != nil {
			return e
		}
		previous := []byte("null")
		if e == nil && before.UserID != 0 {
			previous = mustRawJSON(before)
		}
		return writeAuditWithQueries(ctx, q, admin, "global", nil, "upsert_global_exempt_user", previous, mustRawJSON(result))
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "保存豁免失败"})
	}
	return c.JSON(http.StatusOK, result)
}

func (s *Server) deleteGlobalExemptUser(c echo.Context) error {
	admin, _ := currentAdmin(c)
	if !adminHasGlobalAccess(admin) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "global exemptions out of scope"})
	}
	id, err := parseExemptUserID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "用户 ID 必须为正整数"})
	}
	ctx := c.Request().Context()
	err = s.botService.Queries().Transact(ctx, func(q *store.Queries) error {
		if e := q.LockGlobalExemptUser(ctx, id); e != nil {
			return e
		}
		before, e := q.GetGlobalExemptUser(ctx, id)
		if e != nil {
			return e
		}
		if _, e = q.DeleteGlobalExemptUser(ctx, id); e != nil {
			return e
		}
		return writeAuditWithQueries(ctx, q, admin, "global", nil, "delete_global_exempt_user", mustRawJSON(before), []byte("null"))
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "豁免不存在"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "撤销豁免失败"})
	}
	return c.NoContent(http.StatusNoContent)
}
