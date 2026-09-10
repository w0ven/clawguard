package bot

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/config"
)

func TestNativeAssistantControlTrueReadWriteAndLegacyFreeze(t *testing.T) {
	chatID := int64(-76554)
	live := startNativeLiveGo(t, chatID, 0)
	live.startPython(t)
	if !waitNativeHealth(t, live.service.cfg.AssistantNativeURL, 20*time.Second) {
		t.Fatal("native did not start")
	}
	raw, err := live.service.NativeAssistantControl(live.ctx, "/config/read", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	var current map[string]any
	if err = json.Unmarshal(raw, &current); err != nil {
		t.Fatal(err)
	}
	if current["engine"] != "native" || current["automatic_fact_learning"] != false {
		t.Fatalf("unexpected native config %#v", current)
	}
	revision := int(current["revision"].(float64))
	raw, err = live.service.NativeAssistantControl(live.ctx, "/config/write", 0, map[string]any{
		"revision": revision,
		"bot":      map[string]any{"memory_recent_messages": 1800, "max_context_tokens": 256000},
	})
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	bot := saved["bot"].(map[string]any)
	if bot["memory_recent_messages"] != float64(1800) || bot["max_context_tokens"] != float64(256000) {
		t.Fatalf("source budget not saved: %#v", bot)
	}
	grantBody, err := live.service.NativeAssistantControl(live.ctx, "/groups/read", chatID, nil)
	if err != nil {
		t.Fatal(err)
	}
	var group map[string]any
	if err = json.Unmarshal(grantBody, &group); err != nil {
		t.Fatal(err)
	}
	settings := group["settings"].(map[string]any)
	rev := 0
	if v, ok := settings["cg_config_revision"].(float64); ok {
		rev = int(v)
	}
	raw, err = live.service.NativeAssistantControl(live.ctx, "/groups/write", chatID, map[string]any{
		"revision": rev, "operator_id": 7, "settings": map[string]any{"at_reply_mode": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &group); err != nil {
		t.Fatal(err)
	}
	if group["settings"].(map[string]any)["at_reply_mode"] != true {
		t.Fatalf("group write failed: %#v", group)
	}
	raw, err = live.service.NativeAssistantControl(live.ctx, "/memory/add", chatID, map[string]any{"content": "管理员永久原文", "operator_id": 7})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"permanent":true`) {
		t.Fatalf("permanent memory add: %s", raw)
	}
}

func TestNativeAssistantControlLegacyReadDoesNotStartPython(t *testing.T) {
	s := &Service{cfg: config.Config{AssistantEngine: "legacy"}}
	body, err := s.NativeAssistantControl(t.Context(), "/config/read", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"engine":"legacy"`) {
		t.Fatalf("legacy read: %s", body)
	}
}
