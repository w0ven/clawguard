package bot

import (
	"testing"

	"github.com/openclaw/clawguard/internal/config"
	tele "gopkg.in/telebot.v3"
)

func TestShouldRunVideoModeration_KindGate(t *testing.T) {
	policy := config.AIPolicy{VideoModerationEnabled: true}

	cases := []struct {
		name             string
		msg              *tele.Message
		includeVideoNote bool
		want             bool
	}{
		{name: "video", msg: &tele.Message{Video: &tele.Video{}}, want: true},
		{name: "animation", msg: &tele.Message{Animation: &tele.Animation{}}, want: true},
		{name: "video note disabled", msg: &tele.Message{VideoNote: &tele.VideoNote{}}, want: false},
		{name: "video note enabled", msg: &tele.Message{VideoNote: &tele.VideoNote{}}, includeVideoNote: true, want: true},
		{name: "sticker", msg: &tele.Message{Sticker: &tele.Sticker{}}, want: false},
		{name: "text", msg: &tele.Message{Text: "hello"}, want: false},
		{name: "media caption without video", msg: &tele.Message{Caption: "图说", Photo: &tele.Photo{}}, want: false},
		{name: "nil message", msg: nil, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy.IncludeVideoNote = tc.includeVideoNote
			got := shouldRunVideoModeration(tc.msg, policy)
			if got != tc.want {
				t.Fatalf("expected %t, got %t", tc.want, got)
			}
		})
	}
}

func TestShouldRunVideoModeration_DisabledPolicy(t *testing.T) {
	policy := config.AIPolicy{VideoModerationEnabled: false, IncludeVideoNote: true}
	msgs := []struct {
		name string
		msg  *tele.Message
	}{
		{name: "video", msg: &tele.Message{Video: &tele.Video{}}},
		{name: "animation", msg: &tele.Message{Animation: &tele.Animation{}}},
		{name: "video_note", msg: &tele.Message{VideoNote: &tele.VideoNote{}}},
		{name: "sticker", msg: &tele.Message{Sticker: &tele.Sticker{}}},
		{name: "text", msg: &tele.Message{Text: "x"}},
		{name: "media_caption", msg: &tele.Message{Caption: "图说", Photo: &tele.Photo{}}},
		{name: "captioned video", msg: &tele.Message{Caption: "说明文字", Video: &tele.Video{}}},
	}
	for _, tc := range msgs {
		t.Run(tc.name, func(t *testing.T) {
			if shouldRunVideoModeration(tc.msg, policy) {
				t.Fatalf("expected disabled policy to skip %s", tc.name)
			}
		})
	}
}

func TestShouldRunVideoModeration_CurrentMessageMediaNotMergedMeta(t *testing.T) {
	policyOn := config.AIPolicy{VideoModerationEnabled: true}
	policyOff := config.AIPolicy{VideoModerationEnabled: false, IncludeVideoNote: true}
	quotedVideo := &tele.Message{Video: &tele.Video{}, Sender: &tele.User{ID: 2}}
	externalVideo := &tele.ExternalReplyInfo{
		Video:  &tele.Video{},
		Origin: &tele.MessageOrigin{Type: "channel", Chat: &tele.Chat{Title: "src"}},
	}

	videoNoCaption := &tele.Message{Video: &tele.Video{}}
	videoWithCaption := &tele.Message{Caption: "说明文字", Video: &tele.Video{}}
	photoCaption := &tele.Message{Caption: "图说", Photo: &tele.Photo{}}
	documentCaption := &tele.Message{Caption: "文件说明", Document: &tele.Document{FileName: "a.pdf"}}
	videoNote := &tele.Message{VideoNote: &tele.VideoNote{}}
	videoNoteCaption := &tele.Message{Caption: "圆片说明", VideoNote: &tele.VideoNote{}}
	textReplyToVideo := &tele.Message{Text: "看看这个", ReplyTo: quotedVideo}
	photoCaptionReplyToVideo := &tele.Message{Caption: "图说", Photo: &tele.Photo{}, ReplyTo: quotedVideo}
	documentCaptionReplyToVideo := &tele.Message{
		Caption:  "文件说明",
		Document: &tele.Document{FileName: "a.pdf"},
		ReplyTo:  quotedVideo,
	}
	textExternalVideo := &tele.Message{Text: "看看这个", ExternalReplyInfo: externalVideo}
	photoCaptionExternalVideo := &tele.Message{
		Caption:           "图说",
		Photo:             &tele.Photo{},
		ExternalReplyInfo: externalVideo,
	}
	documentCaptionExternalVideo := &tele.Message{
		Caption:           "文件说明",
		Document:          &tele.Document{FileName: "a.pdf"},
		ExternalReplyInfo: externalVideo,
	}
	captionedVideoReplyToVideo := &tele.Message{Caption: "说明文字", Video: &tele.Video{}, ReplyTo: quotedVideo}

	assertExtract := func(t *testing.T, msg *tele.Message, wantKind, wantSource string) {
		t.Helper()
		got := extractReviewableContent(msg)
		if got.Kind != wantKind || got.VideoMeta.Source != wantSource {
			t.Fatalf("extract kind/source = %q/%q, want %q/%q", got.Kind, got.VideoMeta.Source, wantKind, wantSource)
		}
	}
	assertExtract(t, videoNoCaption, "video", "video")
	assertExtract(t, videoWithCaption, "media_caption", "video")
	assertExtract(t, photoCaption, "media_caption", "")
	assertExtract(t, documentCaption, "media_caption", "")

	// mergeReviewableContent copies quoted VideoMeta onto caption photo/document
	// while Kind stays media_caption. The gate must ignore that merged Source.
	mergedPhotoReply := buildReviewableContent(photoCaptionReplyToVideo)
	if mergedPhotoReply.Kind != "media_caption" || mergedPhotoReply.VideoMeta.Source != "video" {
		t.Fatalf("photo caption reply merge kind/source = %q/%q, want media_caption/video", mergedPhotoReply.Kind, mergedPhotoReply.VideoMeta.Source)
	}
	mergedDocReply := buildReviewableContent(documentCaptionReplyToVideo)
	if mergedDocReply.Kind != "media_caption" || mergedDocReply.VideoMeta.Source != "video" {
		t.Fatalf("document caption reply merge kind/source = %q/%q, want media_caption/video", mergedDocReply.Kind, mergedDocReply.VideoMeta.Source)
	}

	cases := []struct {
		name   string
		msg    *tele.Message
		policy config.AIPolicy
		want   bool
	}{
		{name: "video without caption triggers", msg: videoNoCaption, policy: policyOn, want: true},
		{name: "video with caption triggers", msg: videoWithCaption, policy: policyOn, want: true},
		{name: "captioned video reply to video still triggers", msg: captionedVideoReplyToVideo, policy: policyOn, want: true},
		{name: "video without caption disabled", msg: videoNoCaption, policy: policyOff, want: false},
		{name: "video with caption disabled", msg: videoWithCaption, policy: policyOff, want: false},
		{name: "photo caption not treated as video", msg: photoCaption, policy: policyOn, want: false},
		{name: "document caption not treated as video", msg: documentCaption, policy: policyOn, want: false},
		{name: "photo caption reply to video does not inherit frame extract", msg: photoCaptionReplyToVideo, policy: policyOn, want: false},
		{name: "document caption reply to video does not inherit frame extract", msg: documentCaptionReplyToVideo, policy: policyOn, want: false},
		{name: "video note without include", msg: videoNote, policy: policyOn, want: false},
		{name: "video note with include", msg: videoNote, policy: config.AIPolicy{VideoModerationEnabled: true, IncludeVideoNote: true}, want: true},
		{name: "captioned video note without include", msg: videoNoteCaption, policy: policyOn, want: false},
		{name: "captioned video note with include", msg: videoNoteCaption, policy: config.AIPolicy{VideoModerationEnabled: true, IncludeVideoNote: true}, want: true},
		{name: "text reply to video does not inherit frame extract", msg: textReplyToVideo, policy: policyOn, want: false},
		{name: "text external video does not inherit frame extract", msg: textExternalVideo, policy: policyOn, want: false},
		{name: "photo caption external video does not inherit frame extract", msg: photoCaptionExternalVideo, policy: policyOn, want: false},
		{name: "document caption external video does not inherit frame extract", msg: documentCaptionExternalVideo, policy: policyOn, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldRunVideoModeration(tc.msg, tc.policy)
			if got != tc.want {
				t.Fatalf("expected %t, got %t", tc.want, got)
			}
		})
	}
}
