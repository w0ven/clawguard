package ai

import (
	"context"
	"strings"
	"testing"
	"time"

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

func TestPersistenceContextUsesLifecycleAndTimeout(t *testing.T) {
	lifeCtx, cancel := context.WithCancel(context.Background())
	moderator := NewModerator(lifeCtx, nil, nil, nil, nil, nil, nil, nil)

	persistCtx, persistCancel := moderator.persistenceContext()
	defer persistCancel()

	deadline, ok := persistCtx.Deadline()
	if !ok {
		t.Fatalf("expected persistence context to have deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > 5*time.Second {
		t.Fatalf("expected persistence deadline within 5s, got %v", remaining)
	}

	cancel()
	select {
	case <-persistCtx.Done():
	case <-time.After(time.Second):
		t.Fatalf("expected persistence context to follow lifecycle cancellation")
	}
}

func TestPersistenceContextFallsBackToBackground(t *testing.T) {
	moderator := &Moderator{}
	persistCtx, cancel := moderator.persistenceContext()
	defer cancel()

	if _, ok := persistCtx.Deadline(); !ok {
		t.Fatalf("expected fallback persistence context to have deadline")
	}
}

func TestCheckMessageDoesNotSkipShortUngraduatedMessage(t *testing.T) {
	ref := NewModelRef("p1", "m1")
	models := fakeModelRegistry{items: map[ModelRef]Model{
		ref: {ID: 1, ProviderID: 10, ProviderKey: "p1", ModelKey: "m1", Enabled: true, CapabilityTags: []string{"moderation"}},
	}}
	providers := moderatorProviderRegistry{clients: map[string]LLMClient{
		"p1": moderatorCheckClient{result: &CheckResult{Verdicts: []Verdict{{Verdict: "clean", Confidence: 0.9, Category: "正常"}}, Model: "m1"}},
	}}
	moderator := NewModerator(context.Background(), nil, nil, nil, providers, models, NewResolver(models), nil)

	output, err := moderator.CheckMessage(context.Background(), CheckInput{
		ChatID:        1,
		UserID:        2,
		Text:          "好",
		IsUngraduated: true,
		Policy: config.AIPolicy{
			PrimaryModelRef:         ref.String(),
			SkipMessagesShorterThan: 5,
			TimeoutMs:               1000,
		},
	})
	if err != nil {
		t.Fatalf("CheckMessage returned error: %v", err)
	}
	if output.Skipped {
		t.Fatalf("short ungraduated message should not be skipped")
	}
}
