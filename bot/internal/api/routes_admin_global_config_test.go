package api

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/openclaw/clawguard/internal/store"
)

func TestRejectDangerousGlobalConfigTruncation(t *testing.T) {
	currentRichConfig := []byte(`{
		"verify": {"enabled": true},
		"filter": {"links": {"enabled": true}},
		"warnings": {"enabled": true, "max_warns": 3},
		"ai": {
			"message_rules": "keep message rules",
			"bio_rules": "keep bio rules",
			"primary_model_ref": "newapi:glm-5",
			"fallback_model_refs": ["newapi:minimax-m2.5"],
			"auto_degrade": false,
			"timeout_ms": 10000
		}
	}`)

	tests := []struct {
		name    string
		before  []byte
		next    []byte
		wantErr bool
	}{
		{
			name:   "rejects probe-only payload that would delete existing rules",
			before: currentRichConfig,
			next: []byte(`{
				"ai": {
					"auto_degrade": true,
					"probe_enabled": true,
					"probe_interval_seconds": 300
				}
			}`),
			wantErr: true,
		},
		{
			name:    "rejects other tiny payload that would delete rich config",
			before:  currentRichConfig,
			next:    []byte(`{"verify":{"enabled":false}}`),
			wantErr: true,
		},
		{
			name:   "allows complete merged config",
			before: currentRichConfig,
			next: []byte(`{
				"verify": {"enabled": true},
				"filter": {"links": {"enabled": true}},
				"warnings": {"enabled": true, "max_warns": 3},
				"ai": {
					"message_rules": "keep message rules",
					"bio_rules": "keep bio rules",
					"primary_model_ref": "newapi:glm-5",
					"fallback_model_refs": ["newapi:minimax-m2.5"],
					"auto_degrade": true,
					"probe_enabled": true,
					"probe_interval_seconds": 300,
					"timeout_ms": 10000
				}
			}`),
		},
		{
			name:   "allows probe settings on empty global config",
			before: []byte(`{}`),
			next:   []byte(`{"ai":{"auto_degrade":true,"probe_enabled":true,"probe_interval_seconds":300}}`),
		},
		{
			name:   "allows replacing already-small probe-only config",
			before: []byte(`{"ai":{"auto_degrade":false,"probe_enabled":false,"probe_interval_seconds":120}}`),
			next:   []byte(`{"ai":{"auto_degrade":true,"probe_enabled":true,"probe_interval_seconds":300}}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := rejectDangerousGlobalConfigTruncation(tt.before, tt.next)
			if tt.wantErr {
				if !errors.Is(err, errDangerousGlobalConfigTruncation) {
					t.Fatalf("error = %v, want %v", err, errDangerousGlobalConfigTruncation)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
		})
	}
}

func TestParseGlobalConfigPutBodyRequiresEnvelope(t *testing.T) {
	_, err := parseGlobalConfigPutBody([]byte(`{"verify":{"enabled":true}}`))
	if !errors.Is(err, errGlobalConfigVersionRequired) {
		t.Fatalf("raw document error = %v, want %v", err, errGlobalConfigVersionRequired)
	}

	_, err = parseGlobalConfigPutBody([]byte(`{"section":"document","config":{"verify":{"enabled":true}}}`))
	if !errors.Is(err, errGlobalConfigVersionRequired) {
		t.Fatalf("document without version error = %v, want %v", err, errGlobalConfigVersionRequired)
	}

	req, err := parseGlobalConfigPutBody([]byte(`{"section":"prompt","config":{"ai":{"message_rules":"hello"}}}`))
	if err != nil {
		t.Fatalf("prompt section parse: %v", err)
	}
	if req.Section != globalConfigSectionPrompt {
		t.Fatalf("section = %q", req.Section)
	}
}

func TestMergeGlobalConfigSectionsDoNotOverwriteEachOther(t *testing.T) {
	current := []byte(`{
		"verify": {"enabled": true},
		"ai": {
			"message_rules": "keep message",
			"bio_rules": "keep bio",
			"custom_rules": "",
			"adkiller": {"enabled": false, "timeout_ms": 1500},
			"auto_degrade": false,
			"probe_enabled": false,
			"probe_interval_seconds": 120,
			"primary_model_ref": "newapi:glm-5"
		}
	}`)

	afterAdKiller, err := mergeAdKillerSection(current, []byte(`{
		"ai": {
			"adkiller": {
				"enabled": true,
				"timeout_ms": 2000,
				"on_failure": "fallback",
				"enabled_chat_ids": [1],
				"score_bands": [{"min_score": 91, "max_score": 100, "action": "kick"}]
			}
		}
	}`))
	if err != nil {
		t.Fatalf("merge adkiller: %v", err)
	}
	assertAIString(t, afterAdKiller, "message_rules", "keep message")
	assertAIString(t, afterAdKiller, "bio_rules", "keep bio")
	assertAIString(t, afterAdKiller, "primary_model_ref", "newapi:glm-5")
	adkiller := mustAIObject(t, afterAdKiller, "adkiller")
	if enabled, _ := adkiller["enabled"].(bool); !enabled {
		t.Fatalf("adkiller.enabled = %#v", adkiller["enabled"])
	}

	afterLLM, err := mergeLLMSection(afterAdKiller, []byte(`{
		"ai": {
			"auto_degrade": true,
			"probe_enabled": true,
			"probe_interval_seconds": 300
		}
	}`))
	if err != nil {
		t.Fatalf("merge llm: %v", err)
	}
	assertAIString(t, afterLLM, "message_rules", "keep message")
	assertAIBool(t, afterLLM, "auto_degrade", true)
	assertAIBool(t, afterLLM, "probe_enabled", true)
	afterAd := mustAIObject(t, afterLLM, "adkiller")
	if enabled, _ := afterAd["enabled"].(bool); !enabled {
		t.Fatalf("adkiller overwritten by llm section: %#v", afterAd)
	}

	afterPrompt, err := mergePromptSection(afterLLM, []byte(`{"ai":{"message_rules":"new message"}}`))
	if err != nil {
		t.Fatalf("merge prompt: %v", err)
	}
	assertAIString(t, afterPrompt, "message_rules", "new message")
	assertAIString(t, afterPrompt, "bio_rules", "keep bio")
	assertAIBool(t, afterPrompt, "probe_enabled", true)
	promptAd := mustAIObject(t, afterPrompt, "adkiller")
	if enabled, _ := promptAd["enabled"].(bool); !enabled {
		t.Fatalf("adkiller overwritten by prompt section: %#v", promptAd)
	}
}

func TestApplyGlobalConfigDocumentRejectsStaleVersionWithoutWrite(t *testing.T) {
	current := store.GlobalConfig{
		Config:  []byte(`{"verify":{"enabled":true},"ai":{"message_rules":"keep"}}`),
		Version: 4,
	}
	stale := int64(3)
	next, err := applyGlobalConfigPut(current, globalConfigPutRequest{
		Section: globalConfigSectionDocument,
		Version: &stale,
		Config:  []byte(`{"verify":{"enabled":false}}`),
	})
	if next != nil {
		t.Fatalf("stale document write produced config %s", next)
	}
	var conflict *globalConfigVersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want version conflict", err)
	}
	if conflict.Current.Version != 4 {
		t.Fatalf("conflict version = %d", conflict.Current.Version)
	}
	if string(conflict.Current.Config) != string(current.Config) {
		t.Fatalf("conflict current config mutated")
	}

	currentVersion := int64(4)
	written, err := applyGlobalConfigPut(current, globalConfigPutRequest{
		Section: globalConfigSectionDocument,
		Version: &currentVersion,
		Config:  []byte(`{"verify":{"enabled":true},"ai":{"message_rules":"keep","bio_rules":"keep"}}`),
	})
	if err != nil {
		t.Fatalf("matching version write: %v", err)
	}
	assertTopStringObject(t, written, "verify", "enabled", true)

	truncated, err := applyGlobalConfigPut(store.GlobalConfig{
		Config: []byte(`{
			"verify": {"enabled": true},
			"filter": {"links": {"enabled": true}},
			"warnings": {"enabled": true, "max_warns": 3},
			"ai": {"message_rules": "keep message rules", "bio_rules": "keep bio rules"}
		}`),
		Version: 4,
	}, globalConfigPutRequest{
		Section: globalConfigSectionDocument,
		Version: &currentVersion,
		Config:  []byte(`{"verify":{"enabled":false}}`),
	})
	if truncated != nil {
		t.Fatalf("truncated document write produced config %s", truncated)
	}
	if !errors.Is(err, errDangerousGlobalConfigTruncation) {
		t.Fatalf("truncated document error = %v", err)
	}
}

func TestPromptDualSegmentCustomRulesMigration(t *testing.T) {
	current := []byte(`{
		"ai": {
			"custom_rules": "legacy shared rules",
			"primary_model_ref": "newapi:glm-5"
		}
	}`)

	afterMessage, err := mergePromptSection(current, []byte(`{"ai":{"message_rules":"message only"}}`))
	if err != nil {
		t.Fatalf("save message: %v", err)
	}
	assertAIString(t, afterMessage, "message_rules", "message only")
	assertAIString(t, afterMessage, "custom_rules", "legacy shared rules")
	if _, ok := mustAIObject(t, afterMessage, "")["bio_rules"]; ok {
		t.Fatalf("bio_rules should remain absent until saved")
	}
	assertAIString(t, afterMessage, "primary_model_ref", "newapi:glm-5")

	afterBio, err := mergePromptSection(afterMessage, []byte(`{"ai":{"bio_rules":"bio only"}}`))
	if err != nil {
		t.Fatalf("save bio: %v", err)
	}
	assertAIString(t, afterBio, "message_rules", "message only")
	assertAIString(t, afterBio, "bio_rules", "bio only")
	assertAIString(t, afterBio, "custom_rules", "")
	assertAIString(t, afterBio, "primary_model_ref", "newapi:glm-5")
}

func TestPromptEmptySideDoesNotClearCustomRules(t *testing.T) {
	current := []byte(`{"ai":{"custom_rules":"legacy","message_rules":""}}`)
	next, err := mergePromptSection(current, []byte(`{"ai":{"bio_rules":""}}`))
	if err != nil {
		t.Fatalf("merge empty bio: %v", err)
	}
	assertAIString(t, next, "custom_rules", "legacy")
}

func mustObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return obj
}

func mustAIObject(t *testing.T, raw []byte, key string) map[string]any {
	t.Helper()
	ai, ok := objectField(mustObject(t, raw), "ai")
	if !ok {
		t.Fatalf("missing ai object")
	}
	if key == "" {
		return ai
	}
	child, ok := objectField(ai, key)
	if !ok {
		t.Fatalf("missing ai.%s", key)
	}
	return child
}

func assertAIString(t *testing.T, raw []byte, key, want string) {
	t.Helper()
	ai := mustAIObject(t, raw, "")
	got, _ := ai[key].(string)
	if got != want {
		t.Fatalf("ai.%s = %q, want %q", key, got, want)
	}
}

func assertAIBool(t *testing.T, raw []byte, key string, want bool) {
	t.Helper()
	ai := mustAIObject(t, raw, "")
	got, _ := ai[key].(bool)
	if got != want {
		t.Fatalf("ai.%s = %#v, want %v", key, ai[key], want)
	}
}

func assertTopStringObject(t *testing.T, raw []byte, top, key string, want bool) {
	t.Helper()
	obj := mustObject(t, raw)
	child, ok := objectField(obj, top)
	if !ok {
		t.Fatalf("missing %s", top)
	}
	got, _ := child[key].(bool)
	if got != want {
		t.Fatalf("%s.%s = %#v, want %v", top, key, child[key], want)
	}
}
