package scheduler

import "testing"

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
