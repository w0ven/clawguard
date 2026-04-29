package bot

import (
	"strings"
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

func TestAIModerationTextVideoFramesUsesFrameInstruction(t *testing.T) {
	got := aiModerationText(reviewableContent{
		Text:        "[视频]",
		Kind:        "video",
		VideoFrames: [][]byte{{1}, {2}, {3}},
	})

	if got == "[视频]" {
		t.Fatalf("ai moderation text should not be bare video placeholder")
	}
	if want := "视频关键帧审核：已随请求附带 3 张按时间顺序抽取的关键帧，请以画面内容为准判断。"; got != want {
		t.Fatalf("ai moderation text = %q, want %q", got, want)
	}
}

func TestAIModerationTextVideoFramesKeepsMeaningfulCaption(t *testing.T) {
	got := aiModerationText(reviewableContent{
		Text:        "[GIF] 这是说明文字",
		Kind:        "animation",
		VideoFrames: [][]byte{{1}, {2}},
	})

	for _, want := range []string{"附带 2 张", "请以画面内容为准判断", "视频文字说明：这是说明文字"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ai moderation text should contain %q, got %q", want, got)
		}
	}
	if strings.Contains(got, "视频文字说明：[GIF]") {
		t.Fatalf("ai moderation text should strip bare GIF placeholder, got %q", got)
	}
}
