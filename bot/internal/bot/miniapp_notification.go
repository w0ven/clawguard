package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

var miniAppNotificationSlots = make(chan struct{}, 16)

// A single best-effort delivery is intentionally separate from verification.
// The DB row starts terminal as unconfirmed; a crash or saturation never queues retries.
func (s *Service) notifyMiniAppVerification(pendingID, userID int64) {
	select {
	case miniAppNotificationSlots <- struct{}{}:
	default:
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-miniAppNotificationSlots }()
		parent := s.lifecycleCtx
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		data, _ := json.Marshal(map[string]any{"chat_id": userID, "text": "入群验证已完成，本次入群验证限制已解除。其他未到期的限制不受影响。"})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+s.cfg.BotToken+"/sendMessage", bytes.NewReader(data))
		status := "failed"
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}
			res, sendErr := client.Do(req)
			if sendErr == nil {
				body, _ := io.ReadAll(io.LimitReader(res.Body, 8192))
				res.Body.Close()
				var result struct {
					OK bool `json:"ok"`
				}
				if res.StatusCode == http.StatusOK && json.Unmarshal(body, &result) == nil && result.OK {
					status = "sent"
				}
			}
		}
		dbctx, dbcancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer dbcancel()
		_ = s.queries.SetMiniAppNotificationResult(dbctx, pendingID, status)
	}()
}
