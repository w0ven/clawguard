package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/openclaw/clawguard/internal/store"
)

func TestRenderScheduledTemplateEscapesKnownVarsAndKeepsUnknown(t *testing.T) {
	got := RenderScheduledTemplate("群：{group_title} 未知：{missing}", map[string]string{
		"{group_title}": "A_B{C}",
	})
	want := "群：A\\_B\\{C\\} 未知：{missing}"
	if got != want {
		t.Fatalf("rendered = %q, want %q", got, want)
	}
}

func TestSchedulerAcquireRunRejectsSameIDReentry(t *testing.T) {
	s := &Scheduler{}
	if !s.acquireRun(42) {
		t.Fatal("first acquire rejected")
	}
	if s.acquireRun(42) {
		t.Fatal("second acquire for same id succeeded")
	}
	s.releaseRun(42)
	if !s.acquireRun(42) {
		t.Fatal("acquire after release rejected")
	}
}

func TestSchedulerAcquireRunAllowsDifferentIDs(t *testing.T) {
	s := &Scheduler{}
	if !s.acquireRun(1) {
		t.Fatal("first id acquire rejected")
	}
	if !s.acquireRun(2) {
		t.Fatal("different id acquire rejected")
	}
}

func TestSchedulerAddDailyRollsBackOnInvalidLaterTime(t *testing.T) {
	s := &Scheduler{
		cron:    cron.New(cron.WithLocation(time.UTC), cron.WithSeconds()),
		entries: make(map[int64][]cron.EntryID),
	}
	err := s.Add(store.ScheduledMessage{
		ID:           99,
		ScheduleType: "daily",
		DailyTimes:   []string{"08:00", "INVALID"},
		Enabled:      true,
		Status:       "active",
	})
	if err == nil {
		t.Fatal("Add succeeded, want invalid daily time error")
	}
	if got := len(s.entries[99]); got != 0 {
		t.Fatalf("entries length = %d, want 0", got)
	}
	if got := len(s.cron.Entries()); got != 0 {
		t.Fatalf("cron entries length = %d, want 0", got)
	}
}

func TestCanRunScheduledMessageAllowsManualPaused(t *testing.T) {
	err := canRunScheduledMessage(store.ScheduledMessage{Enabled: false, Status: "paused"}, true)
	if err != nil {
		t.Fatalf("manual paused returned error: %v", err)
	}
}

func TestCanRunScheduledMessageRejectsManualDeleted(t *testing.T) {
	err := canRunScheduledMessage(store.ScheduledMessage{Enabled: true, Status: "deleted"}, true)
	if err == nil || err.Error() != "scheduled message is archived/deleted, cannot run" {
		t.Fatalf("manual deleted error = %v", err)
	}
}

func TestCanRunScheduledMessageAllowsAutomaticActive(t *testing.T) {
	err := canRunScheduledMessage(store.ScheduledMessage{Enabled: true, Status: "active"}, false)
	if err != nil {
		t.Fatalf("automatic active returned error: %v", err)
	}
}

func TestSchedulerStopCancelsDeleteLater(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Scheduler{ctx: ctx, cancel: cancel, cron: cron.New(cron.WithLocation(time.UTC), cron.WithSeconds())}
	s.deleteLater(42, 1, time.Hour)
	stopCtx := s.Stop()
	select {
	case <-stopCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("Stop did not finish after canceling deleteLater")
	}
}
