package bot

import (
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/config"
)

func TestComputeAIModerationTimeout(t *testing.T) {
	tests := []struct {
		name   string
		policy config.AIPolicy
		want   time.Duration
	}{
		{
			name: "single model uses per-call timeout",
			policy: config.AIPolicy{
				TimeoutMs: 10000,
			},
			want: 10 * time.Second,
		},
		{
			name: "fallback chain and retry rounds multiply budget",
			policy: config.AIPolicy{
				TimeoutMs:         8000,
				FallbackModelRefs: []string{"cpa:gpt-5.5", "newapi:gpt-5.5"},
				MaxRetries:        1,
			},
			want: 48 * time.Second,
		},
		{
			name: "all configured retry rounds cap at max",
			policy: config.AIPolicy{
				TimeoutMs:         10000,
				FallbackModelRefs: []string{"p:m2", "p:m3"},
				MaxRetries:        2,
			},
			want: 60 * time.Second,
		},
		{
			name: "missing timeout defaults to ten seconds per call",
			policy: config.AIPolicy{
				FallbackModelRefs: []string{"p:m2", "p:m3"},
			},
			want: 30 * time.Second,
		},
		{
			name: "short timeout clamps to min",
			policy: config.AIPolicy{
				TimeoutMs: 3000,
			},
			want: 5 * time.Second,
		},
		{
			name: "uses legacy fallback chain",
			policy: config.AIPolicy{
				TimeoutMs:     10000,
				FallbackChain: []string{"provider/model"},
			},
			want: 20 * time.Second,
		},
		{
			name: "deduplicates current and legacy fallback refs",
			policy: config.AIPolicy{
				PrimaryModelRef:   "p:m1",
				TimeoutMs:         10000,
				FallbackModelRefs: []string{"p:m2"},
				FallbackChain:     []string{"p/m2"},
			},
			want: 20 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computeAIModerationTimeout(tt.policy); got != tt.want {
				t.Fatalf("computeAIModerationTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestComputeProfileCheckTimeoutUsesRetryRounds(t *testing.T) {
	policy := config.GuardPolicy{
		AI: config.AIPolicy{
			TimeoutMs:         8000,
			FallbackModelRefs: []string{"cpa:gpt-5.5", "newapi:gpt-5.5"},
			MaxRetries:        1,
		},
	}

	if got, want := computeProfileCheckTimeout(policy), 48*time.Second; got != want {
		t.Fatalf("computeProfileCheckTimeout() = %v, want %v", got, want)
	}
}
