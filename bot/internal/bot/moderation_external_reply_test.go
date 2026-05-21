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

func TestBuildReviewableContent_ReplyPhotoWithCaption(t *testing.T) {
	msg := &tele.Message{
		Text:   "💍",
		Sender: &tele.User{ID: 2, FirstName: "x"},
		Chat:   &tele.Chat{ID: -2, Title: "t"},
		ReplyTo: &tele.Message{
			Sender: &tele.User{ID: 3, FirstName: "ad"},
			Chat:   &tele.Chat{ID: -2, Title: "t"},
			Photo:  &tele.Photo{Caption: "今晚不雅广告 text"},
		},
	}

	rv := buildReviewableContent(msg)

	if rv.Skip {
		t.Fatalf("Skip 不应该为 true")
	}
	if rv.Kind != "text" {
		t.Fatalf("Kind = %q, want text", rv.Kind)
	}
	if !rv.HasImage {
		t.Fatalf("HasImage 应为 true，因为 ReplyTo 是 photo")
	}
	if !strings.Contains(rv.Text, "【引用回复】") {
		t.Fatalf("Text 缺少【引用回复】标记: %q", rv.Text)
	}
	if !strings.Contains(rv.Text, "今晚不雅广告 text") {
		t.Fatalf("Text 未包含 caption: %q", rv.Text)
	}
	if strings.TrimSpace(rv.Text) == "💍" {
		t.Fatalf("reply preview 未展开，text=%q", rv.Text)
	}
}

func TestBuildReviewableContent_ReplyPhotoWithoutCaption(t *testing.T) {
	msg := &tele.Message{
		Text:   "💍",
		Sender: &tele.User{ID: 4, FirstName: "x"},
		Chat:   &tele.Chat{ID: -3, Title: "t"},
		ReplyTo: &tele.Message{
			Sender: &tele.User{ID: 5, FirstName: "ad"},
			Chat:   &tele.Chat{ID: -3, Title: "t"},
			Photo:  &tele.Photo{},
		},
	}

	rv := buildReviewableContent(msg)

	if rv.Skip {
		t.Fatalf("Skip 不应该为 true")
	}
	if !strings.Contains(rv.Text, "【引用回复】") {
		t.Fatalf("Text 缺少【引用回复】标记: %q", rv.Text)
	}
	if !strings.Contains(rv.Text, "[图片]") {
		t.Fatalf("Text 未包含 [图片]: %q", rv.Text)
	}
	if !rv.HasImage {
		t.Fatalf("HasImage 应为 true")
	}
}

func TestBuildReviewableContent_QuoteOnlyShortEmoji(t *testing.T) {
	msg := &tele.Message{
		Text:   "💍",
		Sender: &tele.User{ID: 6, FirstName: "x"},
		Chat:   &tele.Chat{ID: -4, Title: "t"},
		Quote:  &tele.TextQuote{Text: "广告词：加我看福利"},
	}

	rv := buildReviewableContent(msg)

	if rv.Skip {
		t.Fatalf("Skip 不应该为 true")
	}
	if !strings.Contains(rv.Text, "【引用片段】") {
		t.Fatalf("Text 缺少【引用片段】标记: %q", rv.Text)
	}
	if !strings.Contains(rv.Text, "广告词：加我看福利") {
		t.Fatalf("Text 未包含 Quote 文字: %q", rv.Text)
	}
	if !strings.Contains(rv.Text, "💍") {
		t.Fatalf("当前短正文未保留: %q", rv.Text)
	}
}

func TestBuildReviewableContent_ExternalReplyWithQuoteShortEmoji(t *testing.T) {
	msg := &tele.Message{
		Text:   "💍",
		Sender: &tele.User{ID: 7, FirstName: "x"},
		Chat:   &tele.Chat{ID: -5, Title: "t"},
		ExternalReplyInfo: &tele.ExternalReplyInfo{
			Photo: []tele.Photo{{}},
			Origin: &tele.MessageOrigin{
				Type: "channel",
				Chat: &tele.Chat{Title: "某频道", Username: "ad_channel"},
			},
			Chat: &tele.Chat{Title: "某频道", Username: "ad_channel"},
		},
		Quote: &tele.TextQuote{Text: "深圳游艇会聚会不雅性爱视频"},
	}

	rv := buildReviewableContent(msg)

	if rv.Skip {
		t.Fatalf("Skip 不应该为 true")
	}
	if !rv.HasImage {
		t.Fatalf("HasImage 应为 true，因为 ExternalReplyInfo.Photo 非空")
	}
	if !strings.Contains(rv.Text, "【跨聊天引用】") {
		t.Fatalf("Text 缺少【跨聊天引用】标记: %q", rv.Text)
	}
	if !strings.Contains(rv.Text, "深圳游艇会") {
		t.Fatalf("Text 未包含 Quote 文字: %q", rv.Text)
	}
	if !strings.Contains(rv.Text, "💍") {
		t.Fatalf("当前短正文未保留: %q", rv.Text)
	}
}

func TestBuildReviewableContent_ShortEmojiAloneStillReviewable(t *testing.T) {
	msg := &tele.Message{
		Text:   "💍",
		Sender: &tele.User{ID: 8, FirstName: "x"},
		Chat:   &tele.Chat{ID: -6, Title: "t"},
	}

	rv := buildReviewableContent(msg)

	if rv.Skip {
		t.Fatalf("Skip 不应该为 true")
	}
	if rv.Text != "【本次消息】💍" {
		t.Fatalf("Text = %q, want %q", rv.Text, "【本次消息】💍")
	}
	if rv.Kind != "text" {
		t.Fatalf("Kind = %q, want text", rv.Kind)
	}
}
