//go:build integration

package bot

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/openclaw/clawguard/internal/config"
	tele "gopkg.in/telebot.v3"
)

func TestAppealPrivateStartMenu(t *testing.T) {
	pool, _, s, _ := settlementFixture(t)
	s.lifecycleCtx, s.lifecycleCancel = context.WithCancel(context.Background())
	s.cfg = config.Config{BotToken: "123:TEST", PublicBaseURL: "https://example.com"}
	tr := &miniCapture{base: &dualTelegram{role: "member"}}
	b, err := tele.NewBot(tele.Settings{Token: s.cfg.BotToken, URL: "http://telegram.test", Client: &http.Client{Transport: tr}, Synchronous: true})
	if err != nil {
		t.Fatal(err)
	}
	s.bot = b
	s.sender = b
	b.Handle("/start", func(c tele.Context) error { return s.runCommandHandler("start", c, s.handleStartCommand) })
	t.Cleanup(s.Stop)
	update := tele.Update{ID: 9, Message: &tele.Message{ID: 1, Chat: &tele.Chat{ID: 101, Type: tele.ChatPrivate}, Sender: &tele.User{ID: 101}, Text: "/start"}}
	if err = s.ProcessUpdate(update); err != nil {
		t.Fatal(err)
	}
	messages := tr.messages()
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1], "我的处理记录") || !strings.Contains(messages[len(messages)-1], "https://example.com/appeal/mini") || !strings.Contains(messages[len(messages)-1], "web_app") {
		t.Fatalf("menu %#v", messages)
	}
	var reachable bool
	if err = pool.QueryRow(context.Background(), `SELECT is_reachable FROM telegram_private_sessions WHERE user_id=101`).Scan(&reachable); err != nil || !reachable {
		t.Fatalf("private start: %v %v", reachable, err)
	}
}
