package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"testing"

	tele "gopkg.in/telebot.v3"
)

type confirmationTransport struct {
	mu       sync.Mutex
	response string
	status   int
	methods  []string
	payloads []map[string]any
}

func (t *confirmationTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	method := path.Base(r.URL.Path)
	raw, _ := io.ReadAll(r.Body)
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	if method == "getMe" {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"bot"}}`))}, nil
	}
	t.mu.Lock()
	t.methods = append(t.methods, method)
	t.payloads = append(t.payloads, p)
	body, status := t.response, t.status
	t.mu.Unlock()
	if status == 0 {
		status = 200
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}
func TestPermissionOperationsRequireConfirmedBoolean(t *testing.T) {
	for _, reply := range []struct {
		name, body    string
		wantConfirmed bool
	}{
		{"ok", `{"ok":true,"result":true}`, true}, {"bad json", `<html>bad</html>`, false}, {"missing result", `{"ok":true}`, false}, {"false result", `{"ok":true,"result":false}`, false},
	} {
		t.Run(reply.name, func(t *testing.T) {
			tr := &confirmationTransport{response: reply.body}
			b, err := tele.NewBot(tele.Settings{Token: "123:TEST", URL: "http://telegram.test", Client: &http.Client{Transport: tr}})
			if err != nil {
				t.Fatal(err)
			}
			chat, user := &tele.Chat{ID: -7}, &tele.User{ID: 42}
			member := &tele.ChatMember{User: user, Rights: tele.NoRights(), RestrictedUntil: 123}
			operations := []struct {
				name   string
				call   func() error
				method string
			}{
				{"ban", func() error { return telegramConfirmedBan(b, chat, member, true) }, "kickChatMember"},
				{"unban", func() error { return telegramConfirmedUnban(b, chat, user, true) }, "unbanChatMember"},
				{"restrict", func() error { return telegramConfirmedRestrict(b, chat, member) }, "restrictChatMember"},
				{"sender ban", func() error { return telegramConfirmedBanSenderChat(b, chat, &tele.Chat{ID: -9}) }, "banChatSenderChat"},
			}
			for _, op := range operations {
				err := op.call()
				if reply.wantConfirmed && err != nil {
					t.Fatalf("%s: %v", op.name, err)
				}
				if !reply.wantConfirmed && !errors.Is(err, ErrTelegramResultUnconfirmed) {
					t.Fatalf("%s ambiguous: %v", op.name, err)
				}
			}
			if len(tr.methods) != 4 {
				t.Fatal(tr.methods)
			}
			for i, op := range operations {
				if tr.methods[i] != op.method {
					t.Fatalf("method %v", tr.methods)
				}
			}
			if tr.payloads[0]["revoke_messages"] != "true" || tr.payloads[0]["until_date"] != "123" {
				t.Fatalf("ban payload %v", tr.payloads[0])
			}
			if tr.payloads[1]["only_if_banned"] != "true" {
				t.Fatalf("unban payload %v", tr.payloads[1])
			}
			if tr.payloads[2]["until_date"] != "123" || tr.payloads[2]["permissions"] == nil {
				t.Fatalf("restrict payload %v", tr.payloads[2])
			}
		})
	}
}
func TestPermissionExplicitTelegramRejectionIsPreserved(t *testing.T) {
	tr := &confirmationTransport{status: 403, response: `{"ok":false,"error_code":403,"description":"Forbidden: bot was kicked"}`}
	b, err := tele.NewBot(tele.Settings{Token: "123:TEST", URL: "http://telegram.test", Client: &http.Client{Transport: tr}})
	if err != nil {
		t.Fatal(err)
	}
	err = telegramConfirmedRestrict(b, &tele.Chat{ID: -7}, &tele.ChatMember{User: &tele.User{ID: 42}})
	if err == nil || errors.Is(err, ErrTelegramResultUnconfirmed) {
		t.Fatalf("403 misclassified: %v", err)
	}
	var tg *tele.Error
	if !errors.As(err, &tg) && !strings.Contains(err.Error(), "(403)") {
		t.Fatalf("lost 403 error identity: %v", err)
	}
}
func TestUnbanNoOpAndUnknownAreDistinct(t *testing.T) {
	tr := &confirmationTransport{response: `{"ok":false,"error_code":400,"description":"Bad Request: user is not banned"}`}
	b, err := tele.NewBot(tele.Settings{Token: "123:TEST", URL: "http://telegram.test", Client: &http.Client{Transport: tr}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{bot: b}
	warning, err := s.SafeUnbanChatUserNoLock(context.Background(), -7, 42)
	if err != nil || warning == "" {
		t.Fatalf("no-op warning=%q err=%v", warning, err)
	}
	tr.mu.Lock()
	tr.response = `{"ok":true}`
	tr.mu.Unlock()
	warning, err = s.SafeUnbanChatUserNoLock(context.Background(), -7, 42)
	if warning != "" || !errors.Is(err, ErrTelegramResultUnconfirmed) {
		t.Fatalf("unknown warning=%q err=%v", warning, err)
	}
}
func TestKickSecondStepUnconfirmedIsPartial(t *testing.T) {
	err := runVerificationKick(func() error { return nil }, func() error { return ErrTelegramResultUnconfirmed })
	var partial *actionAppliedError
	if !errors.As(err, &partial) || !errors.Is(err, ErrTelegramResultUnconfirmed) {
		t.Fatalf("kick partial=%v", err)
	}
	if err = runVerificationKick(func() error { return ErrTelegramResultUnconfirmed }, func() error { return fmt.Errorf("must not run") }); !errors.Is(err, ErrTelegramResultUnconfirmed) {
		t.Fatalf("first step unknown: %v", err)
	}
}
