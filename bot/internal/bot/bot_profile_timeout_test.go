package bot

import (
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/config"
)

func TestComputeProfileCheckTimeout(t *testing.T) {
	tests := []struct {
		name   string
		policy config.GuardPolicy
		want   time.Duration
	}{
		{
			name: "no fallback uses timeout directly",
			policy: config.GuardPolicy{
				AI: config.AIPolicy{
					TimeoutMs: 10000,
				},
			},
			want: 10 * time.Second,
		},
		{
			name: "two fallbacks at thirty seconds caps at max",
			policy: config.GuardPolicy{
				AI: config.AIPolicy{
					TimeoutMs:         30000,
					FallbackModelRefs: []string{"p:m2", "p:m3"},
				},
			},
			want: 25 * time.Second,
		},
		{
			name: "one fallback at fifteen seconds caps at max",
			policy: config.GuardPolicy{
				AI: config.AIPolicy{
					TimeoutMs:         15000,
					FallbackModelRefs: []string{"p:m2"},
				},
			},
			want: 25 * time.Second,
		},
		{
			name: "missing timeout defaults to ten seconds per call",
			policy: config.GuardPolicy{
				AI: config.AIPolicy{
					FallbackModelRefs: []string{"p:m2", "p:m3"},
				},
			},
			want: 25 * time.Second,
		},
		{
			name: "short timeout clamps to min",
			policy: config.GuardPolicy{
				AI: config.AIPolicy{
					TimeoutMs: 3000,
				},
			},
			want: 5 * time.Second,
		},
		{
			name: "uses legacy fallback chain when model refs are empty",
			policy: config.GuardPolicy{
				AI: config.AIPolicy{
					TimeoutMs:     10000,
					FallbackChain: []string{"provider/model"},
				},
			},
			want: 20 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computeProfileCheckTimeout(tt.policy); got != tt.want {
				t.Fatalf("computeProfileCheckTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}
