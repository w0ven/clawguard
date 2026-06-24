package api

import (
	"errors"
	"testing"
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
