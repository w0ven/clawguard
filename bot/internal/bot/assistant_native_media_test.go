package bot

import (
	"strings"
	"testing"

	tele "gopkg.in/telebot.v3"
)

func TestNativeMessageSerializesCurrentAndRepliedPhotosAsBotAPIArray(t *testing.T) {
	msg := &tele.Message{
		ID:       11,
		Unixtime: 1,
		Caption:  "@assistant_test_bot 看图",
		Sender:   &tele.User{ID: 7, FirstName: "成员"},
		Chat:     &tele.Chat{ID: -76543, Type: tele.ChatSuperGroup, Title: "隔离群"},
		Photo:    &tele.Photo{File: tele.File{FileID: "photo-current", UniqueID: "u1", FileSize: 24, FilePath: "/tmp/should-not-leak", FileLocal: "local.jpg"}, Width: 64, Height: 64},
		ReplyTo: &tele.Message{
			ID:      9,
			Caption: "旧图",
			Sender:  &tele.User{ID: 8, FirstName: "前人"},
			Chat:    &tele.Chat{ID: -76543, Type: tele.ChatSuperGroup, Title: "隔离群"},
			Photo:   &tele.Photo{File: tele.File{FileID: "photo-replied", UniqueID: "u9", FileSize: 18, FileURL: "https://should-not-leak.example/x"}, Width: 32, Height: 32},
		},
	}
	value, err := nativeMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	photos, ok := value["photo"].([]any)
	if !ok || len(photos) != 1 {
		t.Fatalf("current photo not a Bot API array: %#v", value["photo"])
	}
	current := photos[0].(map[string]any)
	if current["file_id"] != "photo-current" {
		t.Fatalf("missing current file_id: %#v", current)
	}
	for _, leaked := range []string{"file_path", "file_local", "file_url"} {
		if _, exists := current[leaked]; exists {
			t.Fatalf("leaked %s on current photo: %#v", leaked, current)
		}
	}
	reply := value["reply_to_message"].(map[string]any)
	replied, ok := reply["photo"].([]any)
	if !ok || len(replied) != 1 {
		t.Fatalf("replied photo not a Bot API array: %#v", reply["photo"])
	}
	old := replied[0].(map[string]any)
	if old["file_id"] != "photo-replied" {
		t.Fatalf("missing replied file_id: %#v", old)
	}
	if _, exists := old["file_url"]; exists {
		t.Fatalf("leaked file_url on replied photo: %#v", old)
	}
}

func TestNativeMessageKeepsVoicePlaceholderFieldsWithoutLocalPaths(t *testing.T) {
	msg := &tele.Message{
		ID:     3,
		Sender: &tele.User{ID: 7},
		Chat:   &tele.Chat{ID: -76543, Type: tele.ChatSuperGroup},
		Voice:  &tele.Voice{File: tele.File{FileID: "voice-1", UniqueID: "uv", FilePath: "should-not-leak"}, Duration: 2, MIME: "audio/ogg"},
	}
	value, err := nativeMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	voice := value["voice"].(map[string]any)
	if voice["file_id"] != "voice-1" {
		t.Fatalf("voice file_id missing: %#v", voice)
	}
	if _, exists := voice["file_path"]; exists {
		t.Fatalf("voice file_path leaked: %#v", voice)
	}
	if strings.TrimSpace(msg.Text) != "" {
		t.Fatal("pure voice must not invent text")
	}
}
