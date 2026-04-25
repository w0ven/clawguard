package api

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateProfileCheckLogBioRuneSafe(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "english", value: strings.Repeat("a", 201), want: strings.Repeat("a", 200) + "…"},
		{name: "chinese", value: strings.Repeat("中", 201), want: strings.Repeat("中", 200) + "…"},
		{name: "mixed", value: strings.Repeat("中a", 101), want: strings.Repeat("中a", 100) + "…"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := truncateProfileCheckLogBio(&tt.value)
			if out == nil {
				t.Fatal("truncateProfileCheckLogBio() returned nil")
			}
			if *out != tt.want {
				t.Fatalf("truncateProfileCheckLogBio() = %q, want %q", *out, tt.want)
			}
			if !utf8.ValidString(*out) {
				t.Fatalf("truncateProfileCheckLogBio() returned invalid UTF-8: %q", *out)
			}
			withoutEllipsis := strings.TrimSuffix(*out, "…")
			if got := utf8.RuneCountInString(withoutEllipsis); got != 200 {
				t.Fatalf("truncated bio rune count = %d, want 200", got)
			}
		})
	}
}

func TestTruncateProfileCheckLogBioLeavesShortValues(t *testing.T) {
	value := strings.Repeat("中", 200)
	out := truncateProfileCheckLogBio(&value)
	if out == nil || *out != value {
		t.Fatalf("truncateProfileCheckLogBio() = %v, want %q", out, value)
	}
}
