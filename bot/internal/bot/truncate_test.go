package bot

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateStringRuneSafe(t *testing.T) {
	tests := []struct {
		name  string
		value string
		max   int
		want  string
	}{
		{name: "english", value: "abcdef", max: 3, want: "abc"},
		{name: "chinese", value: "你好世界", max: 2, want: "你好"},
		{name: "mixed", value: "你a好b世c", max: 5, want: "你a好b世"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := truncateString(tt.value, tt.max)
			if out != tt.want {
				t.Fatalf("truncateString() = %q, want %q", out, tt.want)
			}
			if !utf8.ValidString(out) {
				t.Fatalf("truncateString() returned invalid UTF-8: %q", out)
			}
			if got := utf8.RuneCountInString(out); got != tt.max {
				t.Fatalf("truncateString() rune count = %d, want %d", got, tt.max)
			}
		})
	}
}

func TestTruncateStringLeavesShortValues(t *testing.T) {
	value := strings.Repeat("中", 3)
	if got := truncateString(value, 3); got != value {
		t.Fatalf("truncateString() = %q, want %q", got, value)
	}
}
