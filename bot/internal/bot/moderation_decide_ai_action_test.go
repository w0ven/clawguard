package bot

import (
	"testing"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/config"
)

func TestDecideAIActionVerdictCeiling(t *testing.T) {
	policy := config.AIPolicy{
		Thresholds: config.AIThresholds{
			Ban:  0.9,
			Mute: 0.75,
			Warn: 0.5,
			Flag: 0.3,
		},
		ActionsByCategory: map[string]string{
			"刷单": "ban",
			"正常": "none",
		},
	}
	tests := []struct {
		name       string
		verdict    string
		category   string
		confidence float64
		want       string
	}{
		{
			name:       "suspicious category ban is clamped to warn",
			verdict:    "suspicious",
			category:   "刷单",
			confidence: 0.78,
			want:       "warn",
		},
		{
			name:       "hard ad verdict keeps category ban",
			verdict:    "ad",
			category:   "刷单",
			confidence: 0.78,
			want:       "ban",
		},
		{
			name:       "normal ignores category ban",
			verdict:    "normal",
			category:   "刷单",
			confidence: 0.95,
			want:       "none",
		},
		{
			name:       "normal category none stays none",
			verdict:    "normal",
			category:   "正常",
			confidence: 0.10,
			want:       "none",
		},
		{
			name:       "suspicious category none is raised to warn",
			verdict:    "suspicious",
			category:   "正常",
			confidence: 0.80,
			want:       "warn",
		},
		{
			name:       "suspicious low confidence threshold is raised to warn",
			verdict:    "suspicious",
			confidence: 0.10,
			want:       "warn",
		},
		{
			name:       "hard ad category none is raised to warn",
			verdict:    "ad",
			category:   "正常",
			confidence: 0.95,
			want:       "warn",
		},
		{
			name:       "suspicious high confidence threshold is clamped to warn",
			verdict:    "suspicious",
			confidence: 0.95,
			want:       "warn",
		},
		{
			name:       "hard ad high confidence threshold can ban",
			verdict:    "ad",
			confidence: 0.95,
			want:       "ban",
		},
		{
			name:       "hard ad low confidence threshold is raised to warn",
			verdict:    "ad",
			confidence: 0.10,
			want:       "warn",
		},
		{
			name:       "hard scam low confidence threshold is raised to warn",
			verdict:    "scam",
			confidence: 0.10,
			want:       "warn",
		},
	}

	svc := &Service{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := svc.decideAIAction(policy, ai.CheckOutput{
				Verdict: ai.Verdict{
					Verdict:    tt.verdict,
					Category:   tt.category,
					Confidence: tt.confidence,
				},
			})
			if got != tt.want {
				t.Fatalf("decideAIAction() = %q, want %q", got, tt.want)
			}
		})
	}
}
