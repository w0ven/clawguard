package worker

import (
	"testing"

	"github.com/openclaw/clawguard/internal/config"
)

func TestResolveRetentionWindowsUsesIndependentSettings(t *testing.T) {
	windows := resolveRetentionWindows(config.RetentionConfig{
		ViolationsDays:  31,
		AIDecisionsDays: 62,
		ConfigAuditDays: 730,
	})
	if windows.violationsDays != 31 || windows.aiDecisionsDays != 62 || windows.configAuditDays != 730 {
		t.Fatalf("resolveRetentionWindows() = %+v", windows)
	}
}

func TestResolveRetentionWindowsUsesSafeDefaults(t *testing.T) {
	windows := resolveRetentionWindows(config.RetentionConfig{})
	if windows.violationsDays != 90 || windows.aiDecisionsDays != 90 || windows.configAuditDays != 365 {
		t.Fatalf("resolveRetentionWindows() = %+v", windows)
	}
}
