package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/store"
)

func (s *Server) registerAssistantGlobalRoutes(admin *echo.Group) {
	admin.GET("/assistant/global", s.handleGetAssistantGlobal)
	admin.PUT("/assistant/global", s.handlePutAssistantGlobal)
	admin.GET("/assistant/prompts", s.handleGetAssistantPrompts)
	admin.PUT("/assistant/prompts", s.handlePutAssistantPrompts)
}

func defaultAssistantGlobalSettings() store.AssistantGlobalSettings {
	return store.AssistantGlobalSettings{
		ID: 1, InboundMergeWindowSec: 5, ReplyTotalTimeoutSec: 45, DecisionContextItems: 5,
		KeepOriginalText: true, MemoryRecallEnabled: true, HotWindowCompressEnabled: false,
		ProactiveIdleMinutes: 180, ProactiveQuietStart: 0, ProactiveQuietEnd: 8, ProactiveCheckIntervalSec: 60,
		TTSHTTPTimeoutSec: 20, TTSMaxTextLength: 500, TTSAPIBase: "https://openspeech.bytedance.com",
		TTSResourceID: "seed-tts-2.0", TTSAudioFormat: "ogg_opus", TTSSampleRate: 48000, TTSBitRate: 96000,
		TTSEmotionScale: 4, StickerFallbackFileIDs: []string{}, ModelRoles: []byte("{}"),
	}
}

func serializeAssistantGlobal(v store.AssistantGlobalSettings) map[string]any {
	roles := map[string]any{}
	if len(v.ModelRoles) > 0 {
		_ = json.Unmarshal(v.ModelRoles, &roles)
	}
	return map[string]any{
		"version":     v.Version,
		"model_roles": roles,
		"bot": map[string]any{
			"inbound_merge_window_sec":     v.InboundMergeWindowSec,
			"reply_total_timeout_sec":      v.ReplyTotalTimeoutSec,
			"decision_context_items":       v.DecisionContextItems,
			"keep_original_text":           v.KeepOriginalText,
			"memory_recall_enabled":        v.MemoryRecallEnabled,
			"hot_window_compress_enabled":  v.HotWindowCompressEnabled,
			"proactive_idle_minutes":       v.ProactiveIdleMinutes,
			"proactive_quiet_start":        v.ProactiveQuietStart,
			"proactive_quiet_end":          v.ProactiveQuietEnd,
			"proactive_check_interval_sec": v.ProactiveCheckIntervalSec,
		},
		"tts": map[string]any{
			"enabled":               v.TTSEnabled,
			"http_timeout_sec":      v.TTSHTTPTimeoutSec,
			"max_text_length":       v.TTSMaxTextLength,
			"api_base":              v.TTSAPIBase,
			"app_id":                v.TTSAppID,
			"app_key_configured":    strings.TrimSpace(v.TTSAppKeyEnc) != "",
			"access_key_configured": strings.TrimSpace(v.TTSAccessKeyEnc) != "",
			"resource_id":           v.TTSResourceID,
			"model":                 v.TTSModel,
			"speaker":               v.TTSSpeaker,
			"audio_format":          v.TTSAudioFormat,
			"sample_rate":           v.TTSSampleRate,
			"bit_rate":              v.TTSBitRate,
			"emotion":               v.TTSEmotion,
			"emotion_scale":         v.TTSEmotionScale,
			"speech_rate":           v.TTSSpeechRate,
			"loudness_rate":         v.TTSLoudnessRate,
			"silence_duration_ms":   v.TTSSilenceDurationMS,
		},
		"stickers":   map[string]any{"fallback_file_ids": v.StickerFallbackFileIDs},
		"updated_at": v.UpdatedAt,
	}
}

func (s *Server) handleGetAssistantGlobal(c echo.Context) error {
	if s.botService == nil || s.botService.Queries() == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "storage unavailable"})
	}
	settings, err := s.botService.Queries().GetAssistantGlobalSettings(c.Request().Context())
	if errors.Is(err, pgx.ErrNoRows) {
		settings = defaultAssistantGlobalSettings()
		err = nil
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load assistant global settings failed"})
	}
	return c.JSON(http.StatusOK, serializeAssistantGlobal(settings))
}

type assistantGlobalWrite struct {
	ExpectedVersion int64                                     `json:"expected_version"`
	ModelRoles      map[string]store.AssistantModelRoleConfig `json:"model_roles"`
	Bot             struct {
		InboundMergeWindowSec     *float64 `json:"inbound_merge_window_sec"`
		ReplyTotalTimeoutSec      *float64 `json:"reply_total_timeout_sec"`
		DecisionContextItems      *int32   `json:"decision_context_items"`
		KeepOriginalText          *bool    `json:"keep_original_text"`
		MemoryRecallEnabled       *bool    `json:"memory_recall_enabled"`
		HotWindowCompressEnabled  *bool    `json:"hot_window_compress_enabled"`
		ProactiveIdleMinutes      *int32   `json:"proactive_idle_minutes"`
		ProactiveQuietStart       *int32   `json:"proactive_quiet_start"`
		ProactiveQuietEnd         *int32   `json:"proactive_quiet_end"`
		ProactiveCheckIntervalSec *float64 `json:"proactive_check_interval_sec"`
	} `json:"bot"`
	TTS struct {
		Enabled           *bool    `json:"enabled"`
		HTTPTimeoutSec    *float64 `json:"http_timeout_sec"`
		MaxTextLength     *int32   `json:"max_text_length"`
		APIBase           *string  `json:"api_base"`
		AppID             *string  `json:"app_id"`
		AppKey            *string  `json:"app_key"`
		AccessKey         *string  `json:"access_key"`
		ResourceID        *string  `json:"resource_id"`
		Model             *string  `json:"model"`
		Speaker           *string  `json:"speaker"`
		AudioFormat       *string  `json:"audio_format"`
		SampleRate        *int32   `json:"sample_rate"`
		BitRate           *int32   `json:"bit_rate"`
		Emotion           *string  `json:"emotion"`
		EmotionScale      *int32   `json:"emotion_scale"`
		SpeechRate        *int32   `json:"speech_rate"`
		LoudnessRate      *int32   `json:"loudness_rate"`
		SilenceDurationMS *int32   `json:"silence_duration_ms"`
	} `json:"tts"`
	Stickers struct {
		FallbackFileIDs []string `json:"fallback_file_ids"`
	} `json:"stickers"`
}

func (s *Server) handlePutAssistantGlobal(c echo.Context) error {
	admin, _ := currentAdmin(c)
	if s.botService == nil || s.botService.Queries() == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "storage unavailable"})
	}
	var req assistantGlobalWrite
	if err := bindAssistantJSON(c, &req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid assistant global settings"})
	}
	current, err := s.botService.Queries().GetAssistantGlobalSettings(c.Request().Context())
	if errors.Is(err, pgx.ErrNoRows) {
		current = defaultAssistantGlobalSettings()
		err = nil
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load assistant global settings failed"})
	}
	arg := store.UpsertAssistantGlobalSettingsParams{
		ExpectedVersion: req.ExpectedVersion, ModelRoles: current.ModelRoles,
		InboundMergeWindowSec: current.InboundMergeWindowSec, ReplyTotalTimeoutSec: current.ReplyTotalTimeoutSec,
		DecisionContextItems: current.DecisionContextItems, KeepOriginalText: current.KeepOriginalText,
		MemoryRecallEnabled: current.MemoryRecallEnabled, HotWindowCompressEnabled: current.HotWindowCompressEnabled,
		ProactiveIdleMinutes: current.ProactiveIdleMinutes, ProactiveQuietStart: current.ProactiveQuietStart,
		ProactiveQuietEnd: current.ProactiveQuietEnd, ProactiveCheckIntervalSec: current.ProactiveCheckIntervalSec,
		TTSEnabled: current.TTSEnabled, TTSHTTPTimeoutSec: current.TTSHTTPTimeoutSec, TTSMaxTextLength: current.TTSMaxTextLength,
		TTSAPIBase: current.TTSAPIBase, TTSAppID: current.TTSAppID, TTSAppKeyEnc: current.TTSAppKeyEnc, TTSAccessKeyEnc: current.TTSAccessKeyEnc,
		TTSResourceID: current.TTSResourceID, TTSModel: current.TTSModel, TTSSpeaker: current.TTSSpeaker, TTSAudioFormat: current.TTSAudioFormat,
		TTSSampleRate: current.TTSSampleRate, TTSBitRate: current.TTSBitRate, TTSEmotion: current.TTSEmotion, TTSEmotionScale: current.TTSEmotionScale,
		TTSSpeechRate: current.TTSSpeechRate, TTSLoudnessRate: current.TTSLoudnessRate, TTSSilenceDurationMS: current.TTSSilenceDurationMS,
		StickerFallbackFileIDs: current.StickerFallbackFileIDs, UpdatedBy: &admin.TelegramID,
	}
	if req.ModelRoles != nil {
		raw, encErr := json.Marshal(req.ModelRoles)
		if encErr != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid model_roles"})
		}
		if err := s.botService.Assistant().ValidateRoleConfigs(req.ModelRoles); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		arg.ModelRoles = raw
	}
	if req.Bot.InboundMergeWindowSec != nil {
		arg.InboundMergeWindowSec = *req.Bot.InboundMergeWindowSec
	}
	if req.Bot.ReplyTotalTimeoutSec != nil {
		arg.ReplyTotalTimeoutSec = *req.Bot.ReplyTotalTimeoutSec
	}
	if req.Bot.DecisionContextItems != nil {
		arg.DecisionContextItems = *req.Bot.DecisionContextItems
	}
	if req.Bot.KeepOriginalText != nil {
		arg.KeepOriginalText = *req.Bot.KeepOriginalText
	}
	if req.Bot.MemoryRecallEnabled != nil {
		arg.MemoryRecallEnabled = *req.Bot.MemoryRecallEnabled
	}
	if req.Bot.HotWindowCompressEnabled != nil {
		arg.HotWindowCompressEnabled = *req.Bot.HotWindowCompressEnabled
	}
	if req.Bot.ProactiveIdleMinutes != nil {
		arg.ProactiveIdleMinutes = *req.Bot.ProactiveIdleMinutes
	}
	if req.Bot.ProactiveQuietStart != nil {
		arg.ProactiveQuietStart = *req.Bot.ProactiveQuietStart
	}
	if req.Bot.ProactiveQuietEnd != nil {
		arg.ProactiveQuietEnd = *req.Bot.ProactiveQuietEnd
	}
	if req.Bot.ProactiveCheckIntervalSec != nil {
		arg.ProactiveCheckIntervalSec = *req.Bot.ProactiveCheckIntervalSec
	}
	if req.TTS.Enabled != nil {
		arg.TTSEnabled = *req.TTS.Enabled
	}
	if req.TTS.HTTPTimeoutSec != nil {
		arg.TTSHTTPTimeoutSec = *req.TTS.HTTPTimeoutSec
	}
	if req.TTS.MaxTextLength != nil {
		arg.TTSMaxTextLength = *req.TTS.MaxTextLength
	}
	if req.TTS.APIBase != nil {
		arg.TTSAPIBase = strings.TrimSpace(*req.TTS.APIBase)
	}
	if req.TTS.AppID != nil {
		arg.TTSAppID = strings.TrimSpace(*req.TTS.AppID)
	}
	if req.TTS.AppKey != nil && strings.TrimSpace(*req.TTS.AppKey) != "" {
		enc, encErr := ai.EncryptAPIKey(strings.TrimSpace(*req.TTS.AppKey))
		if encErr != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "encrypt tts app_key failed"})
		}
		arg.TTSAppKeyEnc = enc
	}
	if req.TTS.AccessKey != nil && strings.TrimSpace(*req.TTS.AccessKey) != "" {
		enc, encErr := ai.EncryptAPIKey(strings.TrimSpace(*req.TTS.AccessKey))
		if encErr != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "encrypt tts access_key failed"})
		}
		arg.TTSAccessKeyEnc = enc
	}
	if req.TTS.ResourceID != nil {
		arg.TTSResourceID = strings.TrimSpace(*req.TTS.ResourceID)
	}
	if req.TTS.Model != nil {
		arg.TTSModel = strings.TrimSpace(*req.TTS.Model)
	}
	if req.TTS.Speaker != nil {
		arg.TTSSpeaker = strings.TrimSpace(*req.TTS.Speaker)
	}
	if req.TTS.AudioFormat != nil {
		arg.TTSAudioFormat = strings.TrimSpace(*req.TTS.AudioFormat)
	}
	if req.TTS.SampleRate != nil {
		arg.TTSSampleRate = *req.TTS.SampleRate
	}
	if req.TTS.BitRate != nil {
		arg.TTSBitRate = *req.TTS.BitRate
	}
	if req.TTS.Emotion != nil {
		arg.TTSEmotion = strings.TrimSpace(*req.TTS.Emotion)
	}
	if req.TTS.EmotionScale != nil {
		arg.TTSEmotionScale = *req.TTS.EmotionScale
	}
	if req.TTS.SpeechRate != nil {
		arg.TTSSpeechRate = *req.TTS.SpeechRate
	}
	if req.TTS.LoudnessRate != nil {
		arg.TTSLoudnessRate = *req.TTS.LoudnessRate
	}
	if req.TTS.SilenceDurationMS != nil {
		arg.TTSSilenceDurationMS = *req.TTS.SilenceDurationMS
	}
	if req.Stickers.FallbackFileIDs != nil {
		arg.StickerFallbackFileIDs = req.Stickers.FallbackFileIDs
	}
	if arg.InboundMergeWindowSec < 0 || arg.InboundMergeWindowSec > 60 || arg.ReplyTotalTimeoutSec < 5 || arg.ReplyTotalTimeoutSec > 120 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "bot timing out of range"})
	}
	updated, err := s.botService.Queries().UpsertAssistantGlobalSettings(c.Request().Context(), arg)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusConflict, map[string]any{"error": "assistant global settings version conflict", "current": serializeAssistantGlobal(current)})
	}
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "save assistant global settings failed"})
	}
	return c.JSON(http.StatusOK, serializeAssistantGlobal(updated))
}

func serializePromptMap(items []store.AssistantPromptOverride) map[string]string {
	out := map[string]string{"persona": "", "casual": "", "decision": "", "proactive_topic": "", "style_distill": ""}
	for _, item := range items {
		out[item.PromptKey] = item.Content
	}
	return out
}

func (s *Server) handleGetAssistantPrompts(c echo.Context) error {
	if s.botService == nil || s.botService.Queries() == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "storage unavailable"})
	}
	items, err := s.botService.Queries().ListAssistantPromptOverrides(c.Request().Context(), 0)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load prompts failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{"chat_id": 0, "prompts": serializePromptMap(items), "defaults": map[string]string{
		"persona": "内嵌人格", "casual": "内嵌日常对话", "decision": "内嵌回复决策", "proactive_topic": "内嵌主动话题", "style_distill": "内嵌风格提炼",
	}})
}

type assistantPromptWrite struct {
	Prompts map[string]string `json:"prompts"`
}

func savePromptMap(q *store.Queries, ctx echo.Context, chatID int64, prompts map[string]string) error {
	for key, content := range prompts {
		if !store.ValidAssistantPromptKey(key) {
			return fmt.Errorf("unknown prompt key")
		}
		content = strings.TrimSpace(content)
		if content == "" {
			if err := q.DeleteAssistantPromptOverride(ctx.Request().Context(), chatID, key); err != nil {
				return err
			}
			continue
		}
		if _, err := q.UpsertAssistantPromptOverride(ctx.Request().Context(), chatID, key, content); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) handlePutAssistantPrompts(c echo.Context) error {
	if s.botService == nil || s.botService.Queries() == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "storage unavailable"})
	}
	var req assistantPromptWrite
	if err := bindAssistantJSON(c, &req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid prompts"})
	}
	if err := savePromptMap(s.botService.Queries(), c, 0, req.Prompts); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return s.handleGetAssistantPrompts(c)
}

func (s *Server) handleGetGroupAssistantPrompts(c echo.Context) error {
	_, chatID, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	items, err := s.botService.Queries().ListAssistantPromptOverrides(c.Request().Context(), chatID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load group prompts failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{"chat_id": chatID, "prompts": serializePromptMap(items)})
}

func (s *Server) handlePutGroupAssistantPrompts(c echo.Context) error {
	_, chatID, err := s.assistantAccess(c)
	if err != nil {
		return assistantAccessResponse(c, err)
	}
	var req assistantPromptWrite
	if err := bindAssistantJSON(c, &req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid prompts"})
	}
	if err := savePromptMap(s.botService.Queries(), c, chatID, req.Prompts); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return s.handleGetGroupAssistantPrompts(c)
}
