package config

import (
	"encoding/json"
	"testing"
)

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

func TestDefaultPolicyNewUserNoInvitesDisabled(t *testing.T) {
	if DefaultPolicy.Filter.NewUser.NoInvites {
		t.Fatal("expected filter.new_user.no_invites to default to false")
	}
}

func TestMergePolicyDocumentsPreservesExplicitZeroAndEmptyValues(t *testing.T) {
	policy, err := MergePolicyDocuments([]byte(`{
		"verify": {
			"profile_blacklist": [],
			"welcome_message": {"delete_after_seconds": 0}
		},
		"filter": {
			"links": {"whitelist": []},
			"new_user": {"max_messages_per_minute": 0}
		},
		"ai": {
			"fallback_chain": [],
			"capability_requirements": [],
			"actions_by_category": {},
			"thresholds": {"ban": 0, "mute": 0, "warn": 0, "flag": 0}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if policy.Verify.WelcomeMessage.DeleteAfterSeconds != 0 {
		t.Fatalf("welcome delete_after_seconds = %d, want 0", policy.Verify.WelcomeMessage.DeleteAfterSeconds)
	}
	if policy.Filter.NewUser.MaxMessagesPerMinute != 0 {
		t.Fatalf("new user max_messages_per_minute = %d, want 0", policy.Filter.NewUser.MaxMessagesPerMinute)
	}
	if len(policy.Verify.ProfileBlacklist) != 0 || len(policy.Filter.Links.Whitelist) != 0 {
		t.Fatalf("explicit empty lists were not preserved: profile=%v links=%v", policy.Verify.ProfileBlacklist, policy.Filter.Links.Whitelist)
	}
	if len(policy.AI.FallbackChain) != 0 || len(policy.AI.CapabilityRequirements) != 0 || len(policy.AI.ActionsByCategory) != 0 {
		t.Fatalf("explicit empty AI values were not preserved: fallback=%v capabilities=%v actions=%v", policy.AI.FallbackChain, policy.AI.CapabilityRequirements, policy.AI.ActionsByCategory)
	}
	if policy.AI.Thresholds != (AIThresholds{}) {
		t.Fatalf("explicit zero AI thresholds were not preserved: %+v", policy.AI.Thresholds)
	}
}

func TestValidateGuardPolicyRejectsUnknownActionAndInvalidRegex(t *testing.T) {
	policy := DefaultPolicy
	policy.AntiSpam.RateLimit.Action = "mute_5minutes_typo"
	if err := ValidateGuardPolicy(policy); err == nil {
		t.Fatal("invalid anti-spam action was accepted")
	}

	policy = DefaultPolicy
	policy.Filter.Regex.Patterns = []string{"[unterminated"}
	if err := ValidateGuardPolicy(policy); err == nil {
		t.Fatal("invalid regex was accepted")
	}
}

func TestValidatePolicyDocumentRejectsUnknownNestedField(t *testing.T) {
	err := ValidatePolicyDocument([]byte(`{"filter":{"links":{"enabled":true,"allow_everything":true}}}`))
	if err == nil {
		t.Fatal("unknown nested policy field was accepted")
	}
}

func TestValidatePolicyDocumentAcceptsDeployedLegacyFields(t *testing.T) {
	raw := []byte(`{
		"ai": {"daily_budget_cents": 100, "exempt_admins": true, "actions_by_category": {"spam": "delete_warn", "scam": "delete_ban"}},
		"feedback": {"ai_flagged": {"enabled": true, "template": "flagged"}}
	}`)
	if err := ValidatePolicyDocument(raw); err != nil {
		t.Fatalf("deployed legacy config fields were rejected: %v", err)
	}
	policy, err := MergePolicyDocuments(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateGuardPolicy(policy); err != nil {
		t.Fatalf("deployed AI action aliases were rejected: %v", err)
	}
}

func TestDefaultPolicyIsValid(t *testing.T) {
	if err := ValidateGuardPolicy(DefaultPolicy); err != nil {
		t.Fatalf("default policy is invalid: %v", err)
	}
	raw, err := json.Marshal(DefaultPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePolicyDocument(raw); err != nil {
		t.Fatalf("serialized default policy document is invalid: %v", err)
	}
}
