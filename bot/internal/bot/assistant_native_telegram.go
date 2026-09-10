package bot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var nativeTGMethods = map[string]bool{
	"getMe": true, "getChatMember": true, "getFile": true, "sendMessage": true, "sendRichMessage": true,
	"editMessageText": true, "editMessageReplyMarkup": true, "deleteMessage": true, "sendChatAction": true,
	"sendSticker": true, "sendVoice": true, "sendAudio": true, "sendPhoto": true, "sendDocument": true,
	"sendVideo": true, "sendAnimation": true, "sendMediaGroup": true, "answerCallbackQuery": true,
}

type nativeTelegramRequest struct {
	Method  string         `json:"method"`
	Purpose string         `json:"purpose"`
	Data    map[string]any `json:"data"`
	Files   map[string]struct {
		Filename string `json:"filename"`
		Base64   string `json:"base64"`
	} `json:"files"`
}

func nativeInt(value any) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}
func nativeObject(value any) map[string]any {
	if v, ok := value.(map[string]any); ok {
		return v
	}
	if s, ok := value.(string); ok {
		var out map[string]any
		_ = json.Unmarshal([]byte(s), &out)
		return out
	}
	return nil
}

func (n *NativeAssistant) telegram(ctx context.Context, g nativeGrant, envelope nativeEnvelope) (any, error) {
	var req nativeTelegramRequest
	if err := json.Unmarshal(envelope.Payload, &req); err != nil {
		return nil, err
	}
	if req.Purpose == "" {
		req.Purpose = "reply"
	}
	if req.Purpose != "reply" && req.Purpose != "progress" {
		return nil, errors.New("invalid delivery purpose")
	}
	if !nativeTGMethods[req.Method] {
		return nil, errors.New("Telegram operation denied")
	}
	if req.Method == "getMe" {
		return n.a.service.bot.Me, nil
	}
	if g.CallbackID != "" {
		if req.Method != "answerCallbackQuery" && req.Method != "getChatMember" && req.Method != "editMessageText" && req.Method != "editMessageReplyMarkup" && req.Method != "deleteMessage" {
			return nil, errors.New("callback Telegram operation denied")
		}
		if strings.HasPrefix(req.Method, "edit") || req.Method == "deleteMessage" {
			if nativeInt(req.Data["message_id"]) != g.MessageID {
				return nil, errors.New("callback message denied")
			}
		}
	}
	if req.Method == "answerCallbackQuery" {
		if g.CallbackID == "" || req.Data["callback_query_id"] != g.CallbackID {
			return nil, errors.New("callback capability required")
		}
	} else if req.Method == "getChatMember" {
		if nativeInt(req.Data["chat_id"]) != g.GroupID || nativeInt(req.Data["user_id"]) != g.ActorID || g.Background {
			return nil, errors.New("member scope denied")
		}
	} else if req.Method == "getFile" {
		fileID, _ := req.Data["file_id"].(string)
		ok, err := n.a.queries.NativeMediaAuthorized(ctx, g.GroupID, g.TopicID, fileID, g.EventID)
		if err != nil || !ok {
			return nil, errors.New("media source denied")
		}
	} else {
		if nativeInt(req.Data["chat_id"]) != g.GroupID {
			return nil, errors.New("chat scope denied")
		}
		if strings.HasPrefix(req.Method, "send") {
			if thread := nativeInt(req.Data["message_thread_id"]); thread != 0 && thread != int64(g.TopicID) {
				return nil, errors.New("topic scope denied")
			}
			if g.TopicID != 0 {
				req.Data["message_thread_id"] = g.TopicID
			}
		}
		if strings.HasPrefix(req.Method, "edit") || req.Method == "deleteMessage" {
			owned, err := n.a.queries.NativeDeliveryOwned(ctx, g.GroupID, g.TopicID, nativeInt(req.Data["message_id"]))
			if err != nil || !owned {
				return nil, errors.New("native may only modify its own delivery")
			}
		}
		reply := nativeObject(req.Data["reply_parameters"])
		target := nativeInt(reply["message_id"])
		if target == 0 {
			target = nativeInt(req.Data["reply_to_message_id"])
		}
		if target != 0 {
			if chat := nativeInt(reply["chat_id"]); chat != 0 && chat != g.GroupID {
				return nil, errors.New("reply chat denied")
			}
			visible, err := n.sourceVisible(ctx, g.GroupID, g.TopicID, target)
			if err != nil {
				return nil, err
			}
			if !visible {
				return map[string]any{"telegram_error": map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: message to be replied not found"}}, nil
			}
		}
	}
	if limiter := n.a.service.sendLimiter; limiter != nil {
		if err := limiter.WaitGlobal(ctx); err != nil {
			return nil, err
		}
		if strings.HasPrefix(req.Method, "send") {
			if err := limiter.WaitChat(ctx, g.GroupID); err != nil {
				return nil, err
			}
		}
	}
	var payload bytes.Buffer
	contentType := "application/json"
	if len(req.Files) > 0 {
		writer := multipart.NewWriter(&payload)
		for key, file := range req.Files {
			decoded, err := base64.StdEncoding.DecodeString(file.Base64)
			if err != nil || len(decoded) > 20<<20 {
				return nil, errors.New("invalid media attachment")
			}
			part, err := writer.CreateFormFile(key, file.Filename)
			if err != nil {
				return nil, err
			}
			if _, err = part.Write(decoded); err != nil {
				return nil, err
			}
		}
		for key, value := range req.Data {
			str, ok := value.(string)
			if !ok {
				raw, _ := json.Marshal(value)
				str = string(raw)
			}
			if err := writer.WriteField(key, str); err != nil {
				return nil, err
			}
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		contentType = writer.FormDataContentType()
	} else {
		if err := json.NewEncoder(&payload).Encode(req.Data); err != nil {
			return nil, err
		}
	}
	url := strings.TrimRight(n.a.service.bot.URL, "/") + "/bot" + n.a.service.bot.Token + "/" + req.Method
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &payload)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", contentType)
	deliveryID := int64(0)
	if (strings.HasPrefix(req.Method, "send") && req.Method != "sendChatAction") || strings.HasPrefix(req.Method, "edit") || req.Method == "deleteMessage" {
		deliveryID, err = n.a.queries.BeginNativeDelivery(ctx, envelope.Turn, g.GroupID, g.TopicID, req.Method, req.Purpose)
		if err != nil {
			return nil, err
		}
	}
	// No retry: a lost response is an ambiguous delivery, never a reason to send
	// another copy or run the legacy Go assistant.
	response, err := n.client.Do(request)
	if err != nil {
		if deliveryID != 0 {
			_ = n.a.queries.FinishNativeDelivery(context.Background(), deliveryID, nil, "uncertain")
		}
		return nil, errors.New("Telegram delivery unavailable or uncertain")
	}
	defer response.Body.Close()
	var decoded struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Code        int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  map[string]any  `json:"parameters,omitempty"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 20<<20)).Decode(&decoded); err != nil {
		if deliveryID != 0 {
			_ = n.a.queries.FinishNativeDelivery(context.Background(), deliveryID, nil, "uncertain")
		}
		return nil, errors.New("Telegram receipt unavailable")
	}
	if !decoded.OK {
		if deliveryID != 0 {
			status := "failed"
			if decoded.Code >= 500 {
				status = "uncertain"
			}
			_ = n.a.queries.FinishNativeDelivery(context.Background(), deliveryID, nil, status)
		}
		return map[string]any{"telegram_error": map[string]any{"ok": false, "error_code": decoded.Code, "description": decoded.Description, "parameters": decoded.Parameters}}, nil
	}
	var result any
	if err = json.Unmarshal(decoded.Result, &result); err != nil {
		return nil, err
	}
	if deliveryID != 0 {
		if rows, ok := result.([]any); ok {
			for index, row := range rows {
				id := nativeInt(nativeObject(row)["message_id"])
				if index == 0 {
					err = n.a.queries.FinishNativeDelivery(context.Background(), deliveryID, &id, "delivered")
				} else {
					extra, e := n.a.queries.BeginNativeDelivery(context.Background(), envelope.Turn, g.GroupID, g.TopicID, req.Method, req.Purpose)
					if e == nil {
						e = n.a.queries.FinishNativeDelivery(context.Background(), extra, &id, "delivered")
					}
					err = e
				}
				if err != nil {
					return nil, err
				}
			}
		} else {
			id := nativeInt(nativeObject(result)["message_id"])
			if id == 0 {
				id = nativeInt(req.Data["message_id"])
			}
			if err = n.a.queries.FinishNativeDelivery(context.Background(), deliveryID, &id, "delivered"); err != nil {
				return nil, err
			}
		}
	}
	if req.Method == "getFile" {
		file := nativeObject(result)
		path, _ := file["file_path"].(string)
		handle := newAssistantRequestID()
		n.mu.Lock()
		n.downloads[handle] = nativeDownload{group: g.GroupID, topic: g.TopicID, path: path, expires: time.Now().Add(time.Minute)}
		n.mu.Unlock()
		file["file_path"] = handle
	}
	return result, nil
}
func (n *NativeAssistant) download(ctx context.Context, g nativeGrant, raw json.RawMessage) (any, error) {
	var req struct {
		Handle string `json:"handle"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	n.mu.Lock()
	file, ok := n.downloads[req.Handle]
	delete(n.downloads, req.Handle)
	n.mu.Unlock()
	if !ok || file.group != g.GroupID || file.topic != g.TopicID || time.Now().After(file.expires) || file.path == "" || strings.Contains(file.path, "..") || strings.Contains(file.path, ":") {
		return nil, errors.New("media handle denied")
	}
	url := strings.TrimRight(n.a.service.bot.URL, "/") + "/file/bot" + n.a.service.bot.Token + "/" + strings.TrimLeft(file.path, "/")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := n.client.Do(request)
	if err != nil {
		return nil, errors.New("media download unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, errors.New("media download failed")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (5<<20)+1))
	if err != nil || len(data) > 5<<20 {
		return nil, errors.New("media download exceeds limit")
	}
	return map[string]string{"base64": base64.StdEncoding.EncodeToString(data)}, nil
}
