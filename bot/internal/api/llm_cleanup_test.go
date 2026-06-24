package api

import (
	"encoding/json"
	"testing"
)

func TestCleanLLMModelReferencesFromConfigRemovesCurrentAndLegacyRefs(t *testing.T) {
	target := newLLMModelReferenceTarget("sub2api", "gpt-5.5")
	raw := []byte(`{
		"verify": {"enabled": true},
		"ai": {
			"enabled": true,
			"primary_model_ref": "sub2api:gpt-5.5",
			"fallback_model_refs": ["aw:gpt-5.4", "sub2api:gpt-5.5", "keep:model"],
			"primary_provider": "sub2api",
			"primary_model": "gpt-5.5",
			"fallback_chain": ["sub2api/gpt-5.5", "aw/gpt-5.4"],
			"provider": "sub2api",
			"model": "gpt-5.5",
			"timeout_ms": 10000
		}
	}`)

	cleaned, summary, changed, err := cleanLLMModelReferencesFromConfig(raw, target)
	if err != nil {
		t.Fatalf("cleanLLMModelReferencesFromConfig error = %v", err)
	}
	if !changed {
		t.Fatalf("changed = false, want true")
	}
	if summary.PrimaryModelRefs != 1 ||
		summary.FallbackModelRefs != 1 ||
		summary.LegacyPrimaryRefs != 1 ||
		summary.LegacyFallbackChainRefs != 1 ||
		summary.LegacyProviderModelRefs != 1 {
		t.Fatalf("summary = %+v, want one removal in each ref bucket", summary)
	}

	var doc map[string]any
	if err := json.Unmarshal(cleaned, &doc); err != nil {
		t.Fatalf("unmarshal cleaned config: %v", err)
	}
	if _, ok := doc["verify"].(map[string]any); !ok {
		t.Fatalf("verify config missing after cleanup")
	}
	aiConfig := doc["ai"].(map[string]any)
	if _, ok := aiConfig["primary_model_ref"]; ok {
		t.Fatalf("primary_model_ref still present: %v", aiConfig["primary_model_ref"])
	}
	if _, ok := aiConfig["primary_provider"]; ok {
		t.Fatalf("legacy primary_provider still present")
	}
	if _, ok := aiConfig["primary_model"]; ok {
		t.Fatalf("legacy primary_model still present")
	}
	if _, ok := aiConfig["provider"]; ok {
		t.Fatalf("legacy provider still present")
	}
	if _, ok := aiConfig["model"]; ok {
		t.Fatalf("legacy model still present")
	}
	if got := int(aiConfig["timeout_ms"].(float64)); got != 10000 {
		t.Fatalf("timeout_ms = %d, want 10000", got)
	}
	assertStringArray(t, aiConfig["fallback_model_refs"], []string{"aw:gpt-5.4", "keep:model"})
	assertStringArray(t, aiConfig["fallback_chain"], []string{"aw/gpt-5.4"})
}

func TestCleanLLMModelReferencesFromConfigDoesNotRemoveAmbiguousLegacyFields(t *testing.T) {
	target := newLLMModelReferenceTarget("sub2api", "gpt-5.5")
	raw := []byte(`{
		"ai": {
			"primary_provider": "sub2api",
			"primary_model": "gpt-4.1",
			"provider": "sub2api",
			"model": "gpt-4.1",
			"fallback_chain": ["sub2api/gpt-4.1"]
		}
	}`)

	cleaned, summary, changed, err := cleanLLMModelReferencesFromConfig(raw, target)
	if err != nil {
		t.Fatalf("cleanLLMModelReferencesFromConfig error = %v", err)
	}
	if changed {
		t.Fatalf("changed = true, want false")
	}
	if summary.refsRemoved() != 0 {
		t.Fatalf("refsRemoved = %d, want 0", summary.refsRemoved())
	}
	if string(cleaned) != string(raw) {
		t.Fatalf("cleaned config changed unexpectedly: %s", cleaned)
	}
}

func assertStringArray(t *testing.T, value any, want []string) {
	t.Helper()
	values, ok := value.([]any)
	if !ok {
		t.Fatalf("value = %T, want []any", value)
	}
	if len(values) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(values), len(want), values)
	}
	for i, item := range values {
		if item != want[i] {
			t.Fatalf("item[%d] = %v, want %q", i, item, want[i])
		}
	}
}
