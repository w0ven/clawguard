package bot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openclaw/clawguard/internal/store"
	"github.com/pressly/goose/v3"
	tele "gopkg.in/telebot.v3"
)

func nativeLiveDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("CG_NATIVE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires dedicated local cg_native_test database and native/.venv")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Path != "/cg_native_test" || u.User.Username() != "cg_native_test" {
		t.Fatal("refusing non-task database")
	}
	return dsn
}

type nativeLiveStack struct {
	ctx      context.Context
	cancel   context.CancelFunc
	pool     *pgxpool.Pool
	q        *store.Queries
	service  *Service
	mu       sync.Mutex
	requests []map[string]any
	sends    []map[string]any
	files    []string
	dataDir  string
	port     int
	broker   *httptest.Server
	nativeRoot string
}

func startNativeLiveGo(t *testing.T, chatID int64, poolMaxTokens int) *nativeLiveStack {
	t.Helper()
	dsn := nativeLiveDSN(t)
	ctx, cancel := context.WithCancel(context.Background())
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = goose.UpToContext(ctx, db, "../../migrations", 36); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	live := &nativeLiveStack{ctx: ctx, cancel: cancel, pool: pool, q: store.New(pool), dataDir: t.TempDir()}
	t.Cleanup(func() {
		cancel()
		if live.service != nil {
			live.service.wg.Wait()
		}
		pool.Close()
	})
	a, poolConfig := assistantVerificationPool(t, func(w http.ResponseWriter, r *http.Request) {
		var value map[string]any
		_ = json.NewDecoder(r.Body).Decode(&value)
		live.mu.Lock()
		live.requests = append(live.requests, value)
		live.mu.Unlock()
		content := "原生固定回复"
		if msgs, ok := value["messages"].([]any); ok && len(msgs) > 0 {
			raw, _ := json.Marshal(msgs[0])
			if strings.Contains(string(raw), "image_url") {
				content = "出口准备图"
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": content}}}})
	})
	if poolMaxTokens > 0 {
		assign := poolConfig.TaskAssignments["chat"]
		assign.MaxTokens = poolMaxTokens
		poolConfig.TaskAssignments["chat"] = assign
	}
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/file/bot") {
			w.Write([]byte("\x89PNG\r\n\x1a\nfixture-image-bytes"))
			return
		}
		var value map[string]any
		_ = json.NewDecoder(r.Body).Decode(&value)
		var result any = true
		switch {
		case strings.HasSuffix(r.URL.Path, "getChatMember"):
			result = map[string]any{"status": "member", "user": map[string]any{"id": 7, "is_bot": false, "first_name": "成员"}}
		case strings.HasSuffix(r.URL.Path, "getFile"):
			fileID, _ := value["file_id"].(string)
			live.mu.Lock()
			live.files = append(live.files, fileID)
			live.mu.Unlock()
			result = map[string]any{"file_id": fileID, "file_unique_id": "uniq", "file_path": "photos/fixture.jpg", "file_size": 24}
		case strings.HasSuffix(r.URL.Path, "sendMessage"):
			live.mu.Lock()
			live.sends = append(live.sends, value)
			id := 900 + len(live.sends)
			live.mu.Unlock()
			result = map[string]any{"message_id": id, "date": time.Now().Unix(), "chat": map[string]any{"id": chatID, "type": "supergroup", "title": "隔离群"}, "from": map[string]any{"id": 900, "is_bot": true, "first_name": "ClawGuard", "username": "assistant_test_bot"}, "text": value["text"]}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	t.Cleanup(tg.Close)
	service := a.service
	service.cfg.AssistantEngine = "native"
	service.cfg.AssistantBrokerSecret = strings.Repeat("local-secret-", 4)
	service.cfg.JWTSecret = strings.Repeat("signing-key-", 4)
	service.bot = &tele.Bot{Token: "123:isolated", URL: tg.URL, Me: &tele.User{ID: 900, IsBot: true, FirstName: "ClawGuard", Username: "assistant_test_bot"}}
	service.queries = live.q
	service.lifecycleCtx = ctx
	service.aiModels = a.models
	service.aiProviders = a.providers
	a = NewGroupAssistant(service)
	service.assistant = a
	if a.native == nil {
		t.Fatal("official native constructor path was not activated")
	}
	live.service = service
	live.broker = httptest.NewServer(service.NativeAssistantHandler())
	t.Cleanup(live.broker.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	live.port = listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	live.nativeRoot, err = filepath.Abs("../../../native")
	if err != nil {
		t.Fatal(err)
	}
	service.cfg.AssistantNativeURL = fmt.Sprintf("http://127.0.0.1:%d", live.port)
	raw, _ := json.Marshal(poolConfig)
	if _, err = pool.Exec(ctx, `INSERT INTO authorized_groups(chat_id,title) VALUES ($1,'隔离群') ON CONFLICT (chat_id) DO UPDATE SET enabled=true,title='隔离群'`, chatID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO group_assistant_policies(chat_id,chat_enabled,learning_enabled) VALUES ($1,true,true) ON CONFLICT (chat_id) DO UPDATE SET chat_enabled=true,learning_enabled=true`, chatID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO group_assistant_pools(chat_id,config) VALUES ($1,$2) ON CONFLICT (chat_id) DO UPDATE SET config=$2`, chatID, raw); err != nil {
		t.Fatal(err)
	}
	return live
}

func (live *nativeLiveStack) startPython(t *testing.T) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	childCtx, childCancel := context.WithCancel(live.ctx)
	command := exec.CommandContext(childCtx, filepath.Join(live.nativeRoot, ".venv/bin/python"), "-m", "clawguard_native.app")
	command.Dir = live.nativeRoot
	var output bytes.Buffer
	command.Env = append(os.Environ(), "NATIVE_DATA_DIR="+live.dataDir, fmt.Sprintf("NATIVE_PORT=%d", live.port), "CG_BROKER_URL="+live.broker.URL+"/internal/native", "ASSISTANT_BROKER_SECRET="+live.service.cfg.AssistantBrokerSecret)
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		childCancel()
		_ = command.Wait()
		if t.Failed() {
			t.Log(output.String())
		}
	})
	return command, &output
}

func waitNativeHealth(t *testing.T, url string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func waitNativeDone(t *testing.T, pool *pgxpool.Pool, chatID int64) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		var done int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM assistant_native_events WHERE chat_id=$1 AND status='done'`, chatID).Scan(&done); err != nil {
			t.Fatal(err)
		}
		if done > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("native event did not complete")
}

func writeActivationManifest(t *testing.T, dir string, chatID int64, hash string) string {
	t.Helper()
	path := filepath.Join(dir, "migration-manifest.json")
	body, _ := json.Marshal(map[string]any{
		"state": "prepared", "production": true,
		"profiles": map[string]any{
			fmt.Sprintf("%d", chatID): map[string]any{
				"legacy_policy_version": 10, "profile_sha256": hash,
				"target_user_id": 258605875, "sample_count": 14, "distilled_at_count": 14,
			},
		},
	})
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNativeAssistantActivationLiveGoPython(t *testing.T) {
	chatID := int64(-76551)
	profile := "latest model profile"
	sum := sha256.Sum256([]byte(profile))
	hash := hex.EncodeToString(sum[:])
	live := startNativeLiveGo(t, chatID, 0)
	if _, err := live.pool.Exec(live.ctx, `UPDATE group_assistant_policies SET version=10, mimic_target_user_id=258605875, mimic_target_user_name='coolchen', mimic_profile_text=$2, mimic_sample_count=14, mimic_distilled_at_count=14 WHERE chat_id=$1`, chatID, profile); err != nil {
		t.Fatal(err)
	}
	manifest := writeActivationManifest(t, live.dataDir, chatID, strings.Repeat("0", 64))
	failed, output := live.startPython(t)
	if waitNativeHealth(t, live.service.cfg.AssistantNativeURL, 6*time.Second) {
		t.Fatal("mismatched activation must not become healthy")
	}
	_ = failed.Process.Kill()
	_ = failed.Wait()
	time.Sleep(300 * time.Millisecond)
	raw, _ := os.ReadFile(manifest)
	if strings.Contains(string(raw), "activated_at") {
		t.Fatalf("failed activation wrote activated_at: %s\n%s", raw, output.String())
	}
	writeActivationManifest(t, live.dataDir, chatID, hash)
	_, readyOut := live.startPython(t)
	if !waitNativeHealth(t, live.service.cfg.AssistantNativeURL, 20*time.Second) {
		t.Fatalf("matching activation did not start:\n%s\n%s", output.String(), readyOut.String())
	}
	raw, _ = os.ReadFile(manifest)
	if !strings.Contains(string(raw), "activated_at") {
		t.Fatalf("matching activation did not persist activated_at: %s", raw)
	}
	msg := &tele.Message{ID: 1, Unixtime: time.Now().Unix(), Text: "@assistant_test_bot 你好", Sender: &tele.User{ID: 7, FirstName: "成员"}, Chat: &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup, Title: "隔离群"}}
	if err := live.service.handleApprovedAssistantMessage(live.ctx, msg, false, assistantEligibilityEligible, false, false); err != nil {
		t.Fatal(err)
	}
	waitNativeDone(t, live.pool, chatID)
	live.mu.Lock()
	defer live.mu.Unlock()
	if len(live.sends) != 1 {
		t.Fatalf("expected one delivery after activation, sends=%d", len(live.sends))
	}
}

func TestNativeAssistantInboundMediaGoPython(t *testing.T) {
	chatID := int64(-76552)
	live := startNativeLiveGo(t, chatID, 0)
	live.startPython(t)
	if !waitNativeHealth(t, live.service.cfg.AssistantNativeURL, 20*time.Second) {
		t.Fatal("native did not start")
	}
	msg := &tele.Message{
		ID: 11, Unixtime: time.Now().Unix(), Caption: "@assistant_test_bot 看这张图",
		Sender: &tele.User{ID: 7, FirstName: "成员"},
		Chat:   &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup, Title: "隔离群"},
		Photo:  &tele.Photo{File: tele.File{FileID: "photo-current", UniqueID: "u1", FileSize: 24}, Width: 64, Height: 64},
	}
	if err := live.service.handleApprovedAssistantMessage(live.ctx, msg, false, assistantEligibilityEligible, false, false); err != nil {
		t.Fatal(err)
	}
	waitNativeDone(t, live.pool, chatID)
	live.mu.Lock()
	defer live.mu.Unlock()
	if len(live.files) == 0 || !strings.Contains(strings.Join(live.files, ","), "photo-current") {
		t.Fatalf("inbound photo did not authorize getFile: %v", live.files)
	}
	if len(live.sends) != 1 {
		t.Fatalf("expected one delivery, sends=%d requests=%d", len(live.sends), len(live.requests))
	}
}

func TestNativeAssistantPoolMaxTokensOverlayGoPython(t *testing.T) {
	chatID := int64(-76553)
	live := startNativeLiveGo(t, chatID, 77)
	live.startPython(t)
	if !waitNativeHealth(t, live.service.cfg.AssistantNativeURL, 20*time.Second) {
		t.Fatal("native did not start")
	}
	msg := &tele.Message{ID: 1, Unixtime: time.Now().Unix(), Text: "@assistant_test_bot 你好", Sender: &tele.User{ID: 7, FirstName: "成员"}, Chat: &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup, Title: "隔离群"}}
	if err := live.service.handleApprovedAssistantMessage(live.ctx, msg, false, assistantEligibilityEligible, false, false); err != nil {
		t.Fatal(err)
	}
	waitNativeDone(t, live.pool, chatID)
	live.mu.Lock()
	defer live.mu.Unlock()
	found := false
	for _, req := range live.requests {
		if n, ok := req["max_tokens"].(float64); ok && int(n) == 77 {
			found = true
		}
	}
	if !found {
		t.Fatalf("CG assignment max_tokens overlay missing: %#v", live.requests)
	}
}
