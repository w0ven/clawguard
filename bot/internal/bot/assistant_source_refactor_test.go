package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
)

// All assertions count actual requests decoded by a local HTTP provider, not
// scheduler return values or saved JSON. No Telegram/model service is contacted.
func sourceRefactorPool(t *testing.T, h http.HandlerFunc) (*GroupAssistant, AssistantPoolConfig) {
	t.Helper()
	a, cfg := assistantVerificationPool(t, h)
	r := a.models.(*assistantVerificationRegistry)
	for ref, m := range r.models {
		m.SupportsTools = true
		m.SupportsVision = true
		m.CapabilityTags = []string{"embedding"}
		r.models[ref] = m
	}
	cfg.Strategy = "weighted"
	cfg.MaxQueueDepth = 100
	cfg.MaxQueueWaitSec = 5
	for i := range cfg.Endpoints {
		cfg.Endpoints[i].Weight = []int{5, 3, 2}[i]
		cfg.Endpoints[i].MaxConcurrency = 2
	}
	for _, task := range []string{"chat", "learning", "decision", "vision", "compress", "vector"} {
		cfg.TaskAssignments[task] = AssistantTaskAssignment{Primary: "main", Backups: []string{"backup", "text"}}
	}
	return a, cfg
}
func TestSourceRefactorRealWeightedRoles(t *testing.T) {
	for _, task := range []string{"chat", "learning", "decision", "vision", "compress", "vector"} {
		t.Run(task, func(t *testing.T) {
			counts := map[string]int{}
			var mu sync.Mutex
			a, cfg := sourceRefactorPool(t, func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				mu.Lock()
				counts[body.Model]++
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/embeddings" {
					fmt.Fprint(w, `{"data":[{"embedding":[1,0,0]}]}`)
				} else {
					fmt.Fprint(w, `{"choices":[{"message":{"content":"完成"}}]}`)
				}
			})
			for i := 0; i < 100; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				var err error
				switch task {
				case "chat":
					_, _, err = a.dispatchTools(ctx, -1, cfg, store.GroupAssistantPolicy{}, "system", nil, nil)
				case "vector":
					_, _, err = a.dispatchEmbedding(ctx, -1, cfg, store.GroupAssistantPolicy{}, "周六活动")
				default:
					_, _, err = a.dispatchPlain(ctx, -1, task, cfg, store.GroupAssistantPolicy{}, ai.CheckRequest{Messages: []ai.Message{{Role: "user", Content: "test"}}})
				}
				cancel()
				if err != nil {
					t.Fatal(err)
				}
			}
			want := map[string]int{"main": 50, "backup": 30, "text": 20}
			if !reflect.DeepEqual(counts, want) {
				t.Fatalf("actual upstream counts=%v want=%v", counts, want)
			}
			round4AssertReleased(t, a)
			t.Logf("actual HTTP %s: %v", task, counts)
		})
	}
}
func TestSourceRefactorConcurrentToolAffinity(t *testing.T) {
	var mu sync.Mutex
	active := map[string]int{}
	peak := map[string]int{}
	turns := map[string]string{}
	counts := map[string]int{}
	a, cfg := sourceRefactorPool(t, func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Model    string       `json:"model"`
			Messages []ai.Message `json:"messages"`
		}
		if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
			t.Error(e)
		}
		key := fmt.Sprint(b.Messages[1].Content)
		mu.Lock()
		if prev := turns[key]; prev != "" && prev != b.Model {
			t.Errorf("tool turn %s changed %s→%s", key, prev, b.Model)
		}
		turns[key] = b.Model
		counts[b.Model]++
		active[b.Model]++
		if active[b.Model] > peak[b.Model] {
			peak[b.Model] = active[b.Model]
		}
		mu.Unlock()
		defer func() { mu.Lock(); active[b.Model]--; mu.Unlock() }()
		time.Sleep(5 * time.Millisecond)
		if len(b.Messages) == 2 {
			fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"id":"one","type":"function","function":{"name":"not_authorized","arguments":"{}"}}]}}]}`)
		} else {
			if b.Messages[len(b.Messages)-1].Role != "tool" {
				t.Error("tool error feedback absent")
			}
			fmt.Fprint(w, `{"choices":[{"message":{"content":"如实说明技能不可用"}}]}`)
		}
	})
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			_, _, e := a.dispatchTools(ctx, int64(-1-i%4), cfg, store.GroupAssistantPolicy{}, "system", []ai.Message{{Role: "user", Content: fmt.Sprint(i)}}, nil)
			if e != nil {
				t.Error(e)
			}
		}(i)
	}
	wg.Wait()
	for model, n := range peak {
		if n > 2 {
			t.Fatalf("shared capacity exceeded: %s=%d", model, n)
		}
	}
	if len(turns) != 60 {
		t.Fatal("missing turns", len(turns))
	}
	round4AssertReleased(t, a)
	t.Logf("60 concurrent turns across 4 groups, actual calls=%v peak=%v, all tool turns pinned", counts, peak)
}
func TestSourceRefactorRoleFallbackAndCapabilities(t *testing.T) {
	for _, task := range []string{"learning", "decision", "vision", "compress", "vector"} {
		t.Run(task, func(t *testing.T) {
			got := []string{}
			a, cfg := sourceRefactorPool(t, func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model string `json:"model"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				got = append(got, body.Model)
				if body.Model == "main" {
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(429)
					return
				}
				if task == "vector" {
					fmt.Fprint(w, `{"data":[{"embedding":[1,0]}]}`)
				} else {
					fmt.Fprint(w, `{"choices":[{"message":{"content":"备用"}}]}`)
				}
			})
			cfg.Strategy = "primary-overflow"
			var err error
			if task == "vector" {
				_, _, err = a.dispatchEmbedding(context.Background(), -1, cfg, store.GroupAssistantPolicy{}, "活动")
			} else {
				_, _, err = a.dispatchPlain(context.Background(), -1, task, cfg, store.GroupAssistantPolicy{}, ai.CheckRequest{})
			}
			if err != nil || !reflect.DeepEqual(got, []string{"main", "backup"}) {
				t.Fatalf("%s actual=%v err=%v", task, got, err)
			}
			round4AssertReleased(t, a)
		})
	}
	a, cfg := sourceRefactorPool(t, func(w http.ResponseWriter, r *http.Request) { t.Error("incompatible embedding must not send") })
	reg := a.models.(*assistantVerificationRegistry)
	for ref, m := range reg.models {
		m.CapabilityTags = nil
		reg.models[ref] = m
	}
	if _, _, err := a.dispatchEmbedding(context.Background(), -1, cfg, store.GroupAssistantPolicy{}, "中文"); err == nil {
		t.Fatal("undeclared embedding accepted")
	}
}
func TestSourceRefactorSGBBoundaries(t *testing.T) {
	now := time.Unix(100, 0)
	msg := &tele.Message{ID: 20, Chat: &tele.Chat{ID: -1}, ThreadID: 7}
	first := assistantNextFlushAt(assistantReplyBatchItem{msg: msg, text: "普通陈述"}, 1, 5*time.Second, now, time.Time{})
	second := assistantNextFlushAt(assistantReplyBatchItem{msg: msg, text: "继续"}, 2, 5*time.Second, now.Add(300*time.Millisecond), first)
	third := assistantNextFlushAt(assistantReplyBatchItem{msg: msg, text: "最后"}, 3, 5*time.Second, now.Add(400*time.Millisecond), second)
	if first.Sub(now) != 1800*time.Millisecond || second.After(first) || third.After(second) {
		t.Fatalf("adaptive flush %s %s %s", first, second, third)
	}
	if got := assistantNextFlushAt(assistantReplyBatchItem{direct: true}, 1, 5*time.Second, now, time.Time{}); got.Sub(now) != 500*time.Millisecond {
		t.Fatal("mention delay", got)
	}
	if got := assistantNextFlushAt(assistantReplyBatchItem{}, 1, 400*time.Millisecond, now, time.Time{}); got.Sub(now) != 400*time.Millisecond {
		t.Fatal("legacy delay overwritten")
	}
	for _, tc := range []struct {
		raw   string
		count int
	}{
		{"一段\n\n另一段", 1}, {"一\n[[SPLIT]]\n二", 2}, {"```\n[[SPLIT]]\n```", 1}, {`{"schema":"smart-group-bot.reply.v2","should_reply":false}`, 0}, {`{"should_reply":false}`, 1}, {"```json\n{\"schema\":\"smart-group-bot.reply.v2\",\"should_reply\":false}\n```", 1},
	} {
		if got := parseAssistantReplyOutput(tc.raw); len(got) != tc.count {
			t.Fatalf("parse %q=%+v", tc.raw, got)
		}
	}
	history := []store.GroupAssistantMessage{{ChatID: -1, ThreadID: 7, TelegramMessageID: 10}, {ChatID: -2, ThreadID: 7, TelegramMessageID: 11}, {ChatID: -1, ThreadID: 8, TelegramMessageID: 12}}
	for _, key := range []string{"message_11", "message_12", "invented"} {
		if assistantReplyTarget(assistantReplySpec{DeliveryMode: "reply", ReplyTo: key}, msg, history) != nil {
			t.Fatal("fabricated/cross-scope target", key)
		}
	}
	if target := assistantReplyTarget(assistantReplySpec{DeliveryMode: "reply", ReplyTo: "message_10"}, msg, history); target == nil || target.ID != 10 {
		t.Fatal("valid target lost")
	}
	rt := &assistantToolRuntime{}
	ctx := withAssistantRuntime(context.Background(), rt)
	if e := assistantReserveMedia(ctx, "voice:once"); e != nil {
		t.Fatal(e)
	}
	if e := assistantReserveMedia(ctx, "voice:once"); e == nil {
		t.Fatal("ambiguous media replay permitted")
	}
	if _, e := assistantRecallKeyIDs(-1, []string{"-2:20"}); e == nil {
		t.Fatal("cross group key permitted")
	}
}
