package bot

import (
	"strings"
	"testing"

	tele "gopkg.in/telebot.v3"
)

func TestBuildReviewableContent_ViaBot_TextOnly(t *testing.T) {
	msg := &tele.Message{
		Text: "车仑",
		Via:  &tele.User{ID: 123456, Username: "swiftgram", FirstName: "Swiftgram"},
	}

	rv := buildReviewableContent(msg)
	aiText := aiModerationText(rv)

	if rv.Skip {
		t.Fatalf("Skip 不应该为 true")
	}
	if !strings.Contains(rv.Text, "[via inline bot: @swiftgram] 车仑") {
		t.Fatalf("Text 缺少 via 前缀: %q", rv.Text)
	}
	if !strings.Contains(aiText, viaBotAIModerationHint) {
		t.Fatalf("AI 文本缺少审核提示: %q", aiText)
	}
	if strings.Contains(rv.Text, viaBotAIModerationHint) {
		t.Fatalf("reviewable.Text 不应包含审核提示: %q", rv.Text)
	}
}

func TestBuildReviewableContent_ViaBot_Empty(t *testing.T) {
	msg := &tele.Message{
		Text: "",
		Via:  &tele.User{ID: 123456, Username: "swiftgram", FirstName: "Swiftgram"},
	}

	rv := buildReviewableContent(msg)

	if rv.Skip {
		t.Fatalf("Skip 不应该为 true")
	}
	if !strings.Contains(rv.Text, "[via inline bot: @swiftgram]") {
		t.Fatalf("Text 缺少 via 前缀: %q", rv.Text)
	}
	if !strings.Contains(rv.Text, "[无文字内容]") {
		t.Fatalf("Text 缺少无文字标记: %q", rv.Text)
	}
}

func TestBuildReviewableContent_ViaBot_WithPhoto(t *testing.T) {
	msg := &tele.Message{
		Caption: "hi",
		Via:     &tele.User{ID: 123456, Username: "swiftgram", FirstName: "Swiftgram"},
		Photo:   &tele.Photo{},
	}

	rv := buildReviewableContent(msg)

	if rv.Skip {
		t.Fatalf("Skip 不应该为 true")
	}
	if !rv.HasImage {
		t.Fatalf("HasImage 应为 true")
	}
	if !strings.Contains(rv.Text, "[via inline bot: @swiftgram] [图片] hi") {
		t.Fatalf("Text 缺少 via 前缀或 caption: %q", rv.Text)
	}
}

func TestBuildReviewableContent_NoVia_Unchanged(t *testing.T) {
	msg := &tele.Message{Text: "正常文本"}

	rv := buildReviewableContent(msg)

	if rv.Skip {
		t.Fatalf("Skip 不应该为 true")
	}
	if rv.Text != "【本次消息】正常文本" {
		t.Fatalf("Text = %q, want %q", rv.Text, "【本次消息】正常文本")
	}
	if rv.Kind != "text" {
		t.Fatalf("Kind = %q, want text", rv.Kind)
	}
	if rv.ViaBotHint {
		t.Fatalf("ViaBotHint 应为 false")
	}
}

func TestExtractViaBot_UsernameFallback(t *testing.T) {
	if got := extractViaBot(&tele.Message{Via: &tele.User{Username: "swiftgram", FirstName: "Swiftgram"}}); got != "@swiftgram" {
		t.Fatalf("username via = %q, want @swiftgram", got)
	}
	if got := extractViaBot(&tele.Message{Via: &tele.User{FirstName: "Swiftgram"}}); got != "Swiftgram" {
		t.Fatalf("firstName via = %q, want Swiftgram", got)
	}
	if got := extractViaBot(nil); got != "" {
		t.Fatalf("nil msg via = %q, want empty", got)
	}
	if got := extractViaBot(&tele.Message{}); got != "" {
		t.Fatalf("nil Via via = %q, want empty", got)
	}
}
