package config

import "fmt"

type JoinProtectionPolicy struct {
	Enabled                        bool `json:"enabled"`
	JoinThreshold                  int  `json:"join_threshold"`
	JoinWindowSeconds              int  `json:"join_window_seconds"`
	ProtectionDurationSeconds      int  `json:"protection_duration_seconds"`
	TemporaryBanSeconds            int  `json:"temporary_ban_seconds"`
	AdminNotifyIntervalSeconds     int  `json:"admin_notify_interval_seconds"`
	MaxPendingVerifications        int  `json:"max_pending_verifications"`
	TelegramFailureCooldownSeconds int  `json:"telegram_failure_cooldown_seconds"`
}

var DefaultJoinProtectionPolicy = JoinProtectionPolicy{
	Enabled:                        true,
	JoinThreshold:                  20,
	JoinWindowSeconds:              60,
	ProtectionDurationSeconds:      900,
	TemporaryBanSeconds:            3600,
	AdminNotifyIntervalSeconds:     300,
	MaxPendingVerifications:        30,
	TelegramFailureCooldownSeconds: 300,
}

func applyJoinProtectionDefaults(policy *JoinProtectionPolicy) {
	if policy == nil {
		return
	}
	if policy.JoinThreshold <= 0 {
		policy.JoinThreshold = DefaultJoinProtectionPolicy.JoinThreshold
	}
	if policy.JoinWindowSeconds <= 0 {
		policy.JoinWindowSeconds = DefaultJoinProtectionPolicy.JoinWindowSeconds
	}
	if policy.ProtectionDurationSeconds <= 0 {
		policy.ProtectionDurationSeconds = DefaultJoinProtectionPolicy.ProtectionDurationSeconds
	}
	if policy.TemporaryBanSeconds <= 0 {
		policy.TemporaryBanSeconds = DefaultJoinProtectionPolicy.TemporaryBanSeconds
	}
	if policy.AdminNotifyIntervalSeconds <= 0 {
		policy.AdminNotifyIntervalSeconds = DefaultJoinProtectionPolicy.AdminNotifyIntervalSeconds
	}
	if policy.MaxPendingVerifications <= 0 {
		policy.MaxPendingVerifications = DefaultJoinProtectionPolicy.MaxPendingVerifications
	}
	if policy.TelegramFailureCooldownSeconds <= 0 {
		policy.TelegramFailureCooldownSeconds = DefaultJoinProtectionPolicy.TelegramFailureCooldownSeconds
	}
}

func ValidateJoinProtectionPolicy(policy JoinProtectionPolicy) error {
	checks := []struct {
		name    string
		value   int
		minimum int
		maximum int
	}{
		{"join_threshold", policy.JoinThreshold, 2, 1000},
		{"join_window_seconds", policy.JoinWindowSeconds, 5, 3600},
		{"protection_duration_seconds", policy.ProtectionDurationSeconds, 30, 86400},
		{"temporary_ban_seconds", policy.TemporaryBanSeconds, 60, 604800},
		{"admin_notify_interval_seconds", policy.AdminNotifyIntervalSeconds, 30, 86400},
		{"max_pending_verifications", policy.MaxPendingVerifications, 1, 10000},
		{"telegram_failure_cooldown_seconds", policy.TelegramFailureCooldownSeconds, 30, 3600},
	}
	for _, check := range checks {
		if check.value < check.minimum || check.value > check.maximum {
			return fmt.Errorf("%s must be between %d and %d", check.name, check.minimum, check.maximum)
		}
	}
	return nil
}
