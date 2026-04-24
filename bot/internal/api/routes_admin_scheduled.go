package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/openclaw/clawguard/internal/store"
)

const maxScheduledMessagesPerChat = 20

var shanghaiLocation = time.FixedZone("CST", 8*3600)

type scheduledMessageRequest struct {
	Name              string            `json:"name"`
	ScheduleType      string            `json:"schedule_type"`
	IntervalMinutes   *int32            `json:"interval_minutes"`
	DailyTimes        []string          `json:"daily_times"`
	Content           string            `json:"content"`
	Buttons           []scheduledButton `json:"buttons"`
	AutoDeleteSeconds int32             `json:"auto_delete_seconds"`
	Enabled           bool              `json:"enabled"`
}

type scheduledButton struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

func (s *Server) registerScheduledMessageRoutes(admin *echo.Group) {
	admin.GET("/groups/:chat_id/scheduled-messages", s.handleListScheduledMessages)
	admin.POST("/groups/:chat_id/scheduled-messages", s.handleCreateScheduledMessage)
	admin.PUT("/groups/:chat_id/scheduled-messages/:id", s.handleUpdateScheduledMessage)
	admin.DELETE("/groups/:chat_id/scheduled-messages/:id", s.handleDeleteScheduledMessage)
	admin.POST("/groups/:chat_id/scheduled-messages/:id/run-now", s.handleRunScheduledMessageNow)
	admin.GET("/groups/:chat_id/scheduled-messages/:id/runs", s.handleListScheduledMessageRuns)
}

func (s *Server) handleListScheduledMessages(c echo.Context) error {
	chatID, ok := s.authorizeScheduledMessageChat(c)
	if !ok {
		return nil
	}
	items, err := s.botService.Queries().ListScheduledMessagesByChat(c.Request().Context(), chatID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "加载定时消息失败"})
	}
	response := make([]map[string]any, 0, len(items))
	for _, item := range items {
		response = append(response, s.serializeScheduledMessage(item))
	}
	return c.JSON(http.StatusOK, map[string]any{"scheduled_messages": response, "limit": maxScheduledMessagesPerChat})
}

func (s *Server) handleCreateScheduledMessage(c echo.Context) error {
	chatID, ok := s.authorizeScheduledMessageChat(c)
	if !ok {
		return nil
	}
	count, err := s.botService.Queries().CountScheduledMessagesByChat(c.Request().Context(), chatID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "检查定时消息数量失败"})
	}
	if count >= maxScheduledMessagesPerChat {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "每个群最多创建 20 条定时消息"})
	}
	params, err := s.parseScheduledMessageRequest(c, chatID, nil)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	created, err := s.botService.Queries().CreateScheduledMessage(c.Request().Context(), store.CreateScheduledMessageParams{
		ChatID:            params.ChatID,
		Name:              params.Name,
		ScheduleType:      params.ScheduleType,
		IntervalMinutes:   params.IntervalMinutes,
		DailyTimes:        params.DailyTimes,
		Timezone:          params.Timezone,
		Content:           params.Content,
		Buttons:           params.Buttons,
		AutoDeleteSeconds: params.AutoDeleteSeconds,
		Enabled:           params.Enabled,
		Status:            params.Status,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "创建定时消息失败"})
	}
	if err := s.reloadScheduledMessage(created); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "重载定时任务失败"})
	}
	return c.JSON(http.StatusOK, map[string]any{"scheduled_message": s.serializeScheduledMessage(created)})
}

func (s *Server) handleUpdateScheduledMessage(c echo.Context) error {
	chatID, id, ok := s.authorizeScheduledMessageItem(c)
	if !ok {
		return nil
	}
	existing, err := s.botService.Queries().GetScheduledMessage(c.Request().Context(), id)
	if err != nil {
		if err == pgx.ErrNoRows || existing.ChatID != chatID {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "定时消息不存在"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "加载定时消息失败"})
	}
	if existing.ChatID != chatID {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "定时消息不存在"})
	}
	params, err := s.parseScheduledMessageRequest(c, chatID, &id)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	updated, err := s.botService.Queries().UpdateScheduledMessage(c.Request().Context(), store.UpdateScheduledMessageParams{
		ID:                id,
		ChatID:            params.ChatID,
		Name:              params.Name,
		ScheduleType:      params.ScheduleType,
		IntervalMinutes:   params.IntervalMinutes,
		DailyTimes:        params.DailyTimes,
		Timezone:          params.Timezone,
		Content:           params.Content,
		Buttons:           params.Buttons,
		AutoDeleteSeconds: params.AutoDeleteSeconds,
		Enabled:           params.Enabled,
		Status:            params.Status,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "定时消息不存在"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "更新定时消息失败"})
	}
	if err := s.reloadScheduledMessage(updated); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "重载定时任务失败"})
	}
	return c.JSON(http.StatusOK, map[string]any{"scheduled_message": s.serializeScheduledMessage(updated)})
}

func (s *Server) handleDeleteScheduledMessage(c echo.Context) error {
	chatID, id, ok := s.authorizeScheduledMessageItem(c)
	if !ok {
		return nil
	}
	if err := s.botService.Queries().DeleteScheduledMessage(c.Request().Context(), store.DeleteScheduledMessageParams{ID: id, ChatID: chatID}); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "删除定时消息失败"})
	}
	if s.scheduler != nil {
		s.scheduler.Remove(id)
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) handleRunScheduledMessageNow(c echo.Context) error {
	chatID, id, ok := s.authorizeScheduledMessageItem(c)
	if !ok {
		return nil
	}
	msg, err := s.botService.Queries().GetScheduledMessage(c.Request().Context(), id)
	if err != nil || msg.ChatID != chatID {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "定时消息不存在"})
	}
	if s.scheduler == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "定时调度器未启动"})
	}
	if err := s.scheduler.RunNow(c.Request().Context(), id); err != nil {
		return c.JSON(http.StatusBadGateway, map[string]string{"error": "立即试发失败: " + err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListScheduledMessageRuns(c echo.Context) error {
	chatID, id, ok := s.authorizeScheduledMessageItem(c)
	if !ok {
		return nil
	}
	msg, err := s.botService.Queries().GetScheduledMessage(c.Request().Context(), id)
	if err != nil || msg.ChatID != chatID {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "定时消息不存在"})
	}
	runs, err := s.botService.Queries().ListScheduledMessageRuns(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "加载发送历史失败"})
	}
	items := make([]map[string]any, 0, len(runs))
	for _, run := range runs {
		items = append(items, serializeScheduledMessageRun(run))
	}
	return c.JSON(http.StatusOK, map[string]any{"runs": items})
}

type scheduledMessageParams struct {
	ChatID            int64
	Name              string
	ScheduleType      string
	IntervalMinutes   *int32
	DailyTimes        []string
	Timezone          string
	Content           string
	Buttons           []byte
	AutoDeleteSeconds int32
	Enabled           bool
	Status            string
}

func (s *Server) parseScheduledMessageRequest(c echo.Context, chatID int64, _ *int64) (scheduledMessageParams, error) {
	var req scheduledMessageRequest
	if err := c.Bind(&req); err != nil {
		return scheduledMessageParams{}, fmt.Errorf("请求格式无效")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 100 {
		return scheduledMessageParams{}, fmt.Errorf("名称不能为空且不超过 100 个字符")
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		return scheduledMessageParams{}, fmt.Errorf("消息正文不能为空")
	}
	if req.AutoDeleteSeconds < 0 || req.AutoDeleteSeconds > 3600 {
		return scheduledMessageParams{}, fmt.Errorf("自动删除秒数必须在 0-3600 之间")
	}

	var interval *int32
	var dailyTimes []string
	scheduleType := strings.TrimSpace(req.ScheduleType)
	switch scheduleType {
	case "interval":
		if req.IntervalMinutes == nil || *req.IntervalMinutes < 1 || *req.IntervalMinutes > 10080 {
			return scheduledMessageParams{}, fmt.Errorf("间隔分钟数必须在 1-10080 之间")
		}
		value := *req.IntervalMinutes
		interval = &value
	case "daily":
		converted, err := normalizeDailyTimes(req.DailyTimes)
		if err != nil {
			return scheduledMessageParams{}, err
		}
		dailyTimes = converted
	default:
		return scheduledMessageParams{}, fmt.Errorf("触发类型无效")
	}

	buttons, err := normalizeScheduledButtons(req.Buttons)
	if err != nil {
		return scheduledMessageParams{}, err
	}
	status := "paused"
	if req.Enabled {
		status = "active"
	}
	return scheduledMessageParams{
		ChatID:            chatID,
		Name:              name,
		ScheduleType:      scheduleType,
		IntervalMinutes:   interval,
		DailyTimes:        dailyTimes,
		Timezone:          "Asia/Shanghai",
		Content:           content,
		Buttons:           buttons,
		AutoDeleteSeconds: req.AutoDeleteSeconds,
		Enabled:           req.Enabled,
		Status:            status,
	}, nil
}

func (s *Server) authorizeScheduledMessageChat(c echo.Context) (int64, bool) {
	admin, _ := currentAdmin(c)
	chatID, err := parseRequiredInt64(c.Param("chat_id"))
	if err != nil {
		_ = c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid chat_id"})
		return 0, false
	}
	if !adminCanAccessChat(admin, chatID) {
		_ = c.JSON(http.StatusForbidden, map[string]string{"error": "group out of scope"})
		return 0, false
	}
	return chatID, true
}

func (s *Server) authorizeScheduledMessageItem(c echo.Context) (int64, int64, bool) {
	chatID, ok := s.authorizeScheduledMessageChat(c)
	if !ok {
		return 0, 0, false
	}
	id, err := parseRequiredInt64(c.Param("id"))
	if err != nil {
		_ = c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return 0, 0, false
	}
	return chatID, id, true
}

func normalizeDailyTimes(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("每日模式至少需要一个时间点")
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		parsed, err := time.ParseInLocation("15:04", trimmed, shanghaiLocation)
		if err != nil {
			return nil, fmt.Errorf("每日时间必须是 HH:MM 格式")
		}
		utcValue := time.Date(2000, 1, 1, parsed.Hour(), parsed.Minute(), 0, 0, shanghaiLocation).UTC().Format("15:04")
		if !seen[utcValue] {
			seen[utcValue] = true
			result = append(result, utcValue)
		}
	}
	sort.Strings(result)
	return result, nil
}

func normalizeScheduledButtons(buttons []scheduledButton) ([]byte, error) {
	clean := make([]scheduledButton, 0, len(buttons))
	for _, button := range buttons {
		text := strings.TrimSpace(button.Text)
		link := strings.TrimSpace(button.URL)
		if text == "" && link == "" {
			continue
		}
		if text == "" || link == "" {
			return nil, fmt.Errorf("按钮文本和链接都必须填写")
		}
		parsed, err := url.ParseRequestURI(link)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("按钮链接必须是完整 URL")
		}
		clean = append(clean, scheduledButton{Text: text, URL: link})
	}
	if len(clean) == 0 {
		return nil, nil
	}
	return json.Marshal(clean)
}

func (s *Server) reloadScheduledMessage(msg store.ScheduledMessage) error {
	if s.scheduler == nil {
		return nil
	}
	return s.scheduler.Reload(msg)
}

func (s *Server) serializeScheduledMessage(msg store.ScheduledMessage) map[string]any {
	buttons := []scheduledButton{}
	if len(msg.Buttons) > 0 {
		_ = json.Unmarshal(msg.Buttons, &buttons)
	}
	response := map[string]any{
		"id":                  msg.ID,
		"chat_id":             msg.ChatID,
		"name":                msg.Name,
		"schedule_type":       msg.ScheduleType,
		"interval_minutes":    msg.IntervalMinutes,
		"daily_times":         dailyTimesToShanghai(msg.DailyTimes),
		"timezone":            msg.Timezone,
		"content":             msg.Content,
		"buttons":             buttons,
		"auto_delete_seconds": msg.AutoDeleteSeconds,
		"enabled":             msg.Enabled,
		"status":              msg.Status,
		"last_run_at":         msg.LastRunAt,
		"last_message_id":     msg.LastMessageID,
		"last_error":          msg.LastError,
		"last_skip_reason":    msg.LastSkipReason,
		"created_at":          msg.CreatedAt,
		"updated_at":          msg.UpdatedAt,
		"next_run_at":         nil,
	}
	if s.scheduler != nil {
		response["next_run_at"] = s.scheduler.NextRun(msg.ID)
	}
	return response
}

func dailyTimesToShanghai(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		parsed, err := time.ParseInLocation("15:04", strings.TrimSpace(value), time.UTC)
		if err != nil {
			continue
		}
		local := time.Date(2000, 1, 1, parsed.Hour(), parsed.Minute(), 0, 0, time.UTC).In(shanghaiLocation)
		result = append(result, local.Format("15:04"))
	}
	sort.Strings(result)
	return result
}

func serializeScheduledMessageRun(run store.ScheduledMessageRun) map[string]any {
	return map[string]any{
		"id":                   run.ID,
		"scheduled_message_id": run.ScheduledMessageID,
		"ran_at":               run.RanAt,
		"success":              run.Success,
		"tg_message_id":        run.TgMessageID,
		"rendered_preview":     run.RenderedPreview,
		"error":                run.Error,
		"duration_ms":          run.DurationMs,
	}
}
