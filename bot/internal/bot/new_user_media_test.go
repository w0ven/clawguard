package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
)

func TestShouldContinueReviewAfterNewUserMediaDelete(t *testing.T) {
	tests := []struct {
		kind string
		want bool
	}{
		{kind: "contact", want: true},
		{kind: "poll", want: true},
		{kind: "venue", want: true},
		{kind: "invoice", want: true},
		{kind: "game", want: true},
		{kind: "document", want: true},
		{kind: "photo", want: false},
		{kind: "video", want: false},
		{kind: "animation", want: false},
		{kind: "sticker", want: false},
		{kind: "voice", want: false},
		{kind: "video_note", want: false},
		{kind: "dice", want: false},
		{kind: "location", want: false},
		{kind: "story", want: false},
		{kind: "giveaway", want: false},
		{kind: "giveaway_created", want: false},
		{kind: "giveaway_winners", want: false},
		{kind: "giveaway_completed", want: false},
		{kind: "audio", want: false},
		{kind: "media", want: false},
		{kind: "", want: false},
		{kind: "unknown", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			got := shouldContinueReviewAfterNewUserMediaDelete(tt.kind)
			if got != tt.want {
				t.Fatalf("shouldContinueReviewAfterNewUserMediaDelete(%q) = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
}

func TestCheckNewUserFilterMediaContinueReview(t *testing.T) {
	svc := &Service{}
	trust := store.UserTrust{Status: "new"}
	policy := config.FilterNewUserPolicy{
		Enabled: true,
		NoMedia: true,
	}

	tests := []struct {
		name         string
		msg          *tele.Message
		wantKind     string
		wantContinue bool
	}{
		{
			name: "poll",
			msg: &tele.Message{
				Chat:   &tele.Chat{ID: -100},
				Sender: &tele.User{ID: 42},
				Poll:   &tele.Poll{Question: "晚饭吃什么"},
			},
			wantKind:     "poll",
			wantContinue: true,
		},
		{
			name: "venue",
			msg: &tele.Message{
				Chat:     &tele.Chat{ID: -100},
				Sender:   &tele.User{ID: 42},
				Location: &tele.Location{Lat: 31.23, Lng: 121.47},
				Venue: &tele.Venue{
					Title:    "Example Hall",
					Address:  "Example Street 1",
					Location: tele.Location{Lat: 31.23, Lng: 121.47},
				},
			},
			wantKind:     "venue",
			wantContinue: true,
		},
		{
			name: "invoice",
			msg: &tele.Message{
				Chat:   &tele.Chat{ID: -100},
				Sender: &tele.User{ID: 42},
				Invoice: &tele.Invoice{
					Title:       "会员开通",
					Description: "服务费",
				},
			},
			wantKind:     "invoice",
			wantContinue: true,
		},
		{
			name: "game",
			msg: &tele.Message{
				Chat:   &tele.Chat{ID: -100},
				Sender: &tele.User{ID: 42},
				Game:   &tele.Game{},
			},
			wantKind:     "game",
			wantContinue: true,
		},
		{
			name: "document",
			msg: &tele.Message{
				Chat:     &tele.Chat{ID: -100},
				Sender:   &tele.User{ID: 42},
				Document: &tele.Document{FileName: "notes.pdf"},
			},
			wantKind:     "document",
			wantContinue: true,
		},
		{
			name: "photo without caption",
			msg: &tele.Message{
				Chat:   &tele.Chat{ID: -100},
				Sender: &tele.User{ID: 42},
				Photo:  &tele.Photo{},
			},
			wantKind:     "photo",
			wantContinue: false,
		},
		{
			name: "dice",
			msg: &tele.Message{
				Chat:   &tele.Chat{ID: -100},
				Sender: &tele.User{ID: 42},
				Dice:   &tele.Dice{},
			},
			wantKind:     "dice",
			wantContinue: false,
		},
		{
			name: "location",
			msg: &tele.Message{
				Chat:     &tele.Chat{ID: -100},
				Sender:   &tele.User{ID: 42},
				Location: &tele.Location{Lat: 31.23, Lng: 121.47},
			},
			wantKind:     "location",
			wantContinue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := svc.checkNewUserFilter(context.Background(), tt.msg, trust, policy, true)
			if err != nil {
				t.Fatalf("checkNewUserFilter returned error: %v", err)
			}
			if !result.Hit {
				t.Fatal("Hit = false, want true")
			}
			if result.Reason != "filter_newuser_no_media" {
				t.Fatalf("Reason = %q, want filter_newuser_no_media", result.Reason)
			}
			if result.MatchedRule != tt.wantKind {
				t.Fatalf("MatchedRule = %q, want %q", result.MatchedRule, tt.wantKind)
			}
			if result.Action != "delete" {
				t.Fatalf("Action = %q, want delete", result.Action)
			}
			if result.ContinueReview != tt.wantContinue {
				t.Fatalf("ContinueReview = %v, want %v for %s", result.ContinueReview, tt.wantContinue, tt.name)
			}
		})
	}
}

func TestVenueWithTopLevelLocationKeepsVenueKind(t *testing.T) {
	msg := &tele.Message{
		Location: &tele.Location{Lat: 31.23, Lng: 121.47},
		Venue: &tele.Venue{
			Title:    "Example Hall",
			Address:  "Example Street 1",
			Location: tele.Location{Lat: 31.23, Lng: 121.47},
		},
	}
	if kind := messageMediaKind(msg); kind != "venue" {
		t.Fatalf("messageMediaKind = %q, want venue", kind)
	}
	content := extractReviewableContent(msg)
	if content.Kind != "venue" {
		t.Fatalf("extractReviewableContent kind = %q, want venue", content.Kind)
	}
	if content.Skip {
		t.Fatal("extractReviewableContent skip = true, want false")
	}
	if !strings.Contains(content.Text, "Example Hall") {
		t.Fatalf("extractReviewableContent text = %q, want venue title", content.Text)
	}

	plain := &tele.Message{Location: &tele.Location{Lat: 1, Lng: 2}}
	if kind := messageMediaKind(plain); kind != "location" {
		t.Fatalf("plain location kind = %q, want location", kind)
	}
}

