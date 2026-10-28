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

func (s *Server) registerGroupExemptRoutes(admin *echo.Group) {
	admin.GET("/groups/:chat_id/exempt-users", s.listGroupExemptUsers)
	admin.PUT("/groups/:chat_id/exempt-users/:user_id", s.putGroupExemptUser)
	admin.DELETE("/groups/:chat_id/exempt-users/:user_id", s.deleteGroupExemptUser)
}

func (s *Server) listGroupExemptUsers(c echo.Context) error {
	admin, _ := currentAdmin(c)
	chatID, err := strconv.ParseInt(c.Param("chat_id"), 10, 64)
	if err != nil || chatID >= 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid chat_id"})
	}
	if !adminCanAccessChat(admin, chatID) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
	}
	limit, offset := 50, 0
	if raw := c.QueryParam("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid limit"})
		}
	}
	if raw := c.QueryParam("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 || offset > 2000000 {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid offset"})
		}
	}
	status := c.QueryParam("status")
	if status == "" {
		status = "all"
	}
	if status != "all" && status != "active" && status != "expired" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid status"})
	}
	search := strings.TrimSpace(c.QueryParam("search"))
	if len(search) > 500 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "search too long"})
	}
	items, total, err := s.botService.Queries().ListGroupExemptUsersPage(c.Request().Context(), chatID, search, status, limit, offset)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "无法加载本群豁免名单"})
	}
	return c.JSON(http.StatusOK, map[string]any{"users": items, "total": total, "limit": limit, "offset": offset, "has_more": int64(offset+len(items)) < total})
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

func (s *Server) putGroupExemptUser(c echo.Context) error {
	admin, _ := currentAdmin(c)
	chatID, err := strconv.ParseInt(c.Param("chat_id"), 10, 64)
	if err != nil || chatID >= 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid chat_id"})
	}
	if !adminCanAccessChat(admin, chatID) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
	}
	id, err := parseExemptUserID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "用户 ID 必须为正整数"})
	}
	if id == 1087968824 || id == 136817688 || id == 777000 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "虚拟身份不能豁免"})
	}
	knownBot, err := s.botService.ExemptionTargetIsKnownBot(c.Request().Context(), chatID, id)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "无法核实用户身份"})
	}
	if knownBot {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "机器人不能加入真人豁免，请使用机器人白名单"})
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
	if len(input.Note) > 500 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "备注过长或到期时间不是未来时间"})
	}
	ctx := c.Request().Context()
	errExpired := errors.New("expiry not in future")
	var result store.GroupExemptUser
	err = s.botService.Queries().Transact(ctx, func(q *store.Queries) error {
		if e := q.LockGroupExemptUser(ctx, chatID, id); e != nil {
			return e
		}
		before, e := q.GetGroupExemptUser(ctx, chatID, id)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if input.ExpiresAt != nil && !input.ExpiresAt.After(time.Now()) && (before.ExpiresAt == nil || !before.ExpiresAt.Equal(*input.ExpiresAt)) {
			return errExpired
		}
		result, e = q.UpsertGroupExemptUser(ctx, chatID, id, input.Note, input.ExpiresAt)
		if e != nil {
			return e
		}
		previous := []byte("null")
		if e == nil && before.UserID != 0 {
			previous = mustRawJSON(before)
		}
		return writeAuditWithQueries(ctx, q, admin, "group", &chatID, "upsert_group_exempt_user", previous, mustRawJSON(result))
	})
	if errors.Is(err, errExpired) {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "到期时间不是未来时间"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "保存豁免失败"})
	}
	return c.JSON(http.StatusOK, result)
}

func (s *Server) deleteGroupExemptUser(c echo.Context) error {
	admin, _ := currentAdmin(c)
	chatID, err := strconv.ParseInt(c.Param("chat_id"), 10, 64)
	if err != nil || chatID >= 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid chat_id"})
	}
	if !adminCanAccessChat(admin, chatID) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
	}
	id, err := parseExemptUserID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "用户 ID 必须为正整数"})
	}
	ctx := c.Request().Context()
	err = s.botService.Queries().Transact(ctx, func(q *store.Queries) error {
		if e := q.LockGroupExemptUser(ctx, chatID, id); e != nil {
			return e
		}
		before, e := q.GetGroupExemptUser(ctx, chatID, id)
		if e != nil {
			return e
		}
		if _, e = q.DeleteGroupExemptUser(ctx, chatID, id); e != nil {
			return e
		}
		return writeAuditWithQueries(ctx, q, admin, "group", &chatID, "delete_group_exempt_user", mustRawJSON(before), []byte("null"))
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "豁免不存在"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "撤销豁免失败"})
	}
	return c.NoContent(http.StatusNoContent)
}
