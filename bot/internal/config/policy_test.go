package config

import "testing"

func TestApplyAIDefaultsBackfillsSceneRulesFromCustomRules(t *testing.T) {
	policy := AIPolicy{
		CustomRules: "legacy rules",
	}

	applyAIDefaults(&policy)

	if policy.MessageRules != "legacy rules" {
		t.Fatalf("expected message rules to backfill from custom rules, got %q", policy.MessageRules)
	}
	if policy.BioRules != "legacy rules" {
		t.Fatalf("expected bio rules to backfill from custom rules, got %q", policy.BioRules)
	}
}

func TestApplyAIDefaultsPreservesExplicitSceneRules(t *testing.T) {
	policy := AIPolicy{
		CustomRules:  "legacy rules",
		MessageRules: "message only",
		BioRules:     "bio only",
	}

	applyAIDefaults(&policy)

	if policy.MessageRules != "message only" {
		t.Fatalf("expected explicit message rules to be preserved, got %q", policy.MessageRules)
	}
	if policy.BioRules != "bio only" {
		t.Fatalf("expected explicit bio rules to be preserved, got %q", policy.BioRules)
	}
}

func TestApplyAIDefaults_VideoFields(t *testing.T) {
	policy := AIPolicy{}

	applyAIDefaults(&policy)

	if policy.VideoModerationEnabled != false {
		t.Fatalf("expected video moderation enabled to remain false, got %t", policy.VideoModerationEnabled)
	}
	if policy.VideoMaxBytes != DefaultPolicy.AI.VideoMaxBytes {
		t.Fatalf("expected video max bytes %d, got %d", DefaultPolicy.AI.VideoMaxBytes, policy.VideoMaxBytes)
	}
	if policy.VideoMaxDurationSec != DefaultPolicy.AI.VideoMaxDurationSec {
		t.Fatalf("expected video max duration sec %d, got %d", DefaultPolicy.AI.VideoMaxDurationSec, policy.VideoMaxDurationSec)
	}
	if policy.VideoFrameCount != DefaultPolicy.AI.VideoFrameCount {
		t.Fatalf("expected video frame count %d, got %d", DefaultPolicy.AI.VideoFrameCount, policy.VideoFrameCount)
	}
	if policy.VideoConcurrency != DefaultPolicy.AI.VideoConcurrency {
		t.Fatalf("expected video concurrency %d, got %d", DefaultPolicy.AI.VideoConcurrency, policy.VideoConcurrency)
	}
	if policy.IncludeVideoNote != DefaultPolicy.AI.IncludeVideoNote {
		t.Fatalf("expected include video note %t, got %t", DefaultPolicy.AI.IncludeVideoNote, policy.IncludeVideoNote)
	}
}
