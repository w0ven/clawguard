package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
)

func TestRenderMessageTemplateMarkdownV2MixesStructuralAndPlainVars(t *testing.T) {
	rendered := RenderMessageTemplate(
		"欢迎 {user} 来到 *{group}* [说明](https://example.com/path?a=1) (_提示_)",
		"markdownv2",
		map[string]string{
			"{user}": "[可乐](tg://user?id=42)",
		},
		map[string]string{
			"{group}": "高级群[一](测试)",
		},
	)

	if rendered.ParseMode != tele.ModeMarkdownV2 {
		t.Fatalf("parse mode = %q, want %q", rendered.ParseMode, tele.ModeMarkdownV2)
	}
	if !strings.Contains(rendered.Text, "[可乐](tg://user?id=42)") {
		t.Fatalf("structural var was escaped or lost: %q", rendered.Text)
	}
	if !strings.Contains(rendered.Text, "*高级群\\[一\\]\\(测试\\)*") {
		t.Fatalf("plain var was not escaped in MarkdownV2: %q", rendered.Text)
	}
	if !strings.Contains(rendered.Text, "[说明](https://example.com/path?a=1)") {
		t.Fatalf("existing markdown link should be preserved: %q", rendered.Text)
	}
	if !strings.Contains(rendered.Text, "(_提示_)") {
		t.Fatalf("existing markdown italic syntax should be preserved: %q", rendered.Text)
	}
}

func TestRenderMessageTemplateHTMLKeepsTemplateTagsAndEscapesPlainVars(t *testing.T) {
	rendered := RenderMessageTemplate(
		"<b>欢迎</b> {user_mention} <i>{group}</i> <code>{reason}</code>",
		"html",
		map[string]string{
			"{user_mention}": `<a href="tg://user?id=42">可乐</a>`,
		},
		map[string]string{
			"{group}":  `群组 <测试>`,
			"{reason}": `bad <b>tag</b> & more`,
		},
	)

	if rendered.ParseMode != tele.ModeHTML {
		t.Fatalf("parse mode = %q, want %q", rendered.ParseMode, tele.ModeHTML)
	}
	if !strings.Contains(rendered.Text, "<b>欢迎</b>") {
		t.Fatalf("template HTML tags should be preserved: %q", rendered.Text)
	}
	if !strings.Contains(rendered.Text, `<a href="tg://user?id=42">可乐</a>`) {
		t.Fatalf("structural HTML var should be preserved: %q", rendered.Text)
	}
	if !strings.Contains(rendered.Text, "群组 &lt;测试&gt;") {
		t.Fatalf("plain HTML var should be escaped: %q", rendered.Text)
	}
	if !strings.Contains(rendered.Text, "bad &lt;b&gt;tag&lt;/b&gt; &amp; more") {
		t.Fatalf("plain HTML var should escape tags and ampersands: %q", rendered.Text)
	}
}

func TestRenderMessageTemplateMarkdownLegacyMigratesToMarkdownV2(t *testing.T) {
	rendered := RenderMessageTemplate(
		"*欢迎* {user} [群](link): {reason}",
		"markdown",
		map[string]string{
			"{user}": "@kele",
		},
		map[string]string{
			"{reason}": "<b>[test](x)</b>",
		},
	)

	if rendered.ParseMode != tele.ModeMarkdownV2 {
		t.Fatalf("parse mode = %q, want %q", rendered.ParseMode, tele.ModeMarkdownV2)
	}
	if strings.Contains(rendered.Text, "[test](x)") {
		t.Fatalf("legacy markdown plain var was not escaped after migration: %q", rendered.Text)
	}
	if !strings.Contains(rendered.Text, `\[test\]\(x\)`) {
		t.Fatalf("legacy markdown plain var missing MarkdownV2 escaping: %q", rendered.Text)
	}
}

func TestRenderMessageTemplateDefaultsUnknownToMarkdownV2(t *testing.T) {
	rendered := RenderMessageTemplate(
		"Hi {name}",
		"",
		nil,
		map[string]string{
			"{name}": "A[B](C)!",
		},
	)

	if rendered.ParseMode != tele.ModeMarkdownV2 {
		t.Fatalf("parse mode = %q, want %q", rendered.ParseMode, tele.ModeMarkdownV2)
	}
	if rendered.Text != "Hi A\\[B\\]\\(C\\)\\!" {
		t.Fatalf("unexpected MarkdownV2 default render: %q", rendered.Text)
	}
}

func TestRenderMessageTemplateMarkdownV2RealWorldChineseTemplate(t *testing.T) {
	template := `加入 *高级用户群* 即可享受：
⚡ 秒级工单响应
🎁 专属高级群福利
📥 *如需了解加入方式，请发送：*
👉 ` + "`高级群怎么进`"

	rendered := RenderMessageTemplate(template, "markdownv2", nil, nil)

	if rendered.ParseMode != tele.ModeMarkdownV2 {
		t.Fatalf("parse mode = %q, want %q", rendered.ParseMode, tele.ModeMarkdownV2)
	}
	if !strings.Contains(rendered.Text, "*高级用户群*") {
		t.Fatalf("bold syntax should be preserved: %q", rendered.Text)
	}
	if !strings.Contains(rendered.Text, "*如需了解加入方式，请发送：*") {
		t.Fatalf("bold syntax before fullwidth colon should be preserved: %q", rendered.Text)
	}
	if !strings.Contains(rendered.Text, "`高级群怎么进`") {
		t.Fatalf("code syntax should be preserved: %q", rendered.Text)
	}
}

func TestRenderWelcomeMessageUserMentionAcrossParseModes(t *testing.T) {
	baseData := welcomeTemplateData{
		Chat:       &tele.Chat{ID: -100123, Title: "高级群(测试)"},
		Group:      store.Group{Title: "备用群名", MemberCount: 7},
		RenderedAt: time.Date(2026, 4, 21, 15, 33, 0, 0, time.UTC),
	}

	cases := []struct {
		name              string
		parseMode         string
		user              *tele.User
		wantMention       string
		wantGroupSnippet  string
		unwantedSubstring string
	}{
		{
			name:              "markdownv2 username",
			parseMode:         "markdownv2",
			user:              &tele.User{ID: 42, Username: "my_name", FirstName: "可乐[测试]"},
			wantMention:       "@my\\_name",
			wantGroupSnippet:  "高级群\\(测试\\)",
			unwantedSubstring: "tg://user?id=42",
		},
		{
			name:             "markdownv2 no username",
			parseMode:        "markdownv2",
			user:             &tele.User{ID: 42, FirstName: "可乐[测试]"},
			wantMention:      "[可乐\\[测试\\]](tg://user?id=42)",
			wantGroupSnippet: "高级群\\(测试\\)",
		},
		{
			name:              "html username",
			parseMode:         "html",
			user:              &tele.User{ID: 42, Username: "my_name", FirstName: "可乐[测试]"},
			wantMention:       "@my_name",
			wantGroupSnippet:  "高级群(测试)",
			unwantedSubstring: `href="tg://user?id=42"`,
		},
		{
			name:             "html no username",
			parseMode:        "html",
			user:             &tele.User{ID: 42, FirstName: "可乐[测试]"},
			wantMention:      `<a href="tg://user?id=42">可乐[测试]</a>`,
			wantGroupSnippet: "高级群(测试)",
		},
		{
			name:              "markdown username",
			parseMode:         "markdown",
			user:              &tele.User{ID: 42, Username: "my_name", FirstName: "可乐[测试]"},
			wantMention:       "@my\\_name",
			wantGroupSnippet:  "高级群\\(测试\\)",
			unwantedSubstring: "tg://user?id=42",
		},
		{
			name:             "markdown no username",
			parseMode:        "markdown",
			user:             &tele.User{ID: 42, FirstName: "可乐[测试]"},
			wantMention:      "[可乐\\[测试\\]](tg://user?id=42)",
			wantGroupSnippet: "高级群\\(测试\\)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := baseData
			data.User = tc.user

			rendered := renderWelcomeMessage("欢迎 {user_mention} 来到 {group_title}", data, tc.parseMode)
			if !strings.Contains(rendered, tc.wantMention) {
				t.Fatalf("user mention mismatch: %q", rendered)
			}
			if !strings.Contains(rendered, tc.wantGroupSnippet) {
				t.Fatalf("group title escaping mismatch: %q", rendered)
			}
			if tc.unwantedSubstring != "" && strings.Contains(rendered, tc.unwantedSubstring) {
				t.Fatalf("unexpected fallback mention path remained: %q", rendered)
			}
		})
	}
}

func TestMentionMarkdownV2UsesUsernameBeforeLinkFallback(t *testing.T) {
	t.Run("with username", func(t *testing.T) {
		got := mentionMarkdownV2(&tele.User{ID: 42, Username: "my_name", FirstName: "可乐"})
		if got != "@my\\_name" {
			t.Fatalf("mentionMarkdownV2() = %q", got)
		}
	})

	t.Run("without username", func(t *testing.T) {
		got := mentionMarkdownV2(&tele.User{ID: 42, FirstName: "可乐"})
		if got != "[可乐](tg://user?id=42)" {
			t.Fatalf("mentionMarkdownV2() = %q", got)
		}
	})
}

func TestMentionHTMLUsesUsernameBeforeLinkFallback(t *testing.T) {
	t.Run("with username", func(t *testing.T) {
		got := mentionHTML(&tele.User{ID: 42, Username: `my_"name`, FirstName: "可乐"})
		if got != `@my_"name` {
			t.Fatalf("mentionHTML() = %q", got)
		}
	})

	t.Run("without username", func(t *testing.T) {
		got := mentionHTML(&tele.User{ID: 42, FirstName: "<可乐>"})
		if got != `<a href="tg://user?id=42">&lt;可乐&gt;</a>` {
			t.Fatalf("mentionHTML() = %q", got)
		}
	})
}
