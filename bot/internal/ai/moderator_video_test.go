package ai

import (
	"strings"
	"testing"

	"github.com/openclaw/clawguard/internal/config"
)

func TestVisionRequestContent_MultipleImages(t *testing.T) {
	content := visionRequestContent(CheckInput{Text: "hello", ImagesBase64: []string{"a", "b"}}, true)
	parts, ok := content.([]CheckContentPart)
	if !ok {
		t.Fatalf("expected []CheckContentPart, got %T", content)
	}
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(parts))
	}
	if parts[0].Type != "text" || !strings.Contains(parts[0].Text, "关键帧") {
		t.Fatalf("expected first part text to mention key frames, got %#v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL["url"] != "data:image/jpeg;base64,a" {
		t.Fatalf("unexpected second part: %#v", parts[1])
	}
	if parts[2].Type != "image_url" || parts[2].ImageURL["url"] != "data:image/jpeg;base64,b" {
		t.Fatalf("unexpected third part: %#v", parts[2])
	}
}

func TestCacheKey_VideoFileUniqueID(t *testing.T) {
	moderator := &Moderator{}
	policy := videoTestPolicy()

	first := moderator.cacheKey(CheckInput{ChatID: 1, Text: "same", Scene: "video", VideoFileUniqueID: "unique-1", ImagesHash: "hash-a", Policy: policy})
	second := moderator.cacheKey(CheckInput{ChatID: 1, Text: "same", Scene: "video", VideoFileUniqueID: "unique-1", ImagesHash: "hash-b", Policy: policy})

	if first != second {
		t.Fatalf("expected same cache key for same video unique id, got %q != %q", first, second)
	}
	if !strings.Contains(first, ":video:vid:unique-1") {
		t.Fatalf("expected video unique id in cache key, got %q", first)
	}
}

func TestCacheKey_BackCompat_SingleImage(t *testing.T) {
	moderator := &Moderator{}
	policy := videoTestPolicy()

	key := moderator.cacheKey(CheckInput{ChatID: 42, Text: "ignored when image hash exists", Scene: "message", ImageBase64: "base64", ImageHash: "image-hash", Policy: policy})
	keyWithDifferentImageBody := moderator.cacheKey(CheckInput{ChatID: 42, Text: "other", Scene: "message", ImageBase64: "different", ImageHash: "image-hash", Policy: policy})

	if key != keyWithDifferentImageBody {
		t.Fatalf("expected legacy single-image cache key to depend on ImageHash, got %q != %q", key, keyWithDifferentImageBody)
	}
	if !strings.Contains(key, ":message:") || !strings.HasSuffix(key, ":image:image-hash") {
		t.Fatalf("expected legacy image cache key shape, got %q", key)
	}
}

func videoTestPolicy() config.AIPolicy {
	return config.AIPolicy{
		MessageRules:      "message rule",
		BioRules:          "bio rule",
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
}
