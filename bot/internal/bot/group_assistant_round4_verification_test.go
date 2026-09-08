package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/store"
)

type round4Requests struct {
	mu     sync.Mutex
	models []string
}

func (r *round4Requests) add(req *http.Request) string {
	var body struct {
		Model string `json:"model"`
	}
	_ = json.NewDecoder(req.Body).Decode(&body)
	r.mu.Lock()
	r.models = append(r.models, body.Model)
	r.mu.Unlock()
	return body.Model
}
func (r *round4Requests) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.models...)
}
func round4Dispatch(a *GroupAssistant, ctx context.Context, task string, cfg AssistantPoolConfig, timeout time.Duration) (string, AssistantPoolEndpoint, error) {
	if task == "chat" {
		return a.dispatchTools(ctx, -4401, cfg, store.GroupAssistantPolicy{}, "system", nil, nil)
	}
	result, ep, e := a.dispatchPlain(ctx, -4401, "learning", cfg, store.GroupAssistantPolicy{}, ai.CheckRequest{Timeout: timeout})
	text := ""
	if result != nil {
		text = result.Content
	}
	return text, ep, e
}
func round4AssertReleased(t *testing.T, a *GroupAssistant) {
	t.Helper()
	a.runtimeMu.Lock()
	for name, rt := range a.runtimes {
		rt.mu.Lock()
		if rt.active != 0 {
			t.Errorf("active lease leak %s=%d", name, rt.active)
		}
		rt.mu.Unlock()
	}
	a.runtimeMu.Unlock()
	a.queue.mu.Lock()
	defer a.queue.mu.Unlock()
	for chat, depth := range a.queue.depth {
		if depth != 0 {
			t.Errorf("queue leak chat=%d depth=%d", chat, depth)
		}
	}
	if a.queue.chatWaiters != 0 {
		t.Errorf("chatWaiters leak=%d", a.queue.chatWaiters)
	}
}
func round4Reply(w http.ResponseWriter) {
	fmt.Fprint(w, `{"choices":[{"message":{"content":"backup answer"}}]}`)
}

func TestAssistantVerificationRound4NetworkTimeoutPriorityCompatibility(t *testing.T) {
	for _, task := range []string{"chat", "learning"} {
		for _, mode := range []string{"network", "endpoint_timeout"} {
			t.Run(task+"/"+mode, func(t *testing.T) {
				r := &round4Requests{}
				a, cfg := assistantVerificationPool(t, func(w http.ResponseWriter, req *http.Request) {
					model := r.add(req)
					if model == "main" {
						if mode == "network" {
							conn, _, e := w.(http.Hijacker).Hijack()
							if e != nil {
								t.Error(e)
								return
							}
							_ = conn.Close()
							return
						}
						select {
						case <-req.Context().Done():
						case <-time.After(3 * time.Second):
						}
						return
					}
					if model == "text" {
						w.WriteHeader(503)
						return
					}
					round4Reply(w)
				})
				// Assignment order deliberately differs from priorities. Chat must skip text-only.
				cfg.TaskAssignments[task] = AssistantTaskAssignment{Primary: "main", Backups: []string{"backup", "text"}}
				cfg.Endpoints[1].Priority = 2
				cfg.Endpoints[2].Priority = 1
				start := time.Now()
				answer, ep, e := round4Dispatch(a, context.Background(), task, cfg, 10*time.Second)
				elapsed := time.Since(start)
				want := []string{"main", "backup"}
				if task == "learning" {
					want = []string{"main", "text", "backup"}
				}
				if e != nil || answer != "backup answer" || ep.ID != "backup" || !reflect.DeepEqual(r.snapshot(), want) {
					t.Fatalf("ordered compatibility answer=%q ep=%s calls=%v want=%v err=%v", answer, ep.ID, r.snapshot(), want, e)
				}
				if mode == "endpoint_timeout" && (elapsed < 900*time.Millisecond || elapsed > 2500*time.Millisecond) {
					t.Errorf("endpoint 1000ms timeout not enforced, elapsed=%s", elapsed)
				}
				rt := a.runtimeSnapshot("mock:main")
				rt.mu.Lock()
				status, cooldown := rt.status, rt.cooldownUntil
				rt.mu.Unlock()
				if status != "unhealthy" || cooldown.IsZero() {
					t.Errorf("failed endpoint health not recorded: %s %s", status, cooldown)
				}
				round4AssertReleased(t, a)
			})
		}
	}
}

func TestAssistantVerificationRound4UnrecoverableNoFallback(t *testing.T) {
	for _, task := range []string{"chat", "learning"} {
		for _, mode := range []string{"400", "401", "403", "empty_content", "whitespace_content", "no_choices", "malformed"} {
			t.Run(task+"/"+mode, func(t *testing.T) {
				r := &round4Requests{}
				a, cfg := assistantVerificationPool(t, func(w http.ResponseWriter, req *http.Request) {
					if r.add(req) != "main" {
						round4Reply(w)
						return
					}
					switch mode {
					case "400":
						w.WriteHeader(400)
					case "401":
						w.WriteHeader(401)
					case "403":
						w.WriteHeader(403)
					case "empty_content":
						fmt.Fprint(w, `{"choices":[{"message":{"content":""}}]}`)
					case "whitespace_content":
						fmt.Fprint(w, `{"choices":[{"message":{"content":"  \n\t"}}]}`)
					case "no_choices":
						fmt.Fprint(w, `{"choices":[]}`)
					case "malformed":
						fmt.Fprint(w, `{"choices":`)
					}
				})
				answer, _, e := round4Dispatch(a, context.Background(), task, cfg, time.Second)
				if e == nil || answer != "" {
					t.Errorf("unrecoverable/empty response falsely succeeded: answer=%q err=%v", answer, e)
				}
				want := []string{"main"}
				if task == "chat" && (mode == "empty_content" || mode == "whitespace_content") {
					// SGB ordinary-chat fallback is one request on the SAME lease.
					want = []string{"main", "main"}
				}
				if got := r.snapshot(); !reflect.DeepEqual(got, want) {
					t.Errorf("unrecoverable response used unexpected route: %v want %v", got, want)
				}
				if mode == "empty_content" || mode == "whitespace_content" {
					rt := a.runtimeSnapshot("mock:main")
					if rt == nil || rt.statusValue() == "healthy" {
						t.Error("empty response falsely marked healthy")
					}
				}
				round4AssertReleased(t, a)
			})
		}
	}
}

func TestAssistantVerificationRound4NoSwitchAfterToolErrors(t *testing.T) {
	for _, mode := range []string{"429", "timeout", "network"} {
		t.Run(mode, func(t *testing.T) {
			var main, backup atomic.Int32
			a, cfg := assistantVerificationPool(t, func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model string `json:"model"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body.Model != "main" {
					backup.Add(1)
					round4Reply(w)
					return
				}
				if main.Add(1) == 1 {
					fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"id":"c","type":"function","function":{"name":"shell","arguments":"{}"}}]}}]}`)
					return
				}
				switch mode {
				case "429":
					w.WriteHeader(429)
				case "network":
					conn, _, e := w.(http.Hijacker).Hijack()
					if e != nil {
						t.Error(e)
						return
					}
					conn.Close()
				case "timeout":
					select {
					case <-r.Context().Done():
					case <-time.After(3 * time.Second):
					}
				}
			})
			_, ep, e := round4Dispatch(a, context.Background(), "chat", cfg, time.Second)
			if e == nil || ep.ID != "main" || main.Load() != 2 || backup.Load() != 0 {
				t.Errorf("tool affinity lost: ep=%s main=%d backup=%d err=%v", ep.ID, main.Load(), backup.Load(), e)
			}
			round4AssertReleased(t, a)
		})
	}
}

func TestAssistantVerificationRound4CancellationReleasesState(t *testing.T) {
	for _, task := range []string{"chat", "learning"} {
		for _, phase := range []string{"primary_boundary", "fallback_call", "fallback_wait", "lifecycle"} {
			t.Run(task+"/"+phase, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				life, stop := context.WithCancel(context.Background())
				defer stop()
				r := &round4Requests{}
				started := make(chan struct{}, 1)
				a, cfg := assistantVerificationPool(t, func(w http.ResponseWriter, req *http.Request) {
					model := r.add(req)
					if model == "main" {
						if phase == "primary_boundary" {
							cancel()
						}
						w.WriteHeader(503)
						return
					}
					select {
					case started <- struct{}{}:
					default:
					}
					select {
					case <-req.Context().Done():
					case <-time.After(2 * time.Second):
					}
				})
				a.ctx = life
				cfg.TaskAssignments[task] = AssistantTaskAssignment{Primary: "main", Backups: []string{"backup"}}
				var held *assistantEndpointRuntime
				if phase == "fallback_wait" {
					held = a.endpointRuntime("mock:backup", 1)
					if !held.tryAcquire(time.Now()) {
						t.Fatal("cannot occupy backup")
					}
				}
				done := make(chan error, 1)
				go func() { _, _, e := round4Dispatch(a, ctx, task, cfg, 10*time.Second); done <- e }()
				if phase == "fallback_wait" {
					deadline := time.Now().Add(time.Second)
					for {
						a.queue.mu.Lock()
						n := a.queue.depth[-4401]
						a.queue.mu.Unlock()
						if n > 0 {
							break
						}
						if time.Now().After(deadline) {
							cancel()
							if held != nil {
								held.release(true, nil, 0, time.Second)
							}
							t.Fatal("fallback never queued")
						}
						time.Sleep(time.Millisecond)
					}
					cancel()
				} else if phase != "primary_boundary" {
					select {
					case <-started:
					case <-time.After(time.Second):
						cancel()
						t.Fatal("fallback call not started")
					}
					if phase == "lifecycle" {
						stop()
					} else {
						cancel()
					}
				}
				select {
				case e := <-done:
					if !errors.Is(e, context.Canceled) {
						t.Errorf("cancel returned wrong error %v", e)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("cancellation did not terminate")
				}
				if held != nil {
					held.release(true, nil, 0, time.Second)
				}
				round4AssertReleased(t, a)
				calls := r.snapshot()
				want := 1
				if phase == "fallback_call" || phase == "lifecycle" {
					want = 2
				}
				if len(calls) != want {
					t.Errorf("calls after cancellation %v", calls)
				}
			})
		}
	}
}

func TestAssistantVerificationRound4DedupAndAllFailedBound(t *testing.T) {
	for _, task := range []string{"chat", "learning"} {
		for _, mode := range []string{"duplicate_refs", "sixteen_candidates"} {
			t.Run(task+"/"+mode, func(t *testing.T) {
				r := &round4Requests{}
				a, cfg := assistantVerificationPool(t, func(w http.ResponseWriter, req *http.Request) { r.add(req); w.WriteHeader(503) })
				cfg.Endpoints = nil
				ids := []string{}
				registry := a.models.(*assistantVerificationRegistry)
				want := 16
				if mode == "duplicate_refs" {
					want = 3
				}
				for i := 0; i < 16; i++ {
					model := fmt.Sprintf("m%d", i)
					if mode == "duplicate_refs" {
						model = fmt.Sprintf("m%d", i%3)
					}
					ref := ai.NewModelRef("mock", model)
					registry.models[ref] = ai.Model{Enabled: true, SupportsTools: true, ProviderKey: "mock", ModelKey: model}
					id := fmt.Sprintf("ep%d", i)
					ids = append(ids, id)
					cfg.Endpoints = append(cfg.Endpoints, AssistantPoolEndpoint{ID: id, ModelRef: ref.String(), Role: "backup", Priority: i, MaxConcurrency: 1, TimeoutMs: 1000, CooldownSeconds: 1})
				}
				cfg.Endpoints[0].Role = "primary"
				for _, name := range []string{"chat", "learning"} {
					cfg.TaskAssignments[name] = AssistantTaskAssignment{Primary: ids[0], Backups: ids[1:]}
				}
				if e := a.ValidatePoolConfig(cfg, true); e != nil {
					t.Fatal("invalid bounded pool fixture", e)
				}
				start := time.Now()
				answer, _, e := round4Dispatch(a, context.Background(), task, cfg, time.Second)
				calls := r.snapshot()
				if e == nil || answer != "" || len(calls) != want || time.Since(start) > 3*time.Second {
					t.Errorf("failure not bounded: calls=%v answer=%q err=%v elapsed=%s", calls, answer, e, time.Since(start))
				}
				seen := map[string]bool{}
				for _, model := range calls {
					if seen[model] {
						t.Errorf("provider:model retried: mock:%s", model)
					}
					seen[model] = true
				}
				round4AssertReleased(t, a)
			})
		}
	}
}

func TestAssistantVerificationRound4SharedThirtySecondBudget(t *testing.T) {
	for _, task := range []string{"chat", "learning"} {
		t.Run(task, func(t *testing.T) {
			r := &round4Requests{}
			a, cfg := assistantVerificationPool(t, func(w http.ResponseWriter, req *http.Request) {
				switch r.add(req) {
				case "main":
					w.WriteHeader(503)
				case "backup":
					select {
					case <-req.Context().Done():
						return
					case <-time.After(17 * time.Second):
						w.WriteHeader(503)
					}
				case "text":
					select {
					case <-req.Context().Done():
						return
					case <-time.After(20 * time.Second):
						w.WriteHeader(503)
					}
				default:
					round4Reply(w)
				}
			})
			registry := a.models.(*assistantVerificationRegistry)
			text := registry.models[ai.NewModelRef("mock", "text")]
			text.SupportsTools = true
			registry.models[ai.NewModelRef("mock", "text")] = text
			registry.models[ai.NewModelRef("mock", "last")] = ai.Model{Enabled: true, SupportsTools: true, ProviderKey: "mock", ModelKey: "last"}
			cfg.Endpoints = append(cfg.Endpoints, AssistantPoolEndpoint{ID: "last", ModelRef: "mock:last", Role: "backup", Priority: 3, MaxConcurrency: 1, TimeoutMs: 60000, CooldownSeconds: 1})
			for i := range cfg.Endpoints {
				cfg.Endpoints[i].TimeoutMs = 60000
			}
			cfg.TaskAssignments[task] = AssistantTaskAssignment{Primary: "main", Backups: []string{"backup", "text", "last"}}
			cfg.MaxQueueWaitSec = 60
			start := time.Now()
			answer, _, e := round4Dispatch(a, context.Background(), task, cfg, 60*time.Second)
			elapsed := time.Since(start)
			// Two fallback calls share one wall-clock budget: 17 seconds + ~13 seconds,
			// not 30 seconds each. No patched clock or string-only budget assertion.
			if !errors.Is(e, context.DeadlineExceeded) || answer != "" || elapsed < 29*time.Second || elapsed > 33*time.Second || !reflect.DeepEqual(r.snapshot(), []string{"main", "backup", "text"}) {
				t.Errorf("shared budget failure: elapsed=%s calls=%v answer=%q err=%v", elapsed, r.snapshot(), answer, e)
			}
			t.Logf("actual shared fallback elapsed=%s calls=%v", elapsed, r.snapshot())
			round4AssertReleased(t, a)
		})
	}
}

func TestAssistantVerificationRound4PlainRequestTimeoutTightensEndpoint(t *testing.T) {
	r := &round4Requests{}
	a, cfg := assistantVerificationPool(t, func(w http.ResponseWriter, req *http.Request) {
		if r.add(req) == "main" {
			select {
			case <-req.Context().Done():
			case <-time.After(2 * time.Second):
			}
			return
		}
		round4Reply(w)
	})
	start := time.Now()
	answer, _, e := round4Dispatch(a, context.Background(), "learning", cfg, 80*time.Millisecond)
	if e != nil || answer != "backup answer" || time.Since(start) > 500*time.Millisecond {
		t.Errorf("request timeout failed to tighten 1000ms endpoint: elapsed=%s answer=%q err=%v", time.Since(start), answer, e)
	}
	round4AssertReleased(t, a)
}

func TestAssistantVerificationRound5NonemptyPlainContent(t *testing.T) {
	requests := &round4Requests{}
	a, cfg := assistantVerificationPool(t, func(w http.ResponseWriter, r *http.Request) {
		requests.add(r)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"  nonempty answer \n"}}]}`)
	})
	answer, ep, e := round4Dispatch(a, context.Background(), "learning", cfg, time.Second)
	if e != nil || strings.TrimSpace(answer) != "nonempty answer" || ep.ID != "main" || !reflect.DeepEqual(requests.snapshot(), []string{"main"}) {
		t.Fatalf("nonempty control changed: answer=%q ep=%s calls=%v err=%v", answer, ep.ID, requests.snapshot(), e)
	}
	rt := a.runtimeSnapshot("mock:main")
	if rt == nil || rt.statusValue() != "healthy" {
		t.Error("nonempty success was not healthy")
	}
	round4AssertReleased(t, a)
}
