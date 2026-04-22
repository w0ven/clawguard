package ai

import (
	"strings"
	"testing"

	"github.com/openclaw/clawguard/internal/config"
)

func TestBuildPromptPreviewUsesSceneSpecificPrompt(t *testing.T) {
	messagePrompt := BuildPromptPreview("message", "msg rule", "hello world")
	if !strings.Contains(messagePrompt, "消息：") {
		t.Fatalf("expected message prompt to include message section, got %q", messagePrompt)
	}
	if !strings.Contains(messagePrompt, "如果有图片") {
		t.Fatalf("expected message prompt to keep message-only instructions")
	}
	if !strings.Contains(messagePrompt, "msg rule") {
		t.Fatalf("expected message prompt to include custom rules")
	}

	bioPrompt := BuildPromptPreview("bio", "bio rule", "my bio")
	if !strings.Contains(bioPrompt, "简介：") {
		t.Fatalf("expected bio prompt to include bio section, got %q", bioPrompt)
	}
	if !strings.Contains(bioPrompt, "判定要点") {
		t.Fatalf("expected bio prompt to include bio-only instructions")
	}
	if strings.Contains(bioPrompt, "如果有图片") {
		t.Fatalf("expected bio prompt to exclude message-only image instruction")
	}
	if strings.Contains(bioPrompt, "链接预览") {
		t.Fatalf("expected bio prompt to exclude message-only link-preview instruction")
	}
	if !strings.Contains(bioPrompt, "bio rule") {
		t.Fatalf("expected bio prompt to include custom rules")
	}
}

func TestPolicyFingerprintIsSceneSpecific(t *testing.T) {
	moderator := &Moderator{}
	base := config.AIPolicy{
		MessageRules:      "message-a",
		BioRules:          "bio-a",
		PrimaryProvider:   "provider-a",
		PrimaryModel:      "model-a",
		PrimaryModelRef:   "provider-a:model-a",
		FallbackChain:     []string{"provider-b/model-b"},
		FallbackModelRefs: []string{"provider-b:model-b"},
		ActionsByCategory: map[string]string{"正常": "none"},
		Thresholds: config.AIThresholds{
			Ban:  0.9,
			Mute: 0.75,
			Warn: 0.5,
			Flag: 0.3,
		},
	}

	messageA := moderator.policyFingerprint("message", base)
	bioA := moderator.policyFingerprint("bio", base)

	changedBio := base
	changedBio.BioRules = "bio-b"

	messageB := moderator.policyFingerprint("message", changedBio)
	bioB := moderator.policyFingerprint("bio", changedBio)

	if messageA != messageB {
		t.Fatalf("expected message fingerprint to ignore bio rules changes, got %q != %q", messageA, messageB)
	}
	if bioA == bioB {
		t.Fatalf("expected bio fingerprint to change when bio rules change")
	}
}

func TestCacheKeyIncludesScene(t *testing.T) {
	moderator := &Moderator{}
	policy := config.AIPolicy{
		MessageRules:      "message-a",
		BioRules:          "bio-a",
		PrimaryProvider:   "provider-a",
		PrimaryModel:      "model-a",
		ActionsByCategory: map[string]string{"正常": "none"},
		Thresholds: config.AIThresholds{
			Ban:  0.9,
			Mute: 0.75,
			Warn: 0.5,
			Flag: 0.3,
		},
	}

	messageKey := moderator.cacheKey(CheckInput{
		ChatID: 1,
		Text:   "same text",
		Scene:  "message",
		Policy: policy,
	})
	bioKey := moderator.cacheKey(CheckInput{
		ChatID: 1,
		Text:   "same text",
		Scene:  "bio",
		Policy: policy,
	})

	if messageKey == bioKey {
		t.Fatalf("expected cache keys for message and bio scenes to differ")
	}
	if !strings.Contains(messageKey, ":message:") {
		t.Fatalf("expected message cache key to include scene, got %q", messageKey)
	}
	if !strings.Contains(bioKey, ":bio:") {
		t.Fatalf("expected bio cache key to include scene, got %q", bioKey)
	}
}
