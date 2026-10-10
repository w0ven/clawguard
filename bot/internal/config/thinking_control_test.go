package config

import "testing"

func TestThinkingOverrideIsGroupLocal(t *testing.T) {
	global := []byte(`{"ai":{"enabled":true,"primary_model_ref":"p:m","timeout_ms":15000}}`)
	other, err := MergePolicyDocuments(global)
	if err != nil {
		t.Fatal(err)
	}
	cola, err := MergePolicyDocuments(global, []byte(`{"ai":{"disable_thinking":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if other.AI.DisableThinking || !cola.AI.DisableThinking {
		t.Fatal("thinking flag must be opt-in")
	}
	if cola.AI.PrimaryModelRef != other.AI.PrimaryModelRef || cola.AI.TimeoutMs != other.AI.TimeoutMs || cola.AI.Enabled != other.AI.Enabled {
		t.Fatal("unrelated AI settings changed")
	}
}
