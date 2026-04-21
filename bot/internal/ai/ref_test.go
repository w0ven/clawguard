package ai

import "testing"

func TestModelRefParse(t *testing.T) {
	tests := []struct {
		name     string
		input    ModelRef
		provider string
		model    string
		ok       bool
	}{
		{name: "valid", input: NewModelRef("openai", "gpt-5"), provider: "openai", model: "gpt-5", ok: true},
		{name: "spaces", input: ModelRef(" provider : model "), provider: "provider", model: "model", ok: true},
		{name: "missing colon", input: ModelRef("provider/model"), ok: false},
		{name: "missing provider", input: ModelRef(":model"), ok: false},
		{name: "missing model", input: ModelRef("provider:"), ok: false},
		{name: "empty", input: ModelRef(""), ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, model, ok := tt.input.Parse()
			if ok != tt.ok || provider != tt.provider || model != tt.model {
				t.Fatalf("Parse() = (%q, %q, %v), want (%q, %q, %v)", provider, model, ok, tt.provider, tt.model, tt.ok)
			}
		})
	}
}
