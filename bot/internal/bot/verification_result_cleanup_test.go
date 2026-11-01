package bot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

type resultRequest struct {
	method string
	body   map[string]any
}

type resultTransport struct {
	requests               []resultRequest
	editError, deleteError bool
}

func (tr *resultTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	method := path.Base(r.URL.Path)
	body := make(map[string]any)
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
	}
	tr.requests = append(tr.requests, resultRequest{method, body})
	response := `{"ok":true,"result":true}`
	switch {
	case method == "getMe":
		response = `{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"test"}}`
	case method == "editMessageText" && tr.editError:
		response = `{"ok":false,"error_code":400,"description":"Bad Request: message can't be edited"}`
	case method == "editMessageText":
		response = `{"ok":true,"result":{"message_id":111,"chat":{"id":-1001,"type":"supergroup"}}}`
	case method == "deleteMessage" && tr.deleteError:
		response = `{"ok":false,"error_code":400,"description":"Bad Request: message to delete not found"}`
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
}

func newResultService(t *testing.T, tr *resultTransport) (*Service, *observer.ObservedLogs) {
	t.Helper()
	bot, err := tele.NewBot(tele.Settings{Token: "123:TEST", URL: "http://telegram.test", Synchronous: true, Client: &http.Client{Transport: tr}})
	if err != nil {
		t.Fatal(err)
	}
	core, logs := observer.New(zap.DebugLevel)
	tr.requests = nil
	return &Service{bot: bot, logger: zap.New(core)}, logs
}

func TestVerificationResultCleanup(t *testing.T) {
	for _, tc := range []struct {
		name, kind         string
		seconds            int
		enabled, editError bool
	}{
		{"timeout", "timeout", 5, true, false},
		{"wrong answer", "failed", 60, true, false},
		{"admin reject", "admin_reject", 9, true, false},
		{"pass", "passed", 13, true, false},
		{"admin pass", "admin_pass", 17, true, false},
		{"disabled extra feedback", "timeout", 5, false, false},
		{"zero retains card", "failed", 0, true, false},
		{"edit failure", "timeout", 5, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &resultTransport{editError: tc.editError}
			s, logs := newResultService(t, tr)
			chat := &tele.Chat{ID: -1001}
			id := int64(111)
			pending := store.PendingVerification{Method: "button", JoinMessageID: &id}
			feedback := config.ActionFeedback{Enabled: tc.enabled, AutoDeleteSeconds: tc.seconds, Template: "extra feedback"}
			var delay time.Duration
			var job func()
			s.finalizeVerificationPromptWithScheduler(chat, pending, verificationResultHTML(&tele.User{ID: 42, FirstName: "Abdul"}, tc.kind), feedback.AutoDeleteSeconds, func(d time.Duration, f func()) { delay, job = d, f })
			if !feedback.Enabled {
				s.sendActionFeedback(chat, nil, feedback, nil)
			}
			if len(tr.requests) != 1 || tr.requests[0].method != "editMessageText" {
				t.Fatalf("before timer requests = %+v", tr.requests)
			}
			if tc.editError && logs.FilterMessage("edit verification result failed").Len() != 1 {
				t.Fatalf("edit failure was not observed: %v", logs.All())
			}
			if tc.seconds == 0 {
				if job != nil {
					t.Fatal("zero must retain the card")
				}
				return
			}
			if job == nil || delay != time.Duration(tc.seconds)*time.Second {
				t.Fatalf("job %v delay %v", job != nil, delay)
			}
			// Replacement task has another ID; the scheduled job must retain the old card's identity.
			id = 222
			job()
			if len(tr.requests) != 2 || tr.requests[1].method != "deleteMessage" {
				t.Fatalf("requests = %+v", tr.requests)
			}
			got := tr.requests[1].body
			if fmt.Sprint(got["chat_id"]) != "-1001" || fmt.Sprint(got["message_id"]) != "111" {
				t.Fatalf("deleted wrong card: %+v", got)
			}
		})
	}
}

func TestVerificationResultMissingCardAndMathImage(t *testing.T) {
	tr := &resultTransport{}
	s, _ := newResultService(t, tr)
	chat := &tele.Chat{ID: -1001}
	var jobs int
	schedule := func(time.Duration, func()) { jobs++ }
	s.finalizeVerificationPromptWithScheduler(chat, store.PendingVerification{}, "done", 5, schedule)
	id := int64(88)
	s.finalizeVerificationPromptWithScheduler(chat, store.PendingVerification{Method: "math_image", JoinMessageID: &id}, "done", 5, schedule)
	if jobs != 0 || len(tr.requests) != 1 || tr.requests[0].method != "deleteMessage" || fmt.Sprint(tr.requests[0].body["message_id"]) != "88" {
		t.Fatalf("jobs=%d requests=%+v", jobs, tr.requests)
	}
}

func TestVerificationResultDeleteNotFoundIsTerminal(t *testing.T) {
	tr := &resultTransport{deleteError: true}
	s, logs := newResultService(t, tr)
	id := int64(111)
	var job func()
	s.finalizeVerificationPromptWithScheduler(&tele.Chat{ID: -1001}, store.PendingVerification{JoinMessageID: &id}, "done", 5, func(_ time.Duration, f func()) { job = f })
	if job == nil {
		t.Fatal("missing deletion job")
	}
	job()
	if len(tr.requests) != 2 || logs.FilterMessage("auto delete verification result failed").Len() != 0 {
		t.Fatalf("requests=%+v logs=%v", tr.requests, logs.All())
	}
}
