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
