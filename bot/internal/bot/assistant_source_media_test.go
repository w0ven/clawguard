package bot

import (
	"context"
	"fmt"
	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
	"net/http"
	"testing"
)

type sourceFailTTSSynth struct{}

func (sourceFailTTSSynth) Available() bool { return true }
func (sourceFailTTSSynth) Synthesize(context.Context, string) assistantTTSResult {
	return assistantTTSResult{Error: "local synthetic failure"}
}

type sourceAmbiguousSender struct{ calls int }

func (s *sourceAmbiguousSender) Send(tele.Recipient, interface{}, ...interface{}) (*tele.Message, error) {
	s.calls++
	return nil, fmt.Errorf("local transport lost confirmation")
}
func (s *sourceAmbiguousSender) Delete(tele.Editable) error { return nil }
func TestSourceRefactorAmbiguousMediaStopsTurn(t *testing.T) {
	modelCalls := 0
	a, cfg := sourceRefactorPool(t, func(w http.ResponseWriter, r *http.Request) {
		modelCalls++
		fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"id":"v1","type":"function","function":{"name":"doubao_tts","arguments":"{\"text\":\"你好\"}"}},{"id":"v2","type":"function","function":{"name":"doubao_tts","arguments":"{\"text\":\"再说一次\"}"}}]}}]}`)
	})
	sender := &sourceAmbiguousSender{}
	a.service.sender = sender
	a.service.assistant = a
	a.ttsSynth = mockTTSSynth{available: true}
	rt := &assistantToolRuntime{msg: &tele.Message{ID: 1, Chat: &tele.Chat{ID: -1}}, mode: assistantReplyDirect}
	ctx := withAssistantRuntime(context.Background(), rt)
	_, _, err := a.dispatchTools(ctx, -1, cfg, store.GroupAssistantPolicy{TTSMode: "always"}, "system", nil, nil)
	if err == nil || !rt.deliveryUncertain || rt.voiceSent || sender.calls != 1 || modelCalls != 1 {
		t.Fatalf("ambiguous side effect retried or claimed success err=%v rt=%+v sends=%d models=%d", err, rt, sender.calls, modelCalls)
	}
	round4AssertReleased(t, a)
	if err = a.service.deliverAssistantReply(ctx, rt.msg, store.GroupAssistantPolicy{TTSMode: "always"}, assistantReplyDirect, store.AssistantGlobalSettings{}, cfg, nil, "重复正文"); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 1 {
		t.Fatal("final phase replayed ambiguous media")
	}
}
