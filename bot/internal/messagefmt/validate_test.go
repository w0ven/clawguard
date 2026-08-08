package messagefmt

import "testing"

func TestValidateMarkdownV2AcceptsWellFormedTemplates(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{
			name: "inline link",
			text: "🔍 线路测试：\n• [RFC LG](https://rfchost.com/lookingGlass)\n• [Po0 LG](https://wiki.uuuz.de/looking-glass)",
		},
		{
			name: "bold and italic",
			text: "*粗体* 和 _斜体_ 都应通过",
		},
		{
			name: "escaped specials",
			text: "价格是 9\\.9 元 \\(含税\\)\\!",
		},
		{
			name: "code span protects specials",
			text: "执行 `rm -rf (dir)` 即可",
		},
		{
			name: "fenced code block",
			text: "```\nsome (raw) text. with! specials\n```",
		},
		{
			name: "blockquote marker",
			text: "> 引用行不需要转义前导符号",
		},
		{
			name: "plain chinese without specials",
			text: "这是一段没有特殊字符的中文说明",
		},
		{
			name: "url containing dash and dot inside link target",
			text: "[文档](https://wiki.uuuz.de/guide/tutorials/po0fw-whitelist)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateMarkdownV2(tc.text); err != nil {
				t.Fatalf("ValidateMarkdownV2(%q) = %v, want nil", tc.text, err)
			}
		})
	}
}

// TestValidateMarkdownV2RejectsSpaceBetweenLinkLabelAndTarget is the regression
// test for the production `lg` rule: "[Po0 LG] (https://...)" is not a link, so
// Telegram rejects the bare parenthesis with
// "Character '(' is reserved and must be escaped".
func TestValidateMarkdownV2RejectsSpaceBetweenLinkLabelAndTarget(t *testing.T) {
	text := "• [Po0 LG] (https://wiki.uuuz.de/looking-glass)"
	err := ValidateMarkdownV2(text)
	if err == nil {
		t.Fatalf("ValidateMarkdownV2(%q) = nil, want error", text)
	}
}

func TestValidateMarkdownV2RejectsUnescapedSpecials(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{name: "bare parenthesis", text: "价格 (含税)"},
		{name: "bare exclamation", text: "注意!"},
		{name: "bare dot in numbered list", text: "1. 第一步"},
		{name: "bare hyphen bullet", text: "- 项目"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateMarkdownV2(tc.text); err == nil {
				t.Fatalf("ValidateMarkdownV2(%q) = nil, want error", tc.text)
			}
		})
	}
}

func TestValidateTemplateSkipsNonMarkdownV2Modes(t *testing.T) {
	invalidForMarkdownV2 := "价格 (含税)!"

	if err := ValidateTemplate(invalidForMarkdownV2, "html"); err != nil {
		t.Fatalf("ValidateTemplate(html) = %v, want nil", err)
	}
	if err := ValidateTemplate("", "markdownv2"); err != nil {
		t.Fatalf("ValidateTemplate(empty) = %v, want nil", err)
	}
	// An empty parse mode still resolves to MarkdownV2 at send time, so it must
	// be validated as MarkdownV2 rather than skipped.
	if err := ValidateTemplate(invalidForMarkdownV2, ""); err == nil {
		t.Fatalf("ValidateTemplate(empty parse mode) = nil, want error")
	}
	if err := ValidateTemplate(invalidForMarkdownV2, "markdown"); err == nil {
		t.Fatalf("ValidateTemplate(markdown) = nil, want error")
	}
}

// TestValidateMarkdownV2AgreesWithRenderer guards against validation and
// rendering drifting apart: anything the renderer emits for a plain template
// must still be considered valid.
func TestValidateMarkdownV2AgreesWithRenderer(t *testing.T) {
	plain := "价格 (含税)! 1. 第一步"
	rendered := RenderMessageTemplate("{note}", "markdownv2", nil, map[string]string{"{note}": plain})
	if err := ValidateMarkdownV2(rendered.Text); err != nil {
		t.Fatalf("rendered output rejected by validator: %v (text=%q)", err, rendered.Text)
	}
}
