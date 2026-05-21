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
		{name: "document", msg: &tele.Message{Document: &tele.Document{FileName: "a.pdf"}}, want: reviewableContent{Text: "[文件] filename=a.pdf", Kind: "document"}},
		{name: "audio", msg: &tele.Message{Audio: &tele.Audio{}}, want: reviewableContent{Text: "[音频]", Kind: "audio"}},
		{name: "voice", msg: &tele.Message{Voice: &tele.Voice{}}, want: reviewableContent{Text: "[语音]", Kind: "voice"}},
		{name: "sticker", msg: &tele.Message{Sticker: &tele.Sticker{}}, want: reviewableContent{Text: "[贴纸]", Kind: "sticker"}},
		{name: "animation", msg: &tele.Message{Animation: &tele.Animation{}}, want: reviewableContent{Text: "[动图]", Kind: "animation"}},
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

func TestExtractReviewableContentAdditionalTelegramTypes(t *testing.T) {
	tests := []struct {
		name       string
		msg        *tele.Message
		wantKind   string
		wantParts  []string
		wantPrefix string
	}{
		{
			name:     "poll",
			msg:      &tele.Message{Poll: &tele.Poll{Question: "选哪个", Options: []tele.PollOption{{Text: "A"}, {Text: "B"}}}},
			wantKind: "poll",
			wantParts: []string{
				"[投票]", "question=选哪个", "option1=A", "option2=B",
			},
		},
		{name: "dice", msg: &tele.Message{Dice: &tele.Dice{Type: "🎰", Value: 64}}, wantKind: "dice", wantParts: []string{"[骰子]", "emoji=🎰", "value=64"}},
		{name: "location", msg: &tele.Message{Location: &tele.Location{Lat: 31.23, Lng: 121.47}}, wantKind: "location", wantParts: []string{"[位置]", "lat=31.23", "lng=121.47"}},
		{name: "venue", msg: &tele.Message{Venue: &tele.Venue{Title: "店", Address: "路", Location: tele.Location{Lat: 31.23, Lng: 121.47}}}, wantKind: "venue", wantParts: []string{"[地点]", "title=店", "address=路", "lat=31.23", "lng=121.47"}},
		{name: "game", msg: &tele.Message{Game: &tele.Game{Title: "游戏", Description: "描述"}}, wantKind: "game", wantParts: []string{"[游戏]", "title=游戏", "description=描述"}},
		{name: "invoice", msg: &tele.Message{Invoice: &tele.Invoice{Title: "订单", Description: "说明", Total: 123, Currency: "USD"}}, wantKind: "invoice", wantParts: []string{"[Invoice]", "title=订单", "description=说明", "total=123", "currency=USD"}},
		{name: "story", msg: &tele.Message{Story: &tele.Story{ID: 9, Poster: &tele.Chat{ID: -100, Title: "频道"}}}, wantKind: "story", wantParts: []string{"[Story 转发]", "来源=频道/9"}},
		{name: "giveaway", msg: &tele.Message{Giveaway: &tele.Giveaway{PrizeDescription: "奖品", WinnerCount: 3, SelectionUnixtime: 1710000000}}, wantKind: "giveaway", wantParts: []string{"[赠品]", "描述=奖品", "数量=3", "截止=2024-03-10T"}},
		{name: "giveaway_created", msg: &tele.Message{GiveawayCreated: &tele.GiveawayCreated{}}, wantKind: "giveaway_created", wantParts: []string{"[赠品]", "描述=created"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildReviewableContent(tt.msg)
			if got.Skip {
				t.Fatalf("Skip = true, want false")
			}
			if got.Kind != tt.wantKind {
				t.Fatalf("Kind = %q, want %q", got.Kind, tt.wantKind)
			}
			for _, part := range tt.wantParts {
				if !strings.Contains(got.Text, part) {
					t.Fatalf("Text should contain %q, got %q", part, got.Text)
				}
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
