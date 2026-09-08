package bot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/store"
)

const (
	assistantTTSModeOff    = "off"
	assistantTTSModeOn     = "on"
	assistantTTSModeAlways = "always"
	assistantTTSMaxAudio   = 20 << 20
	assistantTTSMaxError   = 64 << 10
)

var assistantTTSHTTPClient = &http.Client{
	Timeout: 60 * time.Second,
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	},
}

type assistantTTSResult struct {
	OK         bool
	Audio      []byte
	Format     string
	Error      string
	Normalized string
}

type assistantTTSSynthesizer interface {
	Available() bool
	Synthesize(ctx context.Context, text string) assistantTTSResult
}

type doubaoTTSClient struct {
	enabled      bool
	apiBase      string
	appID        string
	appKey       string
	accessKey    string
	resourceID   string
	model        string
	speaker      string
	audioFormat  string
	sampleRate   int
	bitRate      int
	emotion      string
	emotionScale int
	speechRate   int
	loudnessRate int
	silenceMS    int
	timeout      time.Duration
	maxText      int
	httpClient   *http.Client
}

func normalizeAssistantTTSMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case assistantTTSModeOn, "enable", "enabled", "true", "1":
		return assistantTTSModeOn
	case assistantTTSModeAlways:
		return assistantTTSModeAlways
	default:
		return assistantTTSModeOff
	}
}

func assistantTTSConfigFromSettings(settings store.AssistantGlobalSettings) doubaoTTSClient {
	timeout := time.Duration(settings.TTSHTTPTimeoutSec * float64(time.Second))
	if timeout < time.Second {
		timeout = 20 * time.Second
	}
	maxText := int(settings.TTSMaxTextLength)
	if maxText <= 0 {
		maxText = 500
	}
	apiBase := strings.TrimSpace(settings.TTSAPIBase)
	if apiBase == "" {
		apiBase = "https://openspeech.bytedance.com"
	}
	resourceID := strings.TrimSpace(settings.TTSResourceID)
	if resourceID == "" {
		resourceID = "seed-tts-2.0"
	}
	format := strings.TrimSpace(settings.TTSAudioFormat)
	if format == "" {
		format = "ogg_opus"
	}
	appKey, accessKey := "", ""
	if strings.TrimSpace(settings.TTSAppKeyEnc) != "" {
		if plain, err := ai.DecryptAPIKey(settings.TTSAppKeyEnc); err == nil {
			appKey = plain
		}
	}
	if strings.TrimSpace(settings.TTSAccessKeyEnc) != "" {
		if plain, err := ai.DecryptAPIKey(settings.TTSAccessKeyEnc); err == nil {
			accessKey = plain
		}
	}
	return doubaoTTSClient{
		enabled: settings.TTSEnabled, apiBase: strings.TrimRight(apiBase, "/"), appID: strings.TrimSpace(settings.TTSAppID),
		appKey: appKey, accessKey: accessKey, resourceID: resourceID, model: strings.TrimSpace(settings.TTSModel),
		speaker: strings.TrimSpace(settings.TTSSpeaker), audioFormat: format, sampleRate: int(settings.TTSSampleRate),
		bitRate: int(settings.TTSBitRate), emotion: strings.TrimSpace(settings.TTSEmotion), emotionScale: int(settings.TTSEmotionScale),
		speechRate: int(settings.TTSSpeechRate), loudnessRate: int(settings.TTSLoudnessRate), silenceMS: int(settings.TTSSilenceDurationMS),
		timeout: timeout, maxText: maxText, httpClient: assistantTTSHTTPClient,
	}
}

func (c doubaoTTSClient) Available() bool {
	return c.enabled && c.apiBase != "" && c.appID != "" && c.accessKey != "" && c.resourceID != "" && c.speaker != ""
}

func assistantNormalizeTTSText(text string) string {
	cleaned := strings.TrimSpace(text)
	cleaned = strings.ReplaceAll(cleaned, "\r", "，")
	cleaned = strings.ReplaceAll(cleaned, "\n", "，")
	cleaned = strings.ReplaceAll(cleaned, "\t", "，")
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	return strings.Trim(cleaned, " ，。！？：、")
}

func (c doubaoTTSClient) Synthesize(ctx context.Context, text string) assistantTTSResult {
	if !c.Available() {
		return assistantTTSResult{Error: "tts_not_configured"}
	}
	normalized := assistantNormalizeTTSText(text)
	if normalized == "" {
		return assistantTTSResult{Error: "empty_text"}
	}
	if utf8.RuneCountInString(normalized) > c.maxText {
		return assistantTTSResult{Error: "text_too_long", Normalized: normalized}
	}
	requestID := newAssistantRequestID()
	audioParams := map[string]any{"format": c.audioFormat, "sample_rate": c.sampleRate}
	if c.bitRate > 0 {
		audioParams["bit_rate"] = c.bitRate
	}
	if c.emotion != "" {
		audioParams["emotion"] = c.emotion
		if c.emotionScale > 0 {
			audioParams["emotion_scale"] = c.emotionScale
		}
	}
	if c.speechRate != 0 {
		audioParams["speech_rate"] = c.speechRate
	}
	if c.loudnessRate != 0 {
		audioParams["loudness_rate"] = c.loudnessRate
	}
	additions := map[string]any{"disable_markdown_filter": true, "cache_config": map[string]any{"text_type": 1, "use_cache": true}}
	if c.silenceMS > 0 {
		additions["silence_duration"] = c.silenceMS
	}
	additionRaw, _ := json.Marshal(additions)
	reqParams := map[string]any{"text": normalized, "speaker": c.speaker, "audio_params": audioParams, "additions": string(additionRaw)}
	if c.model != "" {
		reqParams["model"] = c.model
	}
	payload, err := json.Marshal(map[string]any{
		"user": map[string]any{"uid": c.appID}, "namespace": "UnidirectionalTTS", "req_params": reqParams,
	})
	if err != nil {
		return assistantTTSResult{Error: "marshal_failed", Normalized: normalized}
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.apiBase+"/api/v3/tts/unidirectional", bytes.NewReader(payload))
	if err != nil {
		return assistantTTSResult{Error: "request_failed", Normalized: normalized}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Api-App-Id", c.appID)
	httpReq.Header.Set("X-Api-Access-Key", c.accessKey)
	httpReq.Header.Set("X-Api-Resource-Id", c.resourceID)
	httpReq.Header.Set("X-Api-Request-Id", requestID)
	if c.appKey != "" {
		httpReq.Header.Set("X-Api-App-Key", c.appKey)
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return assistantTTSResult{Error: "http_failed", Normalized: normalized}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, assistantTTSMaxError+1))
		_ = raw
		return assistantTTSResult{Error: fmt.Sprintf("http_%d", resp.StatusCode), Normalized: normalized}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, assistantTTSMaxAudio+4096))
	if err != nil {
		return assistantTTSResult{Error: "read_failed", Normalized: normalized}
	}
	audio, parseErr := parseDoubaoTTSAudio(body)
	if parseErr != "" {
		return assistantTTSResult{Error: parseErr, Normalized: normalized}
	}
	if len(audio) == 0 {
		return assistantTTSResult{Error: "empty_audio", Normalized: normalized}
	}
	return assistantTTSResult{OK: true, Audio: audio, Format: c.audioFormat, Normalized: normalized}
}

func parseDoubaoTTSAudio(body []byte) ([]byte, string) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var parts [][]byte
	total := 0
	for {
		var item map[string]any
		if err := decoder.Decode(&item); err != nil {
			if err == io.EOF {
				break
			}
			if len(parts) == 0 {
				return nil, "invalid_json"
			}
			break
		}
		code, _ := item["code"].(float64)
		if int(code) != 0 {
			return nil, "tts_code"
		}
		data, _ := item["data"].(string)
		if data == "" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, "invalid_audio"
		}
		total += len(decoded)
		if total > assistantTTSMaxAudio {
			return nil, "audio_too_large"
		}
		parts = append(parts, decoded)
	}
	return bytes.Join(parts, nil), ""
}

func (a *GroupAssistant) ttsSynthesizer(settings store.AssistantGlobalSettings) assistantTTSSynthesizer {
	if a != nil && a.ttsSynth != nil {
		return a.ttsSynth
	}
	client := assistantTTSConfigFromSettings(settings)
	return client
}

func (a *GroupAssistant) assistantTTSReady(settings store.AssistantGlobalSettings) bool {
	return a.ttsSynthesizer(settings).Available()
}

func (s *Service) sendAssistantVoice(ctx context.Context, chat tele.Recipient, audio []byte, format string, opts *tele.SendOptions) (*tele.Message, error) {
	if s == nil || len(audio) == 0 {
		return nil, fmt.Errorf("empty audio")
	}
	mime := "audio/ogg"
	if strings.Contains(strings.ToLower(format), "mp3") {
		mime = "audio/mpeg"
	}
	voice := &tele.Voice{File: tele.FromReader(bytes.NewReader(audio)), MIME: mime}
	return s.sendThrottled(ctx, chat, voice, opts)
}

func assistantTTSPreferenceBlock(mode string, ready bool) string {
	mode = normalizeAssistantTTSMode(mode)
	if mode == assistantTTSModeOff || !ready {
		return ""
	}
	lines := []string{"[GROUP_TTS_PREFERENCE]", "tts_mode: " + mode, "tts_service_ready: yes"}
	if mode == assistantTTSModeAlways {
		lines = append(lines,
			"This group is configured for voice-first replies.",
			"For normal outgoing replies, prefer calling doubao_tts instead of returning plain text.",
			"If doubao_tts is available and a spoken reply is possible, do not default to text-only output.",
			"If you use doubao_tts, its text must be the exact final spoken reply, with no extra action explanation.",
		)
		return strings.Join(lines, "\n")
	}
	lines = append(lines,
		"This group has TTS enabled. You have the ability to send voice messages via doubao_tts.",
		"Use your own judgment to decide when voice is more natural and engaging than text.",
		"Prefer voice for: greetings, comfort, goodnights, celebrations, playful banter, emotional reactions, companion-style replies, clingy or cute interactions with the owner, short readings or broadcasts, and any reply where spoken delivery clearly adds warmth or personality.",
		"Keep text for: factual answers, link-heavy or list-heavy replies, management operations, search result summaries, long explanations, and structured content.",
		"When in doubt between voice and text for a short, emotional, or conversational reply, lean toward voice.",
		"If you use doubao_tts, its text must be the exact final spoken reply, with no extra action explanation.",
	)
	return strings.Join(lines, "\n")
}

func (a *GroupAssistant) logTTSSkip(chatID int64, reason string) {
	if a == nil || a.logger == nil {
		return
	}
	a.logger.Info("群助手不发语音", zap.Int64("chat_id", chatID), zap.String("reason", reason))
}

func (a *GroupAssistant) executeDoubaoTTSTool(ctx context.Context, chatID int64, policy store.GroupAssistantPolicy, args map[string]any) (string, error) {
	if err := validateAssistantToolKeys(args, "text", "delivery_mode", "emotion", "emotion_scale", "context", "speech_rate", "loudness_rate"); err != nil {
		return assistantToolError("invalid_arguments", err.Error()), err
	}
	if a == nil || a.service == nil {
		return assistantToolError("missing_context", "当前上下文不支持发送语音"), fmt.Errorf("missing service")
	}
	settings := a.loadRuntimeSettings(ctx)
	if assistantRuntime(ctx) != nil {
		settings = assistantRuntime(ctx).settings
	}
	mode := normalizeAssistantTTSMode(policy.TTSMode)
	if mode == assistantTTSModeOff {
		a.logTTSSkip(chatID, "当前群语音已关闭")
		return assistantToolError("tts_disabled", "当前群未开启语音"), fmt.Errorf("tts disabled")
	}
	synth := a.ttsSynthesizer(settings)
	if !synth.Available() {
		if !settings.TTSEnabled && a.ttsSynth == nil {
			a.logTTSSkip(chatID, "当前群语音已关闭或全局未启用")
			return assistantToolError("tts_disabled", "当前群未开启语音"), fmt.Errorf("tts disabled")
		}
		a.logTTSSkip(chatID, "豆包语音未配置密钥，不能假装发送成功")
		return assistantToolError("tts_not_configured", "语音服务未配置密钥"), fmt.Errorf("tts not configured")
	}
	text := assistantStringArg(args, "text")
	if text == "" && assistantRuntime(ctx) != nil {
		text = assistantRuntime(ctx).current
	}
	result := synth.Synthesize(ctx, text)
	if !result.OK {
		a.logTTSSkip(chatID, "语音合成失败："+result.Error)
		return assistantToolError("tts_failed", "语音合成失败"), fmt.Errorf("%s", result.Error)
	}
	delivery := strings.ToLower(assistantStringArg(args, "delivery_mode"))
	opts := &tele.SendOptions{}
	if assistantRuntime(ctx) != nil && assistantRuntime(ctx).msg != nil {
		opts.ThreadID = assistantRuntime(ctx).msg.ThreadID
		if delivery != "message" {
			opts.ReplyTo = assistantRuntime(ctx).msg
		}
	}
	if rt := assistantRuntime(ctx); rt != nil && a.queries != nil && !a.service.assistantPreSendReview(ctx, rt.msg, rt.mode) {
		return assistantToolError("canceled", "消息已失效或助手已关闭"), context.Canceled
	}
	if err := assistantReserveMedia(ctx, "voice:"+result.Normalized); err != nil {
		return assistantToolError("already_attempted", err.Error()), err
	}
	if rt := assistantRuntime(ctx); rt != nil {
		rt.deliveryUncertain = true
	}
	sent, err := a.service.sendAssistantVoice(ctx, &tele.Chat{ID: chatID}, result.Audio, result.Format, opts)
	if err == nil && (sent == nil || sent.ID == 0) {
		err = fmt.Errorf("语音发送未确认")
	}
	if err != nil {
		a.logTTSSkip(chatID, "语音发送失败")
		return assistantToolError("send_failed", "语音发送失败"), err
	}
	if rt := assistantRuntime(ctx); rt != nil {
		rt.voiceSent = true
		rt.deliveryUncertain = false
		if sent != nil && rt.msg != nil {
			if err := a.service.storeAssistantDelivery(ctx, rt.msg, policy, result.Normalized, sent, opts); err != nil {
				return assistantToolError("storage_failed", "语音已发送但归档失败，不得重发"), err
			}
		}
	}
	return assistantJSONResult(map[string]any{"ok": true, "text": result.Normalized, "delivery_mode": delivery, "tts_sent": true}), nil
}
