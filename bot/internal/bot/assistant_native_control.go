package bot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

func (n *NativeAssistant) backgroundGrant(ctx context.Context, groupID int64, topicID int32) (string, error) {
	generation, err := n.a.queries.NativeScopeGeneration(ctx, groupID, topicID)
	if err != nil {
		return "", err
	}
	return n.sign(nativeGrant{GroupID: groupID, TopicID: topicID, Generation: generation, Background: true, Expires: time.Now().Add(24 * time.Hour).Unix()}), nil
}

func (n *NativeAssistant) bootstrap(ctx context.Context) (any, error) {
	scopes, err := n.a.queries.NativeAssistantScopes(ctx)
	if err != nil {
		return nil, err
	}
	result := []any{}
	for _, scope := range scopes {
		grant, err := n.backgroundGrant(ctx, scope.ChatID, scope.ThreadID)
		if err != nil {
			return nil, err
		}
		models, err := n.roleMetadata(ctx, scope.ChatID)
		if err != nil {
			return nil, err
		}
		result = append(result, map[string]any{"group_id": scope.ChatID, "topic_id": scope.ThreadID, "background_grant": grant, "chat_enabled": scope.ChatEnabled, "models": models})
	}
	tts := assistantTTSConfigFromSettings(n.a.loadRuntimeSettings(ctx))
	return map[string]any{"scopes": result, "bot_user": n.a.service.bot.Me, "tts": map[string]any{
		"ready": tts.Available(), "enabled": tts.enabled, "resource_id": tts.resourceID, "model": tts.model,
		"speaker": tts.speaker, "audio_format": tts.audioFormat, "sample_rate": tts.sampleRate, "bit_rate": tts.bitRate,
		"emotion": tts.emotion, "emotion_scale": tts.emotionScale, "speech_rate": tts.speechRate, "loudness_rate": tts.loudnessRate,
		"silence_duration_ms": tts.silenceMS, "http_timeout_sec": tts.timeout.Seconds(), "max_text_length": tts.maxText,
	}}, nil
}
func (n *NativeAssistant) roleMetadata(ctx context.Context, groupID int64) (map[string]any, error) {
	pool, err := n.a.loadPool(ctx, groupID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for source, task := range map[string]string{"main": "chat", "decision": "decision", "compress": "compress", "vision": "vision", "embed": "vector"} {
		ids, _ := assistantTaskEndpointIDs(pool, task)
		if len(ids) == 0 {
			continue
		}
		ep, ok := endpointByID(pool, ids[0])
		if !ok {
			continue
		}
		ref, ok := normalizeAssistantModelRef(ep.ModelRef)
		if !ok {
			continue
		}
		metadata := map[string]any{"model": assistantModelName(ref), "space_id": ref.String()}
		params := pool.TaskAssignments[task]
		if params.MaxTokens > 0 {
			metadata["max_tokens"] = params.MaxTokens
		}
		if params.Temperature != nil {
			metadata["temperature"] = *params.Temperature
		}
		if ep.TimeoutMs > 0 {
			metadata["timeout_sec"] = float64(ep.TimeoutMs) / 1000
		}
		out[source] = metadata
	}
	return out, nil
}

// NativeAssistantControl is called ONLY by the existing authenticated CG admin
// routes. The caller chooses a constant path after its own group/role check.
func (s *Service) NativeAssistantControl(ctx context.Context, path string, groupID int64, values map[string]any) (json.RawMessage, error) {
	if s.assistant == nil || s.assistant.native == nil {
		if !s.NativeAssistantSelected() && (path == "/config/read" || path == "/groups/read") {
			return json.RawMessage(`{"engine":"legacy"}`), nil
		}
		return nil, errors.New("native assistant is not active")
	}
	switch path {
	case "/config/read", "/config/write", "/groups/read", "/groups/write", "/memory/list", "/memory/add", "/memory/delete", "/memory/replace":
	default:
		return nil, errors.New("native control path denied")
	}
	if values == nil {
		values = map[string]any{}
	}
	if groupID != 0 {
		topicID := int64(0)
		if strings.HasPrefix(path, "/memory/") {
			topicID = nativeInt(values["topic_id"])
		}
		if topicID < 0 || topicID > 2147483647 {
			return nil, errors.New("invalid topic id")
		}
		if path == "/groups/write" {
			if err := s.queries.EnsureNativeAssistantPolicy(ctx, groupID); err != nil {
				return nil, err
			}
		}
		grant, err := s.assistant.native.backgroundGrant(ctx, groupID, int32(topicID))
		if err != nil {
			return nil, err
		}
		values["group_id"], values["background_grant"] = groupID, grant
	}
	return s.assistant.native.request(ctx, path, values)
}
func (s *Service) NativeAssistantSelected() bool {
	return s != nil && (s.cfg.AssistantEngine == "native" || s.cfg.AssistantEngine == "paused")
}
func (s *Service) NativeAssistantEnabled() bool {
	return s != nil && s.assistant != nil && s.assistant.native != nil
}

func (n *NativeAssistant) tts(ctx context.Context, g nativeGrant, raw json.RawMessage) (any, error) {
	c := assistantTTSConfigFromSettings(n.a.loadRuntimeSettings(ctx))
	if !c.Available() {
		return nil, errors.New("tts not configured")
	}
	var req struct {
		Payload   map[string]any `json:"payload"`
		RequestID string         `json:"request_id"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if len(req.RequestID) > 100 || req.Payload == nil {
		return nil, errors.New("invalid TTS request")
	}
	payload, err := json.Marshal(req.Payload)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(callCtx, http.MethodPost, strings.TrimRight(c.apiBase, "/")+"/api/v3/tts/unidirectional", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Api-App-Id", c.appID)
	request.Header.Set("X-Api-Access-Key", c.accessKey)
	request.Header.Set("X-Api-Resource-Id", c.resourceID)
	request.Header.Set("X-Api-Request-Id", req.RequestID)
	request.Header.Set("X-Control-Require-Usage-Tokens-Return", "text_words")
	if c.appKey != "" {
		request.Header.Set("X-Api-App-Key", c.appKey)
	}
	// Source constructs every payload and owns emotional retry/partial fallback.
	// CG neither synthesizes nor retries; this is a single credentialed transport.
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, errors.New("TTS transport unavailable")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (32<<20)+1))
	if err != nil || len(data) > 32<<20 {
		return nil, errors.New("TTS response exceeds limit")
	}
	return map[string]any{"status": response.StatusCode, "headers": map[string]string{"X-Tt-Logid": response.Header.Get("X-Tt-Logid")}, "body": base64.StdEncoding.EncodeToString(data)}, nil
}
