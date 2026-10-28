package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

type routingClient struct {
	mu      sync.Mutex
	calls   int
	prompt  string
	fail    bool
	verdict string
}

func (c *routingClient) Check(_ context.Context, req ai.CheckRequest) (*ai.CheckResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.prompt = req.SystemPrompt
	if c.fail {
		return nil, errors.New("synthetic failure")
	}
	return &ai.CheckResult{Verdicts: []ai.Verdict{{Verdict: c.verdict, Confidence: 0.95, Category: "正常"}}}, nil
}
func (*routingClient) Probe(context.Context, string) (int, error) { return 0, nil }
func (*routingClient) Chat(context.Context, ai.CheckRequest) (*ai.ChatRawResult, error) {
	return nil, nil
}

func TestAdKillerTextModelRouting(t *testing.T) {
	for _, tc := range []struct {
		name                                  string
		firstFail, secondFail                 bool
		second                                bool
		verdict                               string
		score                                 int
		status                                int
		wantCalls                             int
		wantModel                             string
		paused, frozen                        bool
		disabled, otherGroup, edited, noModel bool
	}{
		{name: "normal model", verdict: "normal", score: 95, wantModel: "p1:m1"},
		{name: "ad model", verdict: "ad", score: 95, wantModel: "p1:m1"},
		{name: "backup succeeds", firstFail: true, second: true, verdict: "normal", score: 95, wantModel: "p2:m2"},
		{name: "all fail high", firstFail: true, secondFail: true, second: true, score: 95, wantCalls: 1, wantModel: "adkiller-text-fallback"},
		{name: "all fail low", firstFail: true, score: 10, wantCalls: 1, wantModel: "adkiller-text-fallback"},
		{name: "no models available", noModel: true, score: 10, wantCalls: 1, wantModel: "adkiller-text-fallback"},
		{name: "upstream failure", firstFail: true, status: 503, wantCalls: 1, wantModel: ""},
		{name: "paused", firstFail: true, score: 95, paused: true, wantCalls: 0, wantModel: ""},
		{name: "frozen", firstFail: true, score: 95, frozen: true, wantCalls: 0, wantModel: ""},
		{name: "excluded group", firstFail: true, score: 95, otherGroup: true},
		{name: "ai disabled", firstFail: true, score: 95, disabled: true},
		{name: "edited message", firstFail: true, score: 95, edited: true, wantCalls: 1, wantModel: "adkiller-text-fallback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, calls := newAdKillerTestService(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.status != 0 {
					w.WriteHeader(tc.status)
					return
				}
				level := "normal"
				category := "normal_chat"
				if tc.score > 80 {
					level = "ad"
					category = "goods_sale"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"score": tc.score, "level": level, "primary_category": category, "dimensions": validAdKillerDimensions()})
			})
			botClient, _ := newMockTelegramBot(t, "")
			db := newModerationProfileMatchMockDB(-100123, 42)
			db.systemState.AIPaused = tc.paused
			db.systemState.Frozen = tc.frozen
			svc.queries = store.New(db)
			svc.bot = botClient
			svc.sender = botClient
			svc.sendLimiter = NewSendLimiter()
			svc.logger = zap.NewNop()
			r1 := ai.NewModelRef("p1", "m1")
			r2 := ai.NewModelRef("p2", "m2")
			models := aiErrorFallbackModelRegistry{items: map[ai.ModelRef]ai.Model{r1: {ID: 1, ProviderID: 1, ProviderKey: "p1", ModelKey: "m1", Enabled: true, CapabilityTags: []string{"moderation"}}, r2: {ID: 2, ProviderID: 2, ProviderKey: "p2", ModelKey: "m2", Enabled: true, CapabilityTags: []string{"moderation"}}}}
			if tc.noModel {
				models.items = map[ai.ModelRef]ai.Model{}
			}
			c1 := &routingClient{fail: tc.firstFail, verdict: tc.verdict}
			c2 := &routingClient{fail: tc.secondFail, verdict: tc.verdict}
			svc.aiModerator = ai.NewModerator(context.Background(), zap.NewNop(), nil, svc.queries, aiErrorFallbackProviderRegistry{clients: map[string]ai.LLMClient{"p1": c1, "p2": c2}}, models, ai.NewResolver(models), nil)
			policy := config.DefaultPolicy
			policy.AI.Enabled = !tc.disabled
			policy.AI.PrimaryModelRef = r1.String()
			policy.AI.FallbackModelRefs = nil
			policy.AI.FallbackChain = nil
			if tc.second {
				policy.AI.FallbackModelRefs = []string{r2.String()}
			}
			policy.AI.BatchWindowMs = 1
			policy.AI.MaxRetries = 0
			policy.AI.CheckProfileOnMessage = false
			policy.AI.PerUserDailyLimit = 0
			policy.AI.MessageRules = "CUSTOM TEST RULE"
			policy.AI.AdKiller.Enabled = true
			policy.AI.AdKiller.EnabledChatIDs = []int64{-100123}
			if tc.otherGroup {
				policy.AI.AdKiller.EnabledChatIDs = []int64{-999}
			}
			msg := &tele.Message{ID: 10, Text: "synthetic test", Chat: &tele.Chat{ID: -100123, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 42, FirstName: "Test"}}
			if err := svc.applyAIModeration(context.Background(), msg, policy, false, reviewableContent{Text: msg.Text, Kind: "text"}, tc.edited); err != nil {
				t.Fatal(err)
			}
			if got := int(atomic.LoadInt32(calls)); got != tc.wantCalls {
				t.Fatalf("adkiller calls %d want %d", got, tc.wantCalls)
			}
			if !tc.paused && !tc.disabled && !tc.noModel && !strings.Contains(c1.prompt, "CUSTOM TEST RULE") {
				t.Errorf("missing custom prompt")
			}
			if tc.wantModel != "" {
				db.mu.Lock()
				defer db.mu.Unlock()
				if len(db.aiDecisions) != 1 || db.aiDecisions[0].Model != tc.wantModel {
					t.Fatalf("decisions: %+v", db.aiDecisions)
				}
			}
		})
	}
}

type imageTestTransport struct{ data []byte }

func (t imageTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body := t.data
	if strings.Contains(r.URL.Path, "getFile") {
		body = []byte(`{"ok":true,"result":{"file_path":"synthetic.png"}}`)
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
}

func TestAdKillerImageRouting(t *testing.T) {
	for _, tc := range []struct {
		name, caption, ocr, media   string
		score, status               int
		modelFail, invalid, timeout bool
		wantImage, wantModel        int
		wantDecision                string
	}{
		{name: "caption hit", caption: "synthetic caption", ocr: "synthetic ocr", score: 95, wantImage: 1, wantDecision: "adkiller-image-ocr"},
		{name: "pure image hit", ocr: "synthetic ocr", score: 95, wantImage: 1, wantDecision: "adkiller-image-ocr"},
		{name: "static sticker", media: "sticker", ocr: "synthetic ocr", score: 95, wantImage: 1, wantDecision: "adkiller-image-ocr"},
		{name: "animated sticker thumbnail", media: "sticker-thumbnail", ocr: "synthetic ocr", score: 95, wantImage: 1, wantDecision: "adkiller-image-ocr"},
		{name: "gif existing path", media: "gif", wantModel: 1, wantDecision: "p1:m1"},
		{name: "video existing path", media: "video", wantModel: 1, wantDecision: "p1:m1"},
		{name: "empty ocr", score: 0, wantImage: 1, wantModel: 1, wantDecision: "p1:m1"},
		{name: "empty ocr cannot punish", score: 95, wantImage: 1, wantModel: 1, wantDecision: "p1:m1"},
		{name: "timeout", timeout: true, wantImage: 1, wantModel: 1, wantDecision: "p1:m1"},
		{name: "low score", caption: "caption", score: 10, wantImage: 1, wantModel: 1, wantDecision: "p1:m1"},
		{name: "429", status: 429, wantImage: 1, wantModel: 1, wantDecision: "p1:m1"},
		{name: "415", status: 415, wantImage: 1, wantModel: 1, wantDecision: "p1:m1"},
		{name: "model failure no duplicate", score: 10, modelFail: true, wantImage: 1, wantModel: 1},
		{name: "bad image no upload", invalid: true, wantModel: 1, wantDecision: "p1:m1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			image := []byte("\x89PNG\r\n\x1a\nsynthetic image")
			if tc.invalid {
				image = []byte("not an image")
			}
			old := imageDownloadHTTPClient
			imageDownloadHTTPClient = &http.Client{Transport: imageTestTransport{data: image}}
			defer func() { imageDownloadHTTPClient = old }()
			svc, calls := newAdKillerTestService(t, func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseMultipartForm(6 << 20); err != nil {
					t.Error(err)
					return
				}
				f, _, err := r.FormFile("image")
				if err != nil {
					t.Error(err)
					return
				}
				defer f.Close()
				got, _ := io.ReadAll(f)
				if !bytes.Equal(got, image) {
					t.Error("image bytes changed")
				}
				if r.FormValue("caption") != tc.caption {
					t.Error("caption changed")
				}
				if tc.timeout {
					<-r.Context().Done()
					return
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
					return
				}
				level := "normal"
				category := "normal_chat"
				if tc.score > 80 {
					level = "ad"
					category = "goods_sale"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"score": tc.score, "level": level, "primary_category": category, "dimensions": validAdKillerDimensions(), "ocr_text": tc.ocr})
			})
			botClient, transport := newMockTelegramBot(t, "")
			db := newModerationProfileMatchMockDB(-100123, 42)
			svc.queries = store.New(db)
			svc.bot = botClient
			svc.sender = botClient
			svc.sendLimiter = NewSendLimiter()
			r := ai.NewModelRef("p1", "m1")
			models := aiErrorFallbackModelRegistry{items: map[ai.ModelRef]ai.Model{r: {ID: 1, ProviderID: 1, ProviderKey: "p1", ModelKey: "m1", Enabled: true, SupportsVision: true, CapabilityTags: []string{"moderation", "vision"}}}}
			c := &routingClient{fail: tc.modelFail, verdict: "normal"}
			svc.aiModerator = ai.NewModerator(context.Background(), zap.NewNop(), nil, svc.queries, aiErrorFallbackProviderRegistry{clients: map[string]ai.LLMClient{"p1": c}}, models, ai.NewResolver(models), nil)
			policy := config.DefaultPolicy
			policy.AI.Enabled = true
			policy.AI.ImageModerationEnabled = true
			policy.AI.PrimaryModelRef = r.String()
			policy.AI.FallbackModelRefs = nil
			policy.AI.FallbackChain = nil
			policy.AI.BatchWindowMs = 1
			policy.AI.MaxRetries = 0
			policy.AI.CheckProfileOnMessage = false
			policy.AI.PerUserDailyLimit = 0
			policy.AI.MessageRules = "IMAGE CUSTOM RULE"
			policy.AI.AdKiller.Enabled = true
			policy.AI.AdKiller.EnabledChatIDs = []int64{-100123}
			policy.AI.AdKiller.OnFailure = "skip"
			msg := &tele.Message{ID: 10, Caption: tc.caption, Photo: &tele.Photo{File: tele.File{FileID: "synthetic"}}, Chat: &tele.Chat{ID: -100123, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 42, FirstName: "Test"}}
			switch tc.media {
			case "sticker":
				msg.Photo = nil
				msg.Sticker = &tele.Sticker{File: tele.File{FileID: "synthetic"}}
			case "sticker-thumbnail":
				msg.Photo = nil
				msg.Sticker = &tele.Sticker{Animated: true, Thumbnail: &tele.Photo{File: tele.File{FileID: "synthetic"}}}
			case "gif":
				msg.Photo = nil
				msg.Animation = &tele.Animation{}
			case "video":
				msg.Photo = nil
				msg.Video = &tele.Video{}
			}
			if err := svc.applyAIModeration(context.Background(), msg, policy, false, reviewableContent{Text: "[图片] " + tc.caption, Kind: "photo", HasImage: true}, false); err != nil {
				t.Fatal(err)
			}
			if got := int(atomic.LoadInt32(calls)); got != tc.wantImage {
				t.Fatalf("adkiller calls %d want %d", got, tc.wantImage)
			}
			if c.calls != tc.wantModel {
				t.Fatalf("model calls %d want %d", c.calls, tc.wantModel)
			}
			if tc.wantModel > 0 && !strings.Contains(c.prompt, "IMAGE CUSTOM RULE") {
				t.Error("missing image prompt")
			}
			if tc.wantDecision != "" {
				db.mu.Lock()
				defer db.mu.Unlock()
				if len(db.aiDecisions) != 1 || db.aiDecisions[0].Model != tc.wantDecision {
					t.Fatalf("decisions %+v", db.aiDecisions)
				}
			}
			if tc.wantDecision == "adkiller-image-ocr" && countString(transport.Methods(), "kickChatMember") != 1 {
				t.Fatalf("expected one punishment: %v", transport.Methods())
			}
		})
	}
}

func TestAdKillerBioRoutingBothEntrances(t *testing.T) {
	for _, entrance := range []string{"join", "on_message"} {
		for _, tc := range []struct {
			name                            string
			fail, keyword, paused, disabled bool
			score                           int
			wantCalls                       int
			wantHit                         bool
		}{
			{name: "model pass", score: 95},
			{name: "model failure high", fail: true, score: 95, wantCalls: 1, wantHit: true},
			{name: "model failure low", fail: true, score: 10, wantCalls: 1},
			{name: "keyword mode", keyword: true, score: 95},
			{name: "paused", fail: true, paused: true, score: 95},
			{name: "AI disabled", fail: true, disabled: true, score: 95},
		} {
			t.Run(entrance+"/"+tc.name, func(t *testing.T) {
				svc, calls := newAdKillerTestService(t, func(w http.ResponseWriter, r *http.Request) {
					level := "normal"
					cat := "normal_chat"
					if tc.score > 80 {
						level = "ad"
						cat = "goods_sale"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"score": tc.score, "level": level, "primary_category": cat, "dimensions": validAdKillerDimensions()})
				})
				botClient, _ := newMockTelegramBot(t, "synthetic biography")
				db := newModerationProfileMatchMockDB(-100123, 42)
				db.systemState.AIPaused = tc.paused
				svc.queries = store.New(db)
				svc.bot = botClient
				r := ai.NewModelRef("p1", "m1")
				models := aiErrorFallbackModelRegistry{items: map[ai.ModelRef]ai.Model{r: {ID: 1, ProviderID: 1, ProviderKey: "p1", ModelKey: "m1", Enabled: true, CapabilityTags: []string{"moderation"}}}}
				c := &routingClient{fail: tc.fail, verdict: "normal"}
				svc.aiModerator = ai.NewModerator(context.Background(), zap.NewNop(), nil, svc.queries, aiErrorFallbackProviderRegistry{clients: map[string]ai.LLMClient{"p1": c}}, models, ai.NewResolver(models), nil)
				policy := config.DefaultPolicy
				policy.Verify.CheckProfile = true
				policy.AI.Enabled = !tc.disabled
				policy.Verify.ProfileCheckMode = "ai"
				policy.AI.ProfileOnMessageMode = "ai"
				if tc.keyword {
					policy.Verify.ProfileCheckMode = "keyword"
					policy.AI.ProfileOnMessageMode = "keyword"
				}
				policy.AI.PrimaryModelRef = r.String()
				policy.AI.FallbackModelRefs = nil
				policy.AI.FallbackChain = nil
				policy.AI.BatchWindowMs = 1
				policy.AI.MaxRetries = 0
				policy.AI.PerUserDailyLimit = 0
				policy.AI.BioRules = "CUSTOM BIO RULE"
				policy.AI.AdKiller.Enabled = true
				policy.AI.AdKiller.EnabledChatIDs = []int64{-100123}
				chat := &tele.Chat{ID: -100123, Type: tele.ChatSuperGroup}
				user := &tele.User{ID: 42, FirstName: "Test"}
				var matched string
				var err error
				if entrance == "join" {
					matched, _, err = svc.checkProfile(context.Background(), chat, user, policy)
				} else {
					matched, _, err = svc.checkProfileOnMessage(context.Background(), chat, user, policy)
				}
				if !tc.paused && !tc.disabled && err != nil {
					t.Fatal(err)
				}
				if (tc.paused || tc.disabled) && err == nil {
					t.Fatal("skipped bio must remain an error")
				}
				if (matched != "") != tc.wantHit {
					t.Fatalf("matched %q", matched)
				}
				if got := int(atomic.LoadInt32(calls)); got != tc.wantCalls {
					t.Fatalf("calls %d want %d", got, tc.wantCalls)
				}
				if !tc.keyword && !tc.paused && !strings.Contains(c.prompt, "CUSTOM BIO RULE") {
					t.Fatal("missing bio rule")
				}
			})
		}
	}
}

type waitRoutingClient struct{}

func (waitRoutingClient) Check(ctx context.Context, _ ai.CheckRequest) (*ai.CheckResult, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (waitRoutingClient) Probe(context.Context, string) (int, error) { return 0, nil }
func (waitRoutingClient) Chat(context.Context, ai.CheckRequest) (*ai.ChatRawResult, error) {
	return nil, nil
}

func TestAdKillerModelDeadlineAndCallerCancellation(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel_parent_%v", cancelParent), func(t *testing.T) {
			svc, calls := newAdKillerTestService(t, func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"score": 10, "level": "normal", "primary_category": "normal_chat", "dimensions": validAdKillerDimensions()})
			})
			b, _ := newMockTelegramBot(t, "")
			db := newModerationProfileMatchMockDB(-100123, 42)
			svc.queries = store.New(db)
			svc.bot = b
			svc.sender = b
			svc.sendLimiter = NewSendLimiter()
			r := ai.NewModelRef("p1", "m1")
			models := aiErrorFallbackModelRegistry{items: map[ai.ModelRef]ai.Model{r: {ID: 1, ProviderID: 1, ProviderKey: "p1", ModelKey: "m1", Enabled: true, CapabilityTags: []string{"moderation"}}}}
			svc.aiModerator = ai.NewModerator(context.Background(), zap.NewNop(), nil, svc.queries, aiErrorFallbackProviderRegistry{clients: map[string]ai.LLMClient{"p1": waitRoutingClient{}}}, models, ai.NewResolver(models), nil)
			p := config.DefaultPolicy
			p.AI.Enabled = true
			p.AI.PrimaryModelRef = r.String()
			p.AI.FallbackModelRefs = nil
			p.AI.FallbackChain = nil
			p.AI.BatchWindowMs = 1
			p.AI.MaxRetries = 0
			p.AI.TimeoutMs = 1000
			p.AI.CheckProfileOnMessage = false
			p.AI.PerUserDailyLimit = 0
			p.AI.AdKiller.Enabled = true
			p.AI.AdKiller.EnabledChatIDs = []int64{-100123}
			parent, stop := context.WithCancel(context.Background())
			defer stop()
			child, cc := context.WithTimeout(parent, 40*time.Millisecond)
			defer cc()
			ctx := context.WithValue(child, adKillerParentContextKey{}, parent)
			if cancelParent {
				time.AfterFunc(20*time.Millisecond, stop)
			}
			msg := &tele.Message{ID: 10, Text: "synthetic", Chat: &tele.Chat{ID: -100123, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 42, FirstName: "Test"}}
			_ = svc.applyAIModeration(ctx, msg, p, false, reviewableContent{Text: msg.Text, Kind: "text"}, false)
			want := int32(1)
			if cancelParent {
				want = 0
			}
			if got := atomic.LoadInt32(calls); got != want {
				t.Fatalf("calls=%d want=%d", got, want)
			}
		})
	}
}

func TestAdKillerBioDeadlineAndCancellation(t *testing.T) {
	for _, entrance := range []string{"join", "on_message"} {
		for _, cancelParent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancel_%v", entrance, cancelParent), func(t *testing.T) {
				svc, calls := newAdKillerTestService(t, func(w http.ResponseWriter, _ *http.Request) {
					_ = json.NewEncoder(w).Encode(map[string]any{"score": 95, "level": "ad", "primary_category": "goods_sale", "dimensions": validAdKillerDimensions()})
				})
				b, _ := newMockTelegramBot(t, "synthetic bio")
				db := newModerationProfileMatchMockDB(-100123, 42)
				svc.queries = store.New(db)
				svc.bot = b
				ref := ai.NewModelRef("p", "m")
				models := aiErrorFallbackModelRegistry{items: map[ai.ModelRef]ai.Model{ref: {ProviderKey: "p", ModelKey: "m", Enabled: true, CapabilityTags: []string{"moderation"}}}}
				svc.aiModerator = ai.NewModerator(context.Background(), zap.NewNop(), nil, svc.queries, aiErrorFallbackProviderRegistry{clients: map[string]ai.LLMClient{"p": waitRoutingClient{}}}, models, ai.NewResolver(models), nil)
				p := config.DefaultPolicy
				p.Verify.CheckProfile = true
				p.Verify.ProfileCheckMode = "ai"
				p.AI.Enabled = true
				p.AI.ProfileOnMessageMode = "ai"
				p.AI.PrimaryModelRef = ref.String()
				p.AI.FallbackModelRefs = nil
				p.AI.FallbackChain = nil
				p.AI.BatchWindowMs = 1
				p.AI.MaxRetries = 0
				p.AI.PerUserDailyLimit = 0
				p.AI.AdKiller.Enabled = true
				p.AI.AdKiller.EnabledChatIDs = []int64{-100123}
				parent, stop := context.WithCancel(context.Background())
				defer stop()
				child, cc := context.WithTimeout(parent, 40*time.Millisecond)
				defer cc()
				ctx := context.WithValue(child, adKillerParentContextKey{}, parent)
				if cancelParent {
					time.AfterFunc(20*time.Millisecond, stop)
				}
				chat := &tele.Chat{ID: -100123, Type: tele.ChatSuperGroup}
				user := &tele.User{ID: 42, FirstName: "Test"}
				var matched string
				var err error
				if entrance == "join" {
					matched, _, err = svc.checkProfile(ctx, chat, user, p)
				} else {
					matched, _, err = svc.checkProfileOnMessage(ctx, chat, user, p)
				}
				want := int32(1)
				if cancelParent {
					want = 0
				}
				if got := atomic.LoadInt32(calls); got != want {
					t.Fatalf("calls=%d want=%d", got, want)
				}
				if !cancelParent && (err != nil || matched == "") {
					t.Fatalf("fallback: matched=%q err=%v", matched, err)
				}
				if cancelParent && err == nil {
					t.Fatal("cancelled bio accepted")
				}
			})
		}
	}
}
