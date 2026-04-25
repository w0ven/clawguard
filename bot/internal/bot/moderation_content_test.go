package bot

import (
	"testing"

	tele "gopkg.in/telebot.v3"
)

func TestExtractReviewableContentNonCaptionMedia(t *testing.T) {
	tests := []struct {
		name string
		msg  *tele.Message
		want reviewableContent
	}{
		{name: "video", msg: &tele.Message{Video: &tele.Video{}}, want: reviewableContent{Text: "[视频]", Kind: "video"}},
		{name: "document", msg: &tele.Message{Document: &tele.Document{FileName: "a.pdf"}}, want: reviewableContent{Text: "[文件] a.pdf", Kind: "document"}},
		{name: "audio", msg: &tele.Message{Audio: &tele.Audio{}}, want: reviewableContent{Text: "[音频]", Kind: "audio"}},
		{name: "voice", msg: &tele.Message{Voice: &tele.Voice{}}, want: reviewableContent{Text: "[语音]", Kind: "voice"}},
		{name: "sticker", msg: &tele.Message{Sticker: &tele.Sticker{}}, want: reviewableContent{Text: "[贴纸]", Kind: "sticker"}},
		{name: "animation", msg: &tele.Message{Animation: &tele.Animation{}}, want: reviewableContent{Text: "[GIF]", Kind: "animation"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractReviewableContent(tt.msg)
			if got.Skip {
				t.Fatalf("Skip = true, want false")
			}
			if got.Text != tt.want.Text || got.Kind != tt.want.Kind {
				t.Fatalf("content = (%q,%q), want (%q,%q)", got.Text, got.Kind, tt.want.Text, tt.want.Kind)
			}
		})
	}
}
