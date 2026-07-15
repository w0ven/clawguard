package worker

import (
	"testing"

	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/store"
)

func int64Pointer(value int64) *int64 { return &value }

func TestOperationsAlertsOnlyReportsActionableConditions(t *testing.T) {
	healthy := operationsAlerts(operationsStatus{
		backlog: store.OperationsBacklog{DueCleanup: 2, OldestDueCleanupSeconds: 20},
		runtime: bot.OperationsRuntimeStatus{
			JoinCleanupDeadLetters:       int64Pointer(0),
			BackupSecondsAgo:             int64Pointer(60),
			VerificationWorkerSecondsAgo: int64Pointer(20),
			JoinRecoveryWorkerSecondsAgo: int64Pointer(10),
		},
	})
	if len(healthy) != 0 {
		t.Fatalf("operationsAlerts() = %v, want no alerts", healthy)
	}

	unhealthy := operationsAlerts(operationsStatus{
		backlog: store.OperationsBacklog{DueCleanup: 101, RetryingCleanup: 21},
		runtime: bot.OperationsRuntimeStatus{
			JoinCleanupDeadLetters: int64Pointer(1),
			BackupSecondsAgo:       int64Pointer(37 * 3600),
		},
	})
	if len(unhealthy) != 4 {
		t.Fatalf("operationsAlerts() = %v, want 4 alerts", unhealthy)
	}
}

func TestOperationsAlertsReportsMissingBackup(t *testing.T) {
	alerts := operationsAlerts(operationsStatus{})
	if len(alerts) != 1 || alerts[0] != "No successful database backup has been recorded" {
		t.Fatalf("operationsAlerts() = %v, want missing backup alert", alerts)
	}
}
