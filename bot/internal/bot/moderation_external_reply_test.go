package bot

import (
	"strings"
	"testing"

	tele "gopkg.in/telebot.v3"
)

// 跨聊天引用：用户本次消息为空 + ExternalReplyInfo 有照片 + Quote 有色情文字
// 期望：Skip=false，Text 含 "【跨聊天引用】"、HasImage=true
func TestBuildReviewableContent_ExternalReplyWithPhoto(t *testing.T) {
	msg := &tele.Message{
		Text:   "",
		Sender: &tele.User{ID: 8748009164, FirstName: "huanche"},
		Chat:   &tele.Chat{ID: -1002699516772, Title: "RFC"},
		ExternalReplyInfo: &tele.ExternalReplyInfo{
			Photo: []tele.Photo{{}},
			Origin: &tele.MessageOrigin{
				Type: "channel",
				Chat: &tele.Chat{Title: "某色情频道", Username: "haokan91_channel"},
			},
			Chat: &tele.Chat{Title: "某色情频道", Username: "haokan91_channel"},
		},
		Quote: &tele.TextQuote{Text: "深圳游艇会聚会不雅性爱视频"},
	}

	rv := buildReviewableContent(msg)

	if rv.Skip {
		t.Fatalf("Skip 不应该为 true，跨聊天引用必须进入审核")
	}
	if !rv.HasImage {
		t.Errorf("HasImage 应为 true，因为 ExternalReplyInfo.Photo 非空")
	}
	if !strings.Contains(rv.Text, "【跨聊天引用】") {
		t.Errorf("Text 缺少【跨聊天引用】标记: %q", rv.Text)
	}
	if !strings.Contains(rv.Text, "深圳游艇会") {
		t.Errorf("Text 未包含 Quote 文字: %q", rv.Text)
	}
	if !strings.Contains(rv.Text, "photo") {
		t.Errorf("Text 应包含媒体类型 photo: %q", rv.Text)
	}
	if !strings.Contains(rv.Text, "某色情频道") {
		t.Errorf("Text 应包含引用源名: %q", rv.Text)
	}
}

// 本群引用片段：ReplyTo 为 nil、ExternalReplyInfo 为 nil，只有 Quote
// 期望：Quote 文本被拼入 审核 Text
func TestBuildReviewableContent_QuoteOnly(t *testing.T) {
	msg := &tele.Message{
		Text:   "对",
		Sender: &tele.User{ID: 123, FirstName: "user"},
		Chat:   &tele.Chat{ID: -100, Title: "test"},
		Quote:  &tele.TextQuote{Text: "你好朋友"},
	}

	rv := buildReviewableContent(msg)

	if rv.Skip {
		t.Fatalf("Skip 不应该为 true")
	}
	if !strings.Contains(rv.Text, "【引用片段】") {
		t.Errorf("Text 缺少【引用片段】标记: %q", rv.Text)
	}
	if !strings.Contains(rv.Text, "你好朋友") {
		t.Errorf("Text 未包含 Quote 文字: %q", rv.Text)
	}
	if !strings.Contains(rv.Text, "对") {
		t.Errorf("Text 未包含本次消息: %q", rv.Text)
	}
}

// 跨聊天引用 + 本次消息单字
// 期望：本次消息单字被保留、Skip=false
func TestBuildReviewableContent_ExternalReplyWithSingleChar(t *testing.T) {
	msg := &tele.Message{
		Text:   "对",
		Sender: &tele.User{ID: 1, FirstName: "x"},
		Chat:   &tele.Chat{ID: -1, Title: "t"},
		ExternalReplyInfo: &tele.ExternalReplyInfo{
			Origin: &tele.MessageOrigin{
				Type: "channel",
				Chat: &tele.Chat{Title: "引流频道"},
			},
		},
		Quote: &tele.TextQuote{Text: "招工日结 200"},
	}

	rv := buildReviewableContent(msg)

	if rv.Skip {
		t.Fatalf("Skip 不应该为 true")
	}
	if !strings.Contains(rv.Text, "对") {
		t.Errorf("本次消息 '对' 未保留: %q", rv.Text)
	}
	if !strings.Contains(rv.Text, "招工日结") {
		t.Errorf("Quote 文字未保留: %q", rv.Text)
	}
	if !strings.Contains(rv.Text, "引流频道") {
		t.Errorf("引用源未保留: %q", rv.Text)
	}
}
