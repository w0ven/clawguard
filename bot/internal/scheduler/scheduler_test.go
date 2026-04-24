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
