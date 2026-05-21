package bot

import (
	"strings"
	"testing"

	tele "gopkg.in/telebot.v3"
)

func TestBuildReviewableContent_TextAndReplyTextIncludesBothBlocks(t *testing.T) {
	msg := &tele.Message{
		Text:   "收到",
		Sender: &tele.User{ID: 1},
		ReplyTo: &tele.Message{
			Text:   "原消息广告",
			Sender: &tele.User{ID: 2},
		},
	}

	rv := buildReviewableContent(msg)

	requireReviewTextContains(t, rv, "【本次消息】收到", "【引用回复】", "原消息广告")
}

func TestBuildReviewableContent_EmojiReplyPhotoCaptionIncludesPhotoAndCaption(t *testing.T) {
	msg := &tele.Message{
		Text:   "💍",
		Sender: &tele.User{ID: 1},
		ReplyTo: &tele.Message{
			Caption: "今晚广告",
			Photo:   &tele.Photo{},
			Sender:  &tele.User{ID: 2},
		},
	}

	rv := buildReviewableContent(msg)

	if !rv.HasImage {
		t.Fatalf("HasImage = false, want true")
	}
	requireReviewTextContains(t, rv, "【本次消息】💍", "【引用回复】", "[图片]", "今晚广告")
}

func TestBuildReviewableContent_EmojiReplyPhotoNoCaptionIncludesPhoto(t *testing.T) {
	msg := &tele.Message{
		Text:   "💍",
		Sender: &tele.User{ID: 1},
		ReplyTo: &tele.Message{
			Photo:  &tele.Photo{},
			Sender: &tele.User{ID: 2},
		},
	}

	rv := buildReviewableContent(msg)

	if !rv.HasImage {
		t.Fatalf("HasImage = false, want true")
	}
	requireReviewTextContains(t, rv, "【本次消息】💍", "【引用回复】", "[图片]")
}

func TestBuildReviewableContent_TextAndQuoteIncludesQuoteBlock(t *testing.T) {
	msg := &tele.Message{
		Text:  "看这个",
		Quote: &tele.TextQuote{Text: "引用广告片段"},
	}

	rv := buildReviewableContent(msg)

	requireReviewTextContains(t, rv, "【本次消息】看这个", "【引用片段】引用广告片段")
}

func TestBuildReviewableContent_ExternalReplyPhotoQuoteIncludesSourceKindAndImage(t *testing.T) {
	msg := &tele.Message{
		Text: "💍",
		ExternalReplyInfo: &tele.ExternalReplyInfo{
			Photo: []tele.Photo{{}},
			Chat:  &tele.Chat{Title: "广告频道", Username: "ad_channel"},
			Origin: &tele.MessageOrigin{
				Type: "channel",
				Chat: &tele.Chat{Title: "广告频道", Username: "ad_channel"},
			},
		},
		Quote: &tele.TextQuote{Text: "跨聊天广告片段"},
	}

	rv := buildReviewableContent(msg)

	if !rv.HasImage {
		t.Fatalf("HasImage = false, want true")
	}
	requireReviewTextContains(t, rv, "【跨聊天引用】", "广告频道", "类型: photo", "跨聊天广告片段", "[图片]")
}

func TestBuildReviewableContent_RepresentativeNonTextMessagesDoNotSkip(t *testing.T) {
	tests := []struct {
		name string
		msg  *tele.Message
		want string
	}{
		{name: "photo", msg: &tele.Message{Photo: &tele.Photo{}}, want: "[图片]"},
		{name: "video", msg: &tele.Message{Video: &tele.Video{FileName: "v.mp4", MIME: "video/mp4"}}, want: "[视频]"},
		{name: "document", msg: &tele.Message{Document: &tele.Document{FileName: "a.pdf", MIME: "application/pdf"}}, want: "[文件]"},
		{name: "sticker", msg: &tele.Message{Sticker: &tele.Sticker{Emoji: "💍", SetName: "ads"}}, want: "[贴纸]"},
		{name: "contact", msg: &tele.Message{Contact: &tele.Contact{FirstName: "Ad", PhoneNumber: "+1000"}}, want: "[联系人]"},
		{name: "poll", msg: &tele.Message{Poll: &tele.Poll{Question: "选吗", Options: []tele.PollOption{{Text: "A"}}}}, want: "[投票]"},
		{name: "location", msg: &tele.Message{Location: &tele.Location{Lat: 31.23, Lng: 121.47}}, want: "[位置]"},
		{name: "venue", msg: &tele.Message{Venue: &tele.Venue{Title: "店", Address: "路", Location: tele.Location{Lat: 31.23, Lng: 121.47}}}, want: "[地点]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rv := buildReviewableContent(tt.msg)
			requireReviewTextContains(t, rv, "【本次消息】", tt.want)
		})
	}
}

func TestBuildReviewableContent_ServiceMessageWithoutReviewableContentSkips(t *testing.T) {
	rv := buildReviewableContent(&tele.Message{UsersJoined: []tele.User{{ID: 1}}})

	if !rv.Skip {
		t.Fatalf("Skip = false, want true; text=%q", rv.Text)
	}
}

func requireReviewTextContains(t *testing.T, rv reviewableContent, parts ...string) {
	t.Helper()
	if rv.Skip {
		t.Fatalf("Skip = true, want false")
	}
	for _, part := range parts {
		if !strings.Contains(rv.Text, part) {
			t.Fatalf("Text should contain %q, got %q", part, rv.Text)
		}
	}
}
