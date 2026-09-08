package api

// Regression copy of independent HTTP/PG acceptance (BI-004); original assertions retained.
import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/alicebob/miniredis/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type reviewRound1APIModels struct{}

func (reviewRound1APIModels) Get(ref ai.ModelRef) (ai.Model, bool) {
	_, model, ok := ref.Parse()
	valid := ok && (model == "tools" || model == "tools2" || model == "tools3")
	return ai.Model{ProviderKey: "fake", ModelKey: model, Enabled: valid, SupportsTools: true}, valid
}
func (reviewRound1APIModels) List(ai.ModelFilter) []ai.Model { return nil }
func (reviewRound1APIModels) Reload(context.Context) error   { return nil }
func TestReviewRound1APIInheritanceAndLegacyWrites(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dsn := os.Getenv("CG_SOURCE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("dedicated local PG required")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Path != "/assistant_source_test" || u.User == nil || u.User.Username() != "assistant_source_test" {
		t.Fatal("refusing non-task DB")
	}
	db, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	q := store.New(db)
	chat := -time.Now().UnixNano() / 1000
	actor := -chat
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := db.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`INSERT INTO groups(chat_id,title,type,config)VALUES($1,'independent api','supergroup','{"moderation":"preserved"}')`, chat)
	exec(`INSERT INTO authorized_groups(chat_id)VALUES($1)`, chat)
	var adminID int64
	if e = db.QueryRow(ctx, `INSERT INTO admins(telegram_id,username,role,group_scope)VALUES($1,'independent','owner','[]')RETURNING id`, actor).Scan(&adminID); e != nil {
		t.Fatal(e)
	}
	oldTransport := http.DefaultTransport
	http.DefaultTransport = assistantAPITransport(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/getMe") {
			return nil, fmt.Errorf("blocked non-test external HTTP %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"id":900,"is_bot":true,"first_name":"Test","username":"independent_test_bot"}}`)), Request: r}, nil
	})
	defer func() { http.DefaultTransport = oldTransport }()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	cfg := config.Config{BotToken: "123:INDEPENDENT_TEST_ONLY", JWTSecret: "independent-test-only-jwt"}
	svc, e := bot.New(ctx, cfg, zap.NewNop(), q, rdb, assistantAPIProviders{}, reviewRound1APIModels{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer svc.Stop()
	s := NewServer(cfg, zap.NewNop(), svc, nil)
	token, e := jwt.NewWithClaims(jwt.SigningMethodHS256, adminClaims{Role: "owner", TgID: actor, RegisteredClaims: jwt.RegisteredClaims{Subject: fmt.Sprint(adminID), ID: "independent-api", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}).SignedString([]byte(cfg.JWTSecret))
	if e != nil {
		t.Fatal(e)
	}
	call := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.echo.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s code=%d want=%d body=%s", method, path, w.Code, want, w.Body.String())
		}
		var out map[string]any
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	root := fmt.Sprintf("/api/admin/groups/%d/assistant", chat)
	global := call("GET", "/api/admin/assistant/global", nil, 200)
	modelRoles := map[string]any{"main": map[string]any{"model_ref": "fake:tools", "fallbacks": []string{"fake:tools2", "fake:tools3"}, "strategy": "weighted", "model_options": map[string]any{"fake:tools": map[string]any{"weight": 5, "max_concurrency": 2}, "fake:tools2": map[string]any{"weight": 3, "max_concurrency": 2}, "fake:tools3": map[string]any{"weight": 2, "max_concurrency": 2}}}}
	call("PUT", "/api/admin/assistant/global", map[string]any{"expected_version": global["version"], "model_roles": modelRoles}, 200)
	got := call("GET", root, nil, 200)
	pool := got["model_pool"].(map[string]any)
	if pool["source"] != "global" || pool["strategy"] != "weighted" {
		t.Fatal("new group not global weighted", pool)
	}
	p := got["policy"].(map[string]any)
	if p["chat_enabled"] != false || p["learning_enabled"] != false {
		t.Fatal("GET enabled policy", p)
	}
	// Legacy route material model change must write through to the same effective pool.
	policy := map[string]any{"expected_version": 0, "chat_enabled": true, "learning_enabled": false, "trigger_mode": "mention_or_reply", "followup_window_sec": 30, "max_followup_turns": 5, "chat_model_ref": "fake:tools2", "learning_model_ref": "", "temperature": .3, "history_limit": 30, "retention_days": 7, "tool_allowlist": []string{"conversation_recall"}, "allow_domains": []string{}, "max_queue_depth": 2, "max_queue_wait_sec": 1, "system_prompt": "独立保全 override"}
	pr := call("PUT", root, policy, 200)
	policy["expected_version"] = pr["policy"].(map[string]any)["version"]
	effective, source, e := svc.Assistant().EffectivePool(ctx, chat)
	if e != nil {
		t.Fatal(e)
	}
	ref := func(c bot.AssistantPoolConfig) string {
		for _, ep := range c.Endpoints {
			if ep.ID == c.TaskAssignments["chat"].Primary {
				return ep.ModelRef
			}
		}
		return ""
	}
	if ref(effective) != "fake:tools2" || source != "group" {
		t.Fatal("legacy write ineffective", source, ref(effective))
	}
	saved := call("GET", root+"/model-pool", nil, 200)
	modern := map[string]any{"expected_version": saved["version"], "inherit_global": false, "strategy": "weighted", "task_assignments": map[string]any{"chat": map[string]any{"primary": "new", "backups": []string{"backup"}}}, "endpoints": []any{map[string]any{"id": "new", "model_ref": "fake:tools3", "role": "primary", "priority": 0, "weight": 5, "max_concurrency": 2, "timeout_ms": 1000, "cooldown_duration_sec": 1}, map[string]any{"id": "backup", "model_ref": "fake:tools", "role": "backup", "priority": 1, "weight": 3, "max_concurrency": 2, "timeout_ms": 1000, "cooldown_duration_sec": 1}}}
	custom := call("PUT", root+"/model-pool", modern, 200)
	pr = call("PUT", root, policy, 200)
	policy["expected_version"] = pr["policy"].(map[string]any)["version"]
	effective, _, e = svc.Assistant().EffectivePool(ctx, chat)
	if e != nil || ref(effective) != "fake:tools3" {
		t.Fatal("unchanged stale legacy field overwrote modern pool", ref(effective), e)
	}
	modern["inherit_global"] = true
	modern["expected_version"] = custom["version"]
	inherited := call("PUT", root+"/model-pool", modern, 200)
	effective, source, e = svc.Assistant().EffectivePool(ctx, chat)
	if e != nil || source != "global" || ref(effective) != "fake:tools" {
		t.Fatal("restore inheritance ineffective", source, ref(effective), e)
	}
	raw, _ := json.Marshal(inherited["saved_config"])
	if !strings.Contains(string(raw), "fake:tools3") {
		t.Fatal("restore deleted draft", string(raw))
	}
	var moderation string
	if e = db.QueryRow(ctx, `SELECT config::text FROM groups WHERE chat_id=$1`, chat).Scan(&moderation); e != nil || moderation != `{"moderation": "preserved"}` {
		t.Fatal("moderation mutated", moderation, e)
	}
	fresh, e := q.GetGroupAssistantPolicy(ctx, chat)
	if e != nil || fresh.SystemPrompt != "独立保全 override" || fresh.LearningEnabled || fresh.TTSMode != "off" {
		t.Fatal("policy preserve failed", fresh, e)
	}
	t.Log("real Go API + PG: global weighted save/read, new group inheritance/no auto-enable; changed legacy ref→same pool; unchanged stale ref preserves modern custom; restore inheritance retains saved draft; moderation/Prompt/learning/TTS preserved")
	// Boundary case: an explicit legacy change equals today's inherited global primary.
	policy["chat_model_ref"] = "fake:tools"
	call("PUT", root, policy, 200)
	effective, source, e = svc.Assistant().EffectivePool(ctx, chat)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("legacy field changed fake:tools2→fake:tools; effective ref=%s source=%s", ref(effective), source)
	global = call("GET", "/api/admin/assistant/global", nil, 200)
	call("PUT", "/api/admin/assistant/global", map[string]any{"expected_version": global["version"], "model_roles": map[string]any{"main": map[string]any{"model_ref": "fake:tools3"}}}, 200)
	effective, source, e = svc.Assistant().EffectivePool(ctx, chat)
	if e != nil {
		t.Fatal(e)
	}
	fresh, e = q.GetGroupAssistantPolicy(ctx, chat)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("after changing global: stored legacy ChatModelRef=%s actual=%s source=%s", fresh.ChatModelRef, ref(effective), source)
	if ref(effective) != fresh.ChatModelRef {
		t.Errorf("BI-004: material legacy model write was acknowledged but kept in inactive second channel; explicit requested=%s actual=%s source=%s", fresh.ChatModelRef, ref(effective), source)
	}
}
