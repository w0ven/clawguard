package api

import (
	"encoding/json"
	"github.com/labstack/echo/v4"
	"net/http"
)

func (s *Server) registerNativeAssistantRoutes(admin *echo.Group) {
	admin.GET("/assistant/native", s.handleGetNativeAssistant)
	admin.PUT("/assistant/native", s.handlePutNativeAssistant)
	admin.GET("/groups/:chat_id/assistant/native", s.handleGetNativeGroupAssistant)
	admin.PUT("/groups/:chat_id/assistant/native", s.handlePutNativeGroupAssistant)
	admin.GET("/groups/:chat_id/assistant/native/memories", s.handleListNativeMemories)
	admin.POST("/groups/:chat_id/assistant/native/memories", s.handleAddNativeMemory)
	admin.DELETE("/groups/:chat_id/assistant/native/memories/:id", s.handleDeleteNativeMemory)
	admin.PUT("/groups/:chat_id/assistant/native/memories/:id", s.handleReplaceNativeMemory)
}
func (s *Server) nativeControl(c echo.Context, path string, groupID int64, values map[string]any) error {
	if s.botService == nil {
		return c.JSON(503, map[string]string{"error": "助手服务不可用"})
	}
	body, err := s.botService.NativeAssistantControl(c.Request().Context(), path, groupID, values)
	if err != nil {
		return c.JSON(503, map[string]string{"error": "原生助手不可用或配置未通过；未切回旧引擎"})
	}
	return c.JSONBlob(http.StatusOK, body)
}
func (s *Server) handleGetNativeAssistant(c echo.Context) error {
	return s.nativeControl(c, "/config/read", 0, nil)
}
func (s *Server) handlePutNativeAssistant(c echo.Context) error {
	admin, _ := currentAdmin(c)
	if !adminHasGlobalAccess(admin) {
		return c.JSON(403, map[string]string{"error": "仅全局管理员可修改全局助手配置"})
	}
	var values map[string]any
	if bindAssistantJSON(c, &values) != nil {
		return c.JSON(400, map[string]string{"error": "原生配置格式无效"})
	}
	return s.nativeControl(c, "/config/write", 0, values)
}
func (s *Server) handleGetNativeGroupAssistant(c echo.Context) error {
	_, group, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	return s.nativeControl(c, "/groups/read", group, nil)
}
func (s *Server) handlePutNativeGroupAssistant(c echo.Context) error {
	admin, group, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	var values map[string]any
	if bindAssistantJSON(c, &values) != nil {
		return c.JSON(400, map[string]string{"error": "原生群配置格式无效"})
	}
	values["operator_id"] = admin.TelegramID
	return s.nativeControl(c, "/groups/write", group, values)
}
func (s *Server) handleListNativeMemories(c echo.Context) error {
	_, group, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	return s.nativeControl(c, "/memory/list", group, map[string]any{"topic_id": c.QueryParam("topic_id")})
}
func (s *Server) handleAddNativeMemory(c echo.Context) error {
	admin, group, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	var values map[string]any
	if bindAssistantJSON(c, &values) != nil {
		return c.JSON(400, map[string]string{"error": "记忆格式无效"})
	}
	values["operator_id"] = admin.TelegramID
	return s.nativeControl(c, "/memory/add", group, values)
}
func (s *Server) handleDeleteNativeMemory(c echo.Context) error {
	admin, group, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	id, err := parseRequiredInt64(c.Param("id"))
	if err != nil || id <= 0 {
		return c.JSON(400, map[string]string{"error": "记忆编号无效"})
	}
	return s.nativeControl(c, "/memory/delete", group, map[string]any{"id": id, "operator_id": admin.TelegramID, "topic_id": c.QueryParam("topic_id")})
}

func (s *Server) handleReplaceNativeMemory(c echo.Context) error {
	admin, group, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	id, err := parseRequiredInt64(c.Param("id"))
	if err != nil || id <= 0 {
		return c.JSON(400, map[string]string{"error": "记忆编号无效"})
	}
	var values map[string]any
	if bindAssistantJSON(c, &values) != nil {
		return c.JSON(400, map[string]string{"error": "记忆格式无效"})
	}
	values["id"], values["operator_id"] = id, admin.TelegramID
	return s.nativeControl(c, "/memory/replace", group, values)
}

var _ json.RawMessage // native payloads are served without secret-bearing CG models.
