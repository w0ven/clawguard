package messagefmt

import "testing"

func TestPlainVariablesDoNotExpandOtherPlaceholders(t *testing.T) {
	rendered := RenderMessageTemplate("{reason} / {group}", "html", nil, map[string]string{"{reason}": "user wrote {group}", "{group}": "<important>"})
	if rendered.Text != "user wrote {group} / &lt;important&gt;" {
		t.Fatalf("unexpected recursive substitution: %q", rendered.Text)
	}
}
func TestPlainMarkdownVariablesEscapeBackslashes(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{`end\`, `*end\\*`},
		{`\*evil*`, `*\\\*evil\**`},
	} {
		got := RenderMessageTemplate("*{reason}*", "markdownv2", nil, map[string]string{"{reason}": tc.value}).Text
		if got != tc.want {
			t.Fatalf("value=%q got=%q want=%q", tc.value, got, tc.want)
		}
	}
}
