package bot

import (
	"testing"

	"github.com/openclaw/clawguard/internal/config"
)

func TestShouldRunVideoModeration_KindGate(t *testing.T) {
	policy := config.AIPolicy{VideoModerationEnabled: true}

	cases := []struct {
		name             string
		kind             string
		includeVideoNote bool
		want             bool
	}{
		{name: "video", kind: "video", want: true},
		{name: "animation", kind: "animation", want: true},
		{name: "video note disabled", kind: "video_note", want: false},
		{name: "video note enabled", kind: "video_note", includeVideoNote: true, want: true},
		{name: "sticker", kind: "sticker", want: false},
		{name: "text", kind: "text", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy.IncludeVideoNote = tc.includeVideoNote
			got := shouldRunVideoModeration(reviewableContent{Kind: tc.kind}, policy)
			if got != tc.want {
				t.Fatalf("expected %t, got %t", tc.want, got)
			}
		})
	}
}

func TestShouldRunVideoModeration_DisabledPolicy(t *testing.T) {
	policy := config.AIPolicy{VideoModerationEnabled: false, IncludeVideoNote: true}
	for _, kind := range []string{"video", "animation", "video_note", "sticker", "text"} {
		t.Run(kind, func(t *testing.T) {
			if shouldRunVideoModeration(reviewableContent{Kind: kind}, policy) {
				t.Fatalf("expected disabled policy to skip kind %q", kind)
			}
		})
	}
}
