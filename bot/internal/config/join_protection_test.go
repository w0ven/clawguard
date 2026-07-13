package config

import (
	"encoding/json"
	"testing"
)

func TestDefaultJoinProtectionPolicy(t *testing.T) {
	want := DefaultJoinProtectionPolicy
	if !want.Enabled || want.JoinThreshold != 20 || want.JoinWindowSeconds != 60 || want.ProtectionDurationSeconds != 900 || want.TemporaryBanSeconds != 3600 || want.AdminNotifyIntervalSeconds != 300 || want.MaxPendingVerifications != 30 || want.TelegramFailureCooldownSeconds != 300 {
		t.Fatalf("unexpected defaults: %+v", want)
	}
}

func TestValidateJoinProtectionPolicy(t *testing.T) {
	valid := DefaultJoinProtectionPolicy
	if err := ValidateJoinProtectionPolicy(valid); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	invalid := valid
	invalid.JoinThreshold = 0
	if err := ValidateJoinProtectionPolicy(invalid); err == nil {
		t.Fatal("zero threshold accepted")
	}
	invalid = valid
	invalid.TemporaryBanSeconds = 604801
	if err := ValidateJoinProtectionPolicy(invalid); err == nil {
		t.Fatal("extreme temporary ban accepted")
	}
	invalid = valid
	invalid.TelegramFailureCooldownSeconds = 3601
	if err := ValidateJoinProtectionPolicy(invalid); err == nil {
		t.Fatal("cleanup cooldown above one hour accepted")
	}
}

func TestEmptyGroupConfigKeepsJoinProtectionDefaults(t *testing.T) {
	policy := DefaultPolicy
	if err := json.Unmarshal([]byte(`{}`), &policy); err != nil {
		t.Fatal(err)
	}
	applyJoinProtectionDefaults(&policy.JoinProtection)
	if policy.JoinProtection != DefaultJoinProtectionPolicy {
		t.Fatalf("join protection = %+v, want %+v", policy.JoinProtection, DefaultJoinProtectionPolicy)
	}
}
