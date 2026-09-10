package bot

// This file is transport and routing only. Python's pinned source owns decisions,
// pending queues, tools, memory, speech style and delivery plans.
import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
)

type nativeGrant struct {
	GroupID    int64  `json:"g"`
	TopicID    int32  `json:"t"`
	ActorID    int64  `json:"a"`
	Generation int64  `json:"v"`
	EventID    int64  `json:"e"`
	MessageID  int64  `json:"m"`
	Background bool   `json:"b"`
	StyleOnly  bool   `json:"s,omitempty"`
	Expires    int64  `json:"x"`
	CallbackID string `json:"c,omitempty"`
}
type nativeEnvelope struct {
	GroupID int64           `json:"group_id"`
	TopicID int32           `json:"topic_id"`
	Grant   string          `json:"grant"`
	Turn    string          `json:"turn"`
	Payload json.RawMessage `json:"payload"`
}
type nativeTurn struct {
	mu     sync.Mutex
	lease  *assistantLease
	pinned bool
	used   time.Time
}
type nativeDownload struct {
	group   int64
	topic   int32
	path    string
	expires time.Time
}
type NativeAssistant struct {
	a         *GroupAssistant
	client    *http.Client
	mu        sync.Mutex
	turns     map[string]*nativeTurn
	locks     map[string]*sync.Mutex
	downloads map[string]nativeDownload
}

func newNativeAssistant(a *GroupAssistant) *NativeAssistant {
	n := &NativeAssistant{a: a, client: &http.Client{Timeout: 120 * time.Second}, turns: map[string]*nativeTurn{}, locks: map[string]*sync.Mutex{}, downloads: map[string]nativeDownload{}}
	a.service.wg.Add(1)
	go func() {
		defer a.service.wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-a.ctx.Done():
				n.releaseTurns(true)
				return
			case <-ticker.C:
				n.releaseTurns(false)
				if a.queries == nil {
					continue
				}
				scopes, err := a.queries.PendingNativeAssistantScopes(a.ctx)
				if err != nil {
					continue
				}
				for _, scope := range scopes {
					lock := n.scopeLock(scope.ChatID, scope.ThreadID)
					if !lock.TryLock() {
						continue
					}
					_ = n.flushScope(a.ctx, scope.ChatID, scope.ThreadID)
					lock.Unlock()
				}

			}
		}
	}()
	return n
}
func (n *NativeAssistant) releaseTurns(all bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for key, t := range n.turns {
		if !t.mu.TryLock() {
			continue
		}
		if all || time.Since(t.used) > 3*time.Minute {
			if t.lease != nil {
				t.lease.finish(false, context.Canceled)
			}
			delete(n.turns, key)
		}
		t.mu.Unlock()
	}
}
func (n *NativeAssistant) scopeLock(group int64, topic int32) *sync.Mutex {
	key := fmt.Sprintf("%d:%d", group, topic)
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.locks[key] == nil {
		n.locks[key] = &sync.Mutex{}
	}
	return n.locks[key]
}
func (n *NativeAssistant) sign(g nativeGrant) string {
	raw, _ := json.Marshal(g)
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(n.a.service.cfg.JWTSecret))
	mac.Write([]byte("clawguard-native-v1:" + encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (n *NativeAssistant) verify(token string) (nativeGrant, error) {
	var g nativeGrant
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return g, errors.New("invalid capability")
	}
	mac := hmac.New(sha256.New, []byte(n.a.service.cfg.JWTSecret))
	mac.Write([]byte("clawguard-native-v1:" + parts[0]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(mac.Sum(nil), sig) {
		return g, errors.New("invalid capability")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return g, err
	}
	if err = json.Unmarshal(raw, &g); err != nil {
		return g, err
	}
	if g.Expires < time.Now().Unix() || g.GroupID >= 0 || g.TopicID < 0 {
		return g, errors.New("expired capability")
	}
	return g, nil
}
func (n *NativeAssistant) request(ctx context.Context, path string, value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(n.a.service.cfg.AssistantNativeURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+n.a.service.cfg.AssistantBrokerSecret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, errors.New("原生助手不可用；未回退或重复处理")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("原生助手请求失败 HTTP %d；未回退", resp.StatusCode)
	}
	return body, nil
}
func nativeMessage(msg *tele.Message) (map[string]any, error) {
	raw, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err = json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	// telebot serializes absent optional fields as null; aiogram accepts these.
	// Photo must be a Bot API array on both the current message and a visible reply.
	nativeNormalizeTelegramMedia(value)
	return value, nil
}

func nativeNormalizeTelegramMedia(value map[string]any) {
	if value == nil {
		return
	}
	if photo, ok := value["photo"].(map[string]any); ok {
		value["photo"] = []any{nativePhotoSize(photo)}
	} else if photos, ok := value["photo"].([]any); ok {
		out := make([]any, 0, len(photos))
		for _, item := range photos {
			if m, ok := item.(map[string]any); ok {
				out = append(out, nativePhotoSize(m))
			}
		}
		value["photo"] = out
	}
	for _, key := range []string{"sticker", "voice", "audio", "document", "video", "animation", "video_note"} {
		if media, ok := value[key].(map[string]any); ok {
			nativeStripLocalFileFields(media)
			if thumb, ok := media["thumbnail"].(map[string]any); ok {
				nativeStripLocalFileFields(thumb)
			}
		}
	}
	if reply, ok := value["reply_to_message"].(map[string]any); ok {
		nativeNormalizeTelegramMedia(reply)
	}
}

func nativePhotoSize(photo map[string]any) map[string]any {
	nativeStripLocalFileFields(photo)
	out := map[string]any{}
	for _, key := range []string{"file_id", "file_unique_id", "width", "height", "file_size"} {
		if v, ok := photo[key]; ok && v != nil {
			out[key] = v
		}
	}
	return out
}

func nativeStripLocalFileFields(media map[string]any) {
	delete(media, "file_path")
	delete(media, "file_local")
	delete(media, "file_url")
}
func (n *NativeAssistant) Incoming(ctx context.Context, msg *tele.Message, edited bool, eligibility assistantEligibility, keyword, admin bool) error {
	policy, err := n.a.Policy(ctx, msg.Chat.ID)
	if err != nil {
		return err
	}
	styleOnly := false
	if !policy.ChatEnabled && !edited {
		exists, checkErr := n.a.queries.NativeScopeExists(ctx, msg.Chat.ID)
		if checkErr != nil {
			return checkErr
		}
		if !exists || strings.TrimSpace(msg.Text) == "" {
			return nil
		}
		grant, grantErr := n.backgroundGrant(ctx, msg.Chat.ID, 0)
		if grantErr != nil {
			return grantErr
		}
		raw, readErr := n.request(ctx, "/groups/read", map[string]any{"group_id": msg.Chat.ID, "background_grant": grant})
		if readErr != nil {
			return readErr
		}
		var current struct {
			Settings struct {
				Style struct {
					Target int64 `json:"target_user_id"`
				} `json:"speech_style"`
			} `json:"settings"`
		}
		if json.Unmarshal(raw, &current) != nil || current.Settings.Style.Target == 0 || current.Settings.Style.Target != msg.Sender.ID {
			return nil
		}
		styleOnly = true
	}
	if eligibility != assistantEligibilityEligible && !edited {
		return nil
	}
	lock := n.scopeLock(msg.Chat.ID, int32(msg.ThreadID))
	lock.Lock()
	defer lock.Unlock()
	copyMsg := *msg
	if msg.ReplyTo != nil {
		visible, e := n.sourceVisible(ctx, msg.Chat.ID, int32(msg.ThreadID), int64(msg.ReplyTo.ID))
		if e != nil || !visible || msg.ReplyTo.ThreadID != msg.ThreadID {
			copyMsg.ReplyTo = nil
		}
	}
	value, err := nativeMessage(&copyMsg)
	if err != nil {
		return err
	}
	kind := "message"
	if edited {
		kind = "edit"
	}
	if eligibility != assistantEligibilityEligible {
		kind = "invalidate"
		value = map[string]any{"message_id": msg.ID, "chat": map[string]any{"id": msg.Chat.ID}, "message_thread_id": msg.ThreadID}
	}
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	revision := hex.EncodeToString(sum[:])
	payload := map[string]any{"message": value, "style_only": styleOnly, "is_admin": admin, "is_owner": n.a.assistantIsOwner(ctx, msg.Sender.ID), "bot_user": n.a.service.bot.Me, "chat_enabled": policy.ChatEnabled, "keyword_replied": keyword, "approved": eligibility == assistantEligibilityEligible}
	raw, _ = json.Marshal(payload)
	_, err = n.a.queries.EnqueueNativeAssistantEvent(ctx, store.NativeAssistantEvent{ChatID: msg.Chat.ID, ThreadID: int32(msg.ThreadID), MessageID: int64(msg.ID), Revision: revision, Kind: kind, Payload: raw})
	if err != nil {
		return err
	}
	return n.flushScope(ctx, msg.Chat.ID, int32(msg.ThreadID))
}
func (n *NativeAssistant) flushScope(ctx context.Context, groupID int64, topic int32) error {
	events, err := n.a.queries.NativeAssistantEvents(ctx, groupID, topic)
	if err != nil {
		return err
	}
	for _, event := range events {
		current, currentErr := n.a.queries.NativeEventIsCurrent(ctx, event)
		if currentErr != nil {
			return currentErr
		}
		if !current {
			if err := n.a.queries.SetNativeAssistantEventStatus(ctx, event.ID, "done"); err != nil {
				return err
			}
			continue
		}
		generation, generationErr := n.a.queries.NativeScopeGeneration(ctx, groupID, topic)
		if generationErr != nil {
			return generationErr
		}
		var value map[string]any
		if err = json.Unmarshal(event.Payload, &value); err != nil {
			return err
		}
		var msg struct {
			From struct {
				ID int64 `json:"id"`
			} `json:"from"`
		}
		raw, _ := json.Marshal(value["message"])
		_ = json.Unmarshal(raw, &msg)
		styleOnly, _ := value["style_only"].(bool)
		grant := nativeGrant{GroupID: groupID, TopicID: topic, ActorID: msg.From.ID, StyleOnly: styleOnly, Generation: generation, EventID: event.ID, MessageID: event.MessageID, Expires: time.Now().Add(24 * time.Hour).Unix()}
		value["group_id"], value["topic_id"], value["revision"], value["kind"] = groupID, topic, event.Revision, event.Kind
		value["grant"], value["turn"] = n.sign(grant), strconv.FormatInt(event.ID, 10)
		grant.Background = true
		grant.ActorID = 0
		grant.MessageID = 0
		value["background_grant"] = n.sign(grant)
		rootGrant, rootErr := n.backgroundGrant(ctx, groupID, 0)
		if rootErr != nil {
			return rootErr
		}
		value["group_background_grant"] = rootGrant
		models, metadataErr := n.roleMetadata(ctx, groupID)
		if metadataErr != nil {
			return metadataErr
		}
		value["models"] = models
		if event.Kind == "edit" {
			known, knownErr := n.a.queries.NativeOriginApproved(ctx, groupID, topic, event.MessageID)
			if knownErr != nil {
				return knownErr
			}
			value["known_origin"] = known
		}
		response, callErr := n.request(ctx, "/events", value)
		if callErr != nil {
			err = callErr
			return err
		}
		status := "accepted"
		var outcome struct {
			Done bool `json:"done"`
		}
		_ = json.Unmarshal(response, &outcome)
		if event.Kind != "message" || outcome.Done {
			status = "done"
		}
		if err = n.a.queries.SetNativeAssistantEventStatus(ctx, event.ID, status); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) NativeAssistantHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s != nil && strings.HasSuffix(r.URL.Path, "/state") {
			secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if r.Method != http.MethodPost || len(s.cfg.AssistantBrokerSecret) < 32 || subtle.ConstantTimeCompare([]byte(secret), []byte(s.cfg.AssistantBrokerSecret)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"engine": s.cfg.AssistantEngine})
			return
		}
		if s == nil || s.assistant == nil || s.assistant.native == nil {
			http.NotFound(w, r)
			return
		}
		s.assistant.native.ServeHTTP(w, r)
	})
}
func (n *NativeAssistant) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if r.Method != http.MethodPost || subtle.ConstantTimeCompare([]byte(secret), []byte(n.a.service.cfg.AssistantBrokerSecret)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	op := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	if op == "migration-check" {
		var body struct {
			Profiles map[string]nativeMigrationProfile `json:"profiles"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body) != nil {
			http.Error(w, "invalid migration check", 400)
			return
		}
		if err := n.verifyMigrationProfiles(r.Context(), body.Profiles); err != nil {
			http.Error(w, "migration state changed; export latest paused state", 409)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
		return
	}
	if op == "bootstrap" {
		result, err := n.bootstrap(r.Context())
		if err != nil {
			http.Error(w, "bootstrap unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
		return
	}
	var body nativeEnvelope
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	grant, err := n.verify(body.Grant)
	if err != nil || grant.GroupID != body.GroupID || grant.TopicID != body.TopicID {
		http.Error(w, "scope denied", 403)
		return
	}
	expectedTurn := strconv.FormatInt(grant.EventID, 10)
	if grant.CallbackID != "" {
		expectedTurn = "callback:" + grant.CallbackID
		if op != "authorize-memory" && op != "telegram" {
			http.Error(w, "callback operation denied", 403)
			return
		}
	}
	if !grant.Background && body.Turn != expectedTurn {
		http.Error(w, "turn denied", 403)
		return
	}
	if op != "finish" && op != "turn-status" {
		state, stateErr := n.a.service.GetSystemState(r.Context())
		if stateErr != nil || state.Frozen {
			http.Error(w, "CG runtime is frozen or unavailable", 403)
			return
		}
		valid, e := n.a.queries.NativeAssistantGenerationValid(r.Context(), grant.GroupID, grant.TopicID, grant.Generation)
		policy, pe := n.a.Policy(r.Context(), grant.GroupID)
		authorized, ae := n.a.queries.GetAuthorizedGroupByChatID(r.Context(), grant.GroupID)
		if e != nil || pe != nil || ae != nil || !authorized.Enabled || !valid || (!policy.ChatEnabled && !grant.StyleOnly) {
			http.Error(w, "source scope no longer valid", 403)
			return
		}
	}
	if grant.StyleOnly && op != "model" && op != "finish" && op != "turn-status" {
		http.Error(w, "style-only capability", 403)
		return
	}
	var result any
	switch op {
	case "model":
		result, err = n.model(r.Context(), grant, body)
	case "embedding":
		result, err = n.embedding(r.Context(), grant, body.Payload)
	case "telegram":
		result, err = n.telegram(r.Context(), grant, body)
	case "download":
		result, err = n.download(r.Context(), grant, body.Payload)
	case "tts":
		result, err = n.tts(r.Context(), grant, body.Payload)
	case "authorize-memory":
		if grant.Background || grant.ActorID == 0 {
			http.Error(w, "memory actor denied", 403)
			return
		}
		allowed := n.a.assistantIsOwner(r.Context(), grant.ActorID)
		if !allowed {
			allowed, err = n.a.service.isChatAdmin(r.Context(), grant.GroupID, grant.ActorID)
		}
		if err != nil || !allowed {
			http.Error(w, "memory administrator required", 403)
			return
		}
		result = true
	case "turn-status":
		var consumed bool
		consumed, err = n.a.queries.NativeTurnConsumed(r.Context(), body.Turn)
		result = map[string]any{"consumed": consumed}
	case "finish":
		n.mu.Lock()
		turn := n.turns[body.Turn]
		delete(n.turns, body.Turn)
		n.mu.Unlock()
		if turn != nil {
			turn.mu.Lock()
			if turn.lease != nil {
				turn.lease.finish(true, nil)
			}
			turn.mu.Unlock()
		}
		var outcome struct {
			Succeeded bool `json:"succeeded"`
		}
		if json.Unmarshal(body.Payload, &outcome) != nil {
			http.Error(w, "invalid completion", 400)
			return
		}
		status := "done"
		if !outcome.Succeeded {
			status = "pending"
		}
		err = n.a.queries.SetNativeAssistantEventStatus(r.Context(), grant.EventID, status)
		result = true
	default:
		http.Error(w, "operation denied", 403)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(503)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "native broker operation failed"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

type nativeModelRequest struct {
	Role          string              `json:"role"`
	Messages      []ai.Message        `json:"messages"`
	Tools         []ai.ToolDefinition `json:"tools"`
	MaxTokens     int                 `json:"max_tokens"`
	Temperature   float64             `json:"temperature"`
	Timeout       float64             `json:"timeout_sec"`
	ContextTokens int                 `json:"context_tokens"`
}

func (n *NativeAssistant) model(ctx context.Context, g nativeGrant, body nativeEnvelope) (any, error) {
	var req nativeModelRequest
	if err := json.Unmarshal(body.Payload, &req); err != nil {
		return nil, err
	}
	if g.StyleOnly && req.Role != "compress" {
		return nil, errors.New("style-only model role denied")
	}
	if req.Role != "chat" && req.Role != "decision" && req.Role != "compress" && req.Role != "vision" {
		return nil, errors.New("role denied")
	}
	for _, tool := range req.Tools {
		if tool.Function.Name == "rule_manage" || tool.Function.Name == "vote_ban" {
			return nil, errors.New("moderation tool denied")
		}
	}
	pool, err := n.a.loadPool(ctx, g.GroupID)
	if err != nil {
		return nil, err
	}
	policy, err := n.a.Policy(ctx, g.GroupID)
	if err != nil {
		return nil, err
	}
	if len(req.Tools) == 0 {
		result, _, e := n.a.dispatchPlain(ctx, g.GroupID, req.Role, pool, policy, ai.CheckRequest{PreserveMessages: true, Messages: req.Messages, MaxTokens: req.MaxTokens, Temperature: req.Temperature, Timeout: time.Duration(req.Timeout * float64(time.Second))})
		if e != nil {
			return nil, e
		}
		return map[string]any{"content": result.Content}, nil
	}
	if req.Role != "chat" {
		return nil, errors.New("tools require chat role")
	}
	n.mu.Lock()
	turn := n.turns[body.Turn]
	if turn == nil {
		turn = &nativeTurn{}
		n.turns[body.Turn] = turn
	}
	n.mu.Unlock()
	turn.mu.Lock()
	defer turn.mu.Unlock()
	turn.used = time.Now()
	// Approved CG routing exception: an explicit pool assignment overlays
	// source call-site MaxTokens/Temperature. Native never applies the
	// legacy 12000 local prompt cap (PreserveMessages keeps source budget).
	params := pool.TaskAssignments["chat"]
	if params.MaxTokens > 0 {
		req.MaxTokens = params.MaxTokens
	}
	if params.Temperature != nil {
		req.Temperature = *params.Temperature
	}
	lease := turn.lease
	if lease == nil && turn.pinned {
		return nil, errors.New("pinned tool endpoint failed; no re-selection")
	}
	if lease == nil {
		lease, err = n.a.acquire(ctx, g.GroupID, "chat", pool, true, policy)
		if err != nil {
			return nil, err
		}
		if lease.queued {
			n.a.releaseQueued(g.GroupID)
		}
	}
	attempted := map[string]struct{}{lease.ref.String(): {}}
	fallbackCtx, cancel := context.WithTimeout(ctx, assistantFallbackBudget)
	defer cancel()
	for {
		key, _, _ := lease.ref.Parse()
		client, exists := n.a.providers.Client(key)
		toolClient, capable := client.(ai.ToolCallingClient)
		var result *ai.ToolChatResult
		var callErr error
		started := time.Now()
		if !exists || !capable {
			callErr = errors.New("model tools unavailable")
		} else {
			result, callErr = toolClient.ChatWithTools(ctx, ai.ToolChatRequest{PreserveMessages: true, Model: assistantModelName(lease.ref), Messages: req.Messages, Tools: req.Tools, MaxTokens: req.MaxTokens, Temperature: req.Temperature, Timeout: assistantEndpointRequestTimeout(time.Duration(req.Timeout*float64(time.Second)), lease.ep.TimeoutMs)})
		}
		if callErr == nil && result == nil {
			callErr = errors.New("empty model response")
		}
		if result != nil && len(result.ToolCalls) > 0 {
			turn.pinned = true
		}
		n.a.recordDispatch(context.Background(), g.GroupID, "chat", lease, int32(time.Since(started)/time.Millisecond), callErr)
		if callErr == nil {
			if turn.pinned {
				turn.lease = lease
			} else {
				lease.finish(true, nil)
				turn.lease = nil
			}
			return map[string]any{"response": map[string]any{"model": result.Model, "choices": []any{map[string]any{"index": 0, "finish_reason": result.FinishReason, "message": map[string]any{"role": "assistant", "content": result.Content, "tool_calls": result.ToolCalls}}}}, "content": result.Content}, nil
		}
		lease.finish(false, callErr)
		turn.lease = nil
		if turn.pinned || !assistantDispatchErrorRecoverable(ctx, n.a.ctx, callErr) {
			return nil, callErr
		}
		ids := n.a.assistantFallbackEndpointIDs(pool, "chat", true, lease.ep, attempted)
		if len(ids) == 0 {
			return nil, callErr
		}
		lease, err = n.a.acquireEndpointIDs(fallbackCtx, g.GroupID, "chat", pool, true, policy, ids)
		if err != nil {
			return nil, err
		}
		if lease.queued {
			n.a.releaseQueued(g.GroupID)
		}
		attempted[lease.ref.String()] = struct{}{}
	}
}
func (n *NativeAssistant) embedding(ctx context.Context, g nativeGrant, raw json.RawMessage) (any, error) {
	var req struct {
		Texts   []string `json:"texts"`
		SpaceID string   `json:"space_id"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if len(req.Texts) > 64 {
		return nil, errors.New("embedding batch too large")
	}
	pool, err := n.a.loadPool(ctx, g.GroupID)
	if err != nil {
		return nil, err
	}
	policy, err := n.a.Policy(ctx, g.GroupID)
	if err != nil {
		return nil, err
	}
	var vectors [][]float64
	space := ""
	for _, input := range req.Texts {
		vector, ref, e := n.a.dispatchEmbedding(ctx, g.GroupID, pool, policy, input)
		if e != nil {
			return nil, e
		}
		if space != "" && space != ref {
			return nil, errors.New("embedding space changed inside batch")
		}
		if space == "" {
			// One source embedding batch is one model space. Keep the CG
			// weighted/primary+fallback choice for the first request, then pin
			// the selected reference for the rest of this same batch.
			for _, ep := range pool.Endpoints {
				if string(ep.ModelRef) == ref {
					pool.Strategy = "primary-overflow"
					pool.TaskAssignments = map[string]AssistantTaskAssignment{"vector": {Primary: ep.ID}}
					break
				}
			}
		}
		space = ref
		vectors = append(vectors, vector)
	}
	if len(vectors) == 0 {
		return nil, errors.New("empty embedding input")
	}
	return map[string]any{"vectors": vectors, "space_id": space, "model": space, "dimensions": len(vectors[0])}, nil
}
