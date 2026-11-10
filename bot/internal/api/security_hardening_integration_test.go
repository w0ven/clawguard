//go:build integration

package api

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

func newSecurityHardeningTestServer(t *testing.T) (*Server, *h1Fixture, store.Admin, ed25519.PrivateKey) {
	t.Helper()
	fx := openH1Fixture(t)
	if err := ai.ConfigureEncryption("0123456789abcdef0123456789abcdef", zap.NewNop()); err != nil {
		t.Fatalf("ConfigureEncryption: %v", err)
	}

	oldTransport := http.DefaultTransport
	http.DefaultTransport = exemptTelegramTransport{}
	t.Cleanup(func() { http.DefaultTransport = oldTransport })

	redisClient := newAPIFakeRedisClient(t)
	providers := ai.NewProviderRegistry(zap.NewNop(), fx.queries)
	models := ai.NewModelRegistry(fx.queries)
	cfg := config.Config{
		BotToken:      "123456:TEST_BOT_TOKEN_FOR_AUDIT",
		BotUsername:   "test_bot",
		WebhookSecret: "0123456789abcdef0123456789abcdef",
		PublicBaseURL: "https://guard.example.com",
		JWTSecret:     "fedcba9876543210fedcba9876543210",
	}
	svc, err := bot.New(context.Background(), cfg, zap.NewNop(), fx.queries, redisClient, providers, models, ai.NewResolver(models))
	if err != nil {
		t.Fatalf("bot.New: %v", err)
	}
	t.Cleanup(svc.Stop)

	owner, err := fx.queries.UpsertAdmin(context.Background(), store.UpsertAdminParams{
		TelegramID: 6425070392,
		Username:   stringPtr("iike1e"),
		FirstName:  stringPtr("Owner"),
		Role:       "owner",
		GroupScope: []byte("[]"),
	})
	if err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	srv := NewServer(cfg, zap.NewNop(), svc, nil)
	srv.webAppPubKey = pub
	return srv, fx, owner, priv
}

func buildSignedInitDataForTest(t *testing.T, botToken string, priv ed25519.PrivateKey, tgID int64, queryID string, now time.Time) string {
	t.Helper()
	botID, _, _ := strings.Cut(botToken, ":")
	values := url.Values{
		"auth_date": {strconv.FormatInt(now.Unix(), 10)},
		"query_id":  {queryID},
		"user":      {fmt.Sprintf(`{"id":%d,"first_name":"Owner","username":"iike1e"}`, tgID)},
	}
	edKeys := []string{"auth_date", "query_id", "user"}
	sort.Strings(edKeys)
	edParts := make([]string, 0, len(edKeys))
	for _, k := range edKeys {
		edParts = append(edParts, k+"="+values.Get(k))
	}
	edSig := ed25519.Sign(priv, []byte(botID+":WebAppData\n"+strings.Join(edParts, "\n")))
	values.Set("signature", base64.RawURLEncoding.EncodeToString(edSig))

	hmacKeys := []string{"auth_date", "query_id", "signature", "user"}
	sort.Strings(hmacKeys)
	hmacParts := make([]string, 0, len(hmacKeys))
	for _, k := range hmacKeys {
		hmacParts = append(hmacParts, k+"="+values.Get(k))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(botToken))
	check := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = check.Write([]byte(strings.Join(hmacParts, "\n")))
	values.Set("hash", hex.EncodeToString(check.Sum(nil)))
	return values.Encode()
}

func doOwnerJSONRequest(t *testing.T, srv *Server, owner store.Admin, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	jwtToken, err := srv.signAdminJWT(owner)
	if err != nil {
		t.Fatalf("signAdminJWT: %v", err)
	}
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestLLMProviderUpdateRejectsBaseURLChangeWithoutAPIKeyAndBlocksSSRF(t *testing.T) {
	srv, fx, owner, _ := newSecurityHardeningTestServer(t)

	// 1. Reject SSRF base_url on create even with valid API key.
	for _, badURL := range []string{
		"http://127.0.0.1:8080/v1",
		"http://169.254.169.254/latest/meta-data",
		"http://localhost:8080/v1",
		"http://172.18.0.2:8080/v1",
		"https://user:pass@parrot.ke1e.de/v1",
	} {
		rec := doOwnerJSONRequest(t, srv, owner, http.MethodPost, "/api/admin/llm/providers", fmt.Sprintf(
			`{"key":"bad","label":"Bad","type":"openai","base_url":%q,"api_key":"sk-test"}`, badURL,
		))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("create with SSRF URL %q status = %d, want 400; body=%s", badURL, rec.Code, rec.Body.String())
		}
	}

	// 2. Reject Host header override in extra_headers.
	recHostHdr := doOwnerJSONRequest(t, srv, owner, http.MethodPost, "/api/admin/llm/providers",
		`{"key":"badhdr","label":"BadHdr","type":"openai","base_url":"https://parrot.ke1e.de/v1","api_key":"sk-test","extra_headers":{"Host":"internal.local"}}`,
	)
	if recHostHdr.Code != http.StatusBadRequest {
		t.Fatalf("create with Host extra_header status = %d, want 400", recHostHdr.Code)
	}

	// 3. Create legitimate provider with secret key and custom auth header.
	const origSecret = "sk-live-super-secret-orig-key"
	const origHeaderSecret = "hdr-live-super-secret-token"
	recCreate := doOwnerJSONRequest(t, srv, owner, http.MethodPost, "/api/admin/llm/providers", fmt.Sprintf(
		`{"key":"att","label":"parrot","type":"openai","base_url":"https://parrot.ke1e.de/v1","api_key":%q,"extra_headers":{"X-Custom-Auth":%q}}`,
		origSecret, origHeaderSecret,
	))
	if recCreate.Code != http.StatusCreated {
		t.Fatalf("create provider status = %d, want 201; body=%s", recCreate.Code, recCreate.Body.String())
	}

	// 4. Attack reproduction: attempt to redirect base_url to attacker server with empty api_key.
	recSteal := doOwnerJSONRequest(t, srv, owner, http.MethodPut, "/api/admin/llm/providers/att",
		`{"label":"parrot","type":"openai","base_url":"https://attacker.example.com/v1","api_key":""}`,
	)
	if recSteal.Code != http.StatusBadRequest {
		t.Fatalf("redirect base_url without api_key status = %d, want 400; body=%s", recSteal.Code, recSteal.Body.String())
	}

	// Also reject scheme downgrade (https -> http) without re-entering api_key.
	recDowngrade := doOwnerJSONRequest(t, srv, owner, http.MethodPut, "/api/admin/llm/providers/att",
		`{"base_url":"http://parrot.ke1e.de/v1","api_key":""}`,
	)
	if recDowngrade.Code != http.StatusBadRequest {
		t.Fatalf("downgrade scheme without api_key status = %d, want 400", recDowngrade.Code)
	}

	storedAfterAttack, err := fx.queries.GetProviderByKey(context.Background(), "att")
	if err != nil {
		t.Fatalf("GetProviderByKey: %v", err)
	}
	if storedAfterAttack.BaseURL != "https://parrot.ke1e.de/v1" {
		t.Fatalf("base_url mutated after rejected update: got %q", storedAfterAttack.BaseURL)
	}

	// 5. Benign metadata/toggle updates that keep the same canonical base_url still work without re-entering api_key.
	recToggle := doOwnerJSONRequest(t, srv, owner, http.MethodPut, "/api/admin/llm/providers/att",
		`{"enabled":false}`,
	)
	if recToggle.Code != http.StatusOK {
		t.Fatalf("toggle enabled status = %d, want 200; body=%s", recToggle.Code, recToggle.Body.String())
	}
	recSameCanonical := doOwnerJSONRequest(t, srv, owner, http.MethodPut, "/api/admin/llm/providers/att",
		`{"label":"parrot-renamed","base_url":"https://PARROT.ke1e.de:443/v1/","api_key":"","enabled":true}`,
	)
	if recSameCanonical.Code != http.StatusOK {
		t.Fatalf("canonical-equivalent base_url update status = %d, want 200; body=%s", recSameCanonical.Code, recSameCanonical.Body.String())
	}

	// 6. Legitimate base_url migration succeeds when a new api_key is supplied.
	const rotatedSecret = "sk-live-rotated-key-999"
	recMigrate := doOwnerJSONRequest(t, srv, owner, http.MethodPut, "/api/admin/llm/providers/att", fmt.Sprintf(
		`{"base_url":"https://llm2.example.com/v1","api_key":%q}`, rotatedSecret,
	))
	if recMigrate.Code != http.StatusOK {
		t.Fatalf("base_url update with new api_key status = %d, want 200; body=%s", recMigrate.Code, recMigrate.Body.String())
	}

	// 7. Model create / update / delete and provider delete all write redacted config_audit rows.
	if rec := doOwnerJSONRequest(t, srv, owner, http.MethodPost, "/api/admin/llm/models",
		`{"provider_key":"att","model_key":"gpt-test","label":"GPT Test"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create model status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if rec := doOwnerJSONRequest(t, srv, owner, http.MethodPut, "/api/admin/llm/models/att/gpt-test",
		`{"label":"GPT Test Updated"}`); rec.Code != http.StatusOK {
		t.Fatalf("update model status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if rec := doOwnerJSONRequest(t, srv, owner, http.MethodDelete, "/api/admin/llm/models/att/gpt-test", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete model status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if rec := doOwnerJSONRequest(t, srv, owner, http.MethodDelete, "/api/admin/llm/providers/att", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete provider status = %d; body=%s", rec.Code, rec.Body.String())
	}

	audits, err := fx.queries.ListAuditByChat(context.Background(), store.ListAuditByChatParams{
		Limit:  50,
		Global: true,
		Scope:  []int64{},
	})
	if err != nil {
		t.Fatalf("ListAuditByChat: %v", err)
	}
	seenActions := map[string]int{}
	for _, entry := range audits {
		seenActions[entry.Action]++
		raw := string(entry.Before) + string(entry.After)
		for _, secret := range []string{origSecret, rotatedSecret, origHeaderSecret} {
			if strings.Contains(raw, secret) {
				t.Fatalf("secret %q leaked into config_audit entry %s: %s", secret, entry.Action, raw)
			}
		}
	}
	for _, wantAction := range []string{
		"create_llm_provider",
		"update_llm_provider",
		"delete_llm_provider",
		"create_llm_model",
		"update_llm_model",
		"delete_llm_model",
	} {
		if seenActions[wantAction] == 0 {
			t.Fatalf("missing audit action %q in %v", wantAction, seenActions)
		}
	}
}

func TestBotTokenLeakCannotImpersonateAdminAndZeroTrustLoginAuditWorks(t *testing.T) {
	srv, fx, owner, telegramEdPriv := newSecurityHardeningTestServer(t)
	now := time.Now()
	authDate := strconv.FormatInt(now.Unix(), 10)

	// 1. Attack reproduction #1: Attacker steals BOT_TOKEN and crafts a valid Telegram Login Widget HMAC for Owner 6425070392.
	loginFields := map[string]string{
		"auth_date":  authDate,
		"first_name": "Owner",
		"id":         "6425070392",
		"username":   "iike1e",
	}
	keys := []string{"auth_date", "first_name", "id", "username"}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+loginFields[k])
	}
	botHash := sha256.Sum256([]byte(srv.cfg.BotToken))
	mac := hmac.New(sha256.New, botHash[:])
	_, _ = mac.Write([]byte(strings.Join(parts, "\n")))
	q := url.Values{}
	for k, v := range loginFields {
		q.Set(k, v)
	}
	q.Set("hash", hex.EncodeToString(mac.Sum(nil)))

	reqForgedWidget := httptest.NewRequest(http.MethodPost, "/api/auth/telegram-login?"+q.Encode(), nil)
	recForgedWidget := httptest.NewRecorder()
	srv.ServeHTTP(recForgedWidget, reqForgedWidget)
	if recForgedWidget.Code != http.StatusForbidden {
		t.Fatalf("forged telegram-login with leaked BOT_TOKEN status = %d, want 403", recForgedWidget.Code)
	}
	if len(recForgedWidget.Result().Cookies()) > 0 {
		t.Fatalf("forged telegram-login issued cookies: %+v", recForgedWidget.Result().Cookies())
	}

	// 2. Attack reproduction #2: Attacker steals BOT_TOKEN and crafts MiniApp initData with valid BOT_TOKEN HMAC but forged Ed25519 key.
	_, attackerFakePriv, _ := ed25519.GenerateKey(rand.Reader)
	forgedMiniInit := buildSignedInitDataForTest(t, srv.cfg.BotToken, attackerFakePriv, 6425070392, "forged-q1", now)
	forgedMiniBody, _ := json.Marshal(map[string]string{"init_data": forgedMiniInit})
	reqForgedMini := httptest.NewRequest(http.MethodPost, "/api/auth/miniapp", strings.NewReader(string(forgedMiniBody)))
	reqForgedMini.Header.Set("Content-Type", "application/json")
	recForgedMini := httptest.NewRecorder()
	srv.ServeHTTP(recForgedMini, reqForgedMini)
	if recForgedMini.Code != http.StatusUnauthorized {
		t.Fatalf("forged miniapp login without Telegram Ed25519 private key status = %d, want 401; body=%s", recForgedMini.Code, recForgedMini.Body.String())
	}

	// 3. Attack reproduction #3: Legacy plaintext magic link exchange is retired (410 Gone).
	reqMagic := httptest.NewRequest(http.MethodPost, "/api/public/magic/exchange", strings.NewReader(`{"token":"leaked-token"}`))
	reqMagic.Header.Set("Content-Type", "application/json")
	recMagic := httptest.NewRecorder()
	srv.ServeHTTP(recMagic, reqMagic)
	if recMagic.Code != http.StatusGone {
		t.Fatalf("legacy magic exchange status = %d, want 410", recMagic.Code)
	}

	// 4. Attack reproduction #4: Knowing WebhookPathToken (from getWebhookInfo) cannot forge webhook updates without WebhookHeaderToken.
	reqForgedWebhook := httptest.NewRequest(http.MethodPost, "/webhook/"+srv.cfg.WebhookPathToken(), strings.NewReader(`{"update_id":1}`))
	reqForgedWebhook.Header.Set("Content-Type", "application/json")
	reqForgedWebhook.Header.Set("X-Telegram-Bot-Api-Secret-Token", srv.cfg.WebhookPathToken())
	recForgedWebhook := httptest.NewRecorder()
	srv.ServeHTTP(recForgedWebhook, reqForgedWebhook)
	if recForgedWebhook.Code != http.StatusUnauthorized {
		t.Fatalf("forged webhook using path token as header secret status = %d, want 401", recForgedWebhook.Code)
	}

	// 5. Legitimate Mini App login signed by Telegram's Ed25519 key succeeds once, writes audit, and rejects replay.
	validMiniInit := buildSignedInitDataForTest(t, srv.cfg.BotToken, telegramEdPriv, 6425070392, "real-q1", now)
	validMiniBody, _ := json.Marshal(map[string]string{"init_data": validMiniInit})
	reqMini := httptest.NewRequest(http.MethodPost, "/api/auth/miniapp", strings.NewReader(string(validMiniBody)))
	reqMini.Header.Set("Content-Type", "application/json")
	reqMini.RemoteAddr = "198.51.100.55:51234"
	reqMini.Header.Set("X-Forwarded-For", "8.8.8.8") // Direct public peer spoofing XFF
	recMini := httptest.NewRecorder()
	srv.ServeHTTP(recMini, reqMini)
	if recMini.Code != http.StatusOK {
		t.Fatalf("valid Ed25519 miniapp login status = %d; body=%s", recMini.Code, recMini.Body.String())
	}

	// Replaying the exact same signed initData a second time must be rejected!
	reqReplay := httptest.NewRequest(http.MethodPost, "/api/auth/miniapp", strings.NewReader(string(validMiniBody)))
	reqReplay.Header.Set("Content-Type", "application/json")
	recReplay := httptest.NewRecorder()
	srv.ServeHTTP(recReplay, reqReplay)
	if recReplay.Code != http.StatusUnauthorized {
		t.Fatalf("replayed miniapp initData status = %d, want 401", recReplay.Code)
	}

	// 6. Legitimate Web Browser Challenge -> Telegram Ed25519 Approval -> Poll Exchange flow.
	reqChal := httptest.NewRequest(http.MethodPost, "/api/auth/web-challenge", strings.NewReader(`{}`))
	reqChal.Header.Set("Content-Type", "application/json")
	reqChal.RemoteAddr = "172.18.0.6:50000"
	reqChal.Header.Set("X-Forwarded-For", "1.1.1.1, 198.51.100.99")
	recChal := httptest.NewRecorder()
	srv.ServeHTTP(recChal, reqChal)
	if recChal.Code != http.StatusOK {
		t.Fatalf("web-challenge status = %d; body=%s", recChal.Code, recChal.Body.String())
	}
	var chalResp struct {
		ChallengeID string `json:"challenge_id"`
		Code        string `json:"code"`
	}
	if err := json.Unmarshal(recChal.Body.Bytes(), &chalResp); err != nil {
		t.Fatalf("unmarshal challenge: %v", err)
	}
	var pollCookie *http.Cookie
	for _, ck := range recChal.Result().Cookies() {
		if ck.Name == loginPollCookieName {
			pollCookie = ck
		}
	}
	if pollCookie == nil {
		t.Fatal("missing cg_login_poll cookie from web-challenge")
	}

	// Non-admin user's real Telegram Ed25519 signature cannot approve the challenge.
	nonAdminInit := buildSignedInitDataForTest(t, srv.cfg.BotToken, telegramEdPriv, 999888777, "stranger-q", now)
	nonAdminApproveBody, _ := json.Marshal(map[string]string{"code": chalResp.Code, "init_data": nonAdminInit})
	reqStrangerApprove := httptest.NewRequest(http.MethodPost, "/api/auth/web-approve", strings.NewReader(string(nonAdminApproveBody)))
	reqStrangerApprove.Header.Set("Content-Type", "application/json")
	recStrangerApprove := httptest.NewRecorder()
	srv.ServeHTTP(recStrangerApprove, reqStrangerApprove)
	if recStrangerApprove.Code != http.StatusForbidden {
		t.Fatalf("non-admin web-approve status = %d, want 403", recStrangerApprove.Code)
	}

	// Real Owner approves via Telegram Mini App Ed25519 signature.
	// First, verify a mistyped 6-char code returns 404 WITHOUT burning the session's initData signature,
	// while an already-used initData from /api/auth/miniapp is still immediately rejected with 401.
	replayedApproveBody, _ := json.Marshal(map[string]string{"code": "ZZZZZZ", "init_data": validMiniInit})
	reqReplayedApprove := httptest.NewRequest(http.MethodPost, "/api/auth/web-approve", strings.NewReader(string(replayedApproveBody)))
	reqReplayedApprove.Header.Set("Content-Type", "application/json")
	recReplayedApprove := httptest.NewRecorder()
	srv.ServeHTTP(recReplayedApprove, reqReplayedApprove)
	if recReplayedApprove.Code != http.StatusUnauthorized {
		t.Fatalf("replayed miniapp initData on web-approve status = %d, want 401", recReplayedApprove.Code)
	}

	ownerApproveInit := buildSignedInitDataForTest(t, srv.cfg.BotToken, telegramEdPriv, 6425070392, "approve-q1", now)
	typoCode := "ZZZZZZ"
	if chalResp.Code == typoCode {
		typoCode = "YYYYYY"
	}
	typoApproveBody, _ := json.Marshal(map[string]string{"code": typoCode, "init_data": ownerApproveInit})
	reqTypoApprove := httptest.NewRequest(http.MethodPost, "/api/auth/web-approve", strings.NewReader(string(typoApproveBody)))
	reqTypoApprove.Header.Set("Content-Type", "application/json")
	recTypoApprove := httptest.NewRecorder()
	srv.ServeHTTP(recTypoApprove, reqTypoApprove)
	if recTypoApprove.Code != http.StatusNotFound {
		t.Fatalf("typo code web-approve status = %d, want 404", recTypoApprove.Code)
	}

	approveBody, _ := json.Marshal(map[string]string{"code": chalResp.Code, "init_data": ownerApproveInit})
	reqApprove := httptest.NewRequest(http.MethodPost, "/api/auth/web-approve", strings.NewReader(string(approveBody)))
	reqApprove.Header.Set("Content-Type", "application/json")
	reqApprove.RemoteAddr = "203.0.113.77:54321"
	recApprove := httptest.NewRecorder()
	srv.ServeHTTP(recApprove, reqApprove)
	if recApprove.Code != http.StatusOK {
		t.Fatalf("owner web-approve after typo correction status = %d; body=%s", recApprove.Code, recApprove.Body.String())
	}

	// Replaying ownerApproveInit after successful approval must fail with 401!
	reqReplayApprove := httptest.NewRequest(http.MethodPost, "/api/auth/web-approve", strings.NewReader(string(approveBody)))
	reqReplayApprove.Header.Set("Content-Type", "application/json")
	recReplayApprove := httptest.NewRecorder()
	srv.ServeHTTP(recReplayApprove, reqReplayApprove)
	if recReplayApprove.Code != http.StatusUnauthorized {
		t.Fatalf("replayed ownerApproveInit after approval status = %d, want 401", recReplayApprove.Code)
	}

	// Attacker who guesses challenge_id WITHOUT the browser's HttpOnly cg_login_poll cookie cannot steal the approved session!
	pollBodyBytes, _ := json.Marshal(map[string]string{"challenge_id": chalResp.ChallengeID})
	reqHijackPoll := httptest.NewRequest(http.MethodPost, "/api/auth/web-poll", strings.NewReader(string(pollBodyBytes)))
	reqHijackPoll.Header.Set("Content-Type", "application/json")
	recHijackPoll := httptest.NewRecorder()
	srv.ServeHTTP(recHijackPoll, reqHijackPoll)
	if recHijackPoll.Code != http.StatusUnauthorized {
		t.Fatalf("hijack web-poll without cookie status = %d, want 401", recHijackPoll.Code)
	}

	// Initiating browser with matching cg_login_poll cookie exchanges once and receives cg_admin JWT.
	reqPoll := httptest.NewRequest(http.MethodPost, "/api/auth/web-poll", strings.NewReader(string(pollBodyBytes)))
	reqPoll.Header.Set("Content-Type", "application/json")
	reqPoll.RemoteAddr = "172.18.0.6:50001"
	reqPoll.Header.Set("X-Forwarded-For", "1.1.1.1, 198.51.100.99")
	reqPoll.AddCookie(pollCookie)
	recPoll := httptest.NewRecorder()
	srv.ServeHTTP(recPoll, reqPoll)
	if recPoll.Code != http.StatusOK {
		t.Fatalf("legitimate web-poll status = %d; body=%s", recPoll.Code, recPoll.Body.String())
	}
	var gotAdminCookie bool
	for _, ck := range recPoll.Result().Cookies() {
		if ck.Name == adminCookieName && ck.Value != "" {
			gotAdminCookie = true
		}
	}
	if !gotAdminCookie {
		t.Fatal("approved web-poll did not set cg_admin cookie")
	}

	// 7. Admin create/update/delete & AdKiller secret update/clear audits.
	recCreateAdmin := doOwnerJSONRequest(t, srv, owner, http.MethodPost, "/api/admin/admins",
		`{"telegram_id":73578332,"role":"admin","notes":"十一","group_scope":[-1002699516772]}`,
	)
	if recCreateAdmin.Code != http.StatusOK {
		t.Fatalf("create admin status = %d; body=%s", recCreateAdmin.Code, recCreateAdmin.Body.String())
	}
	var createdAdminResp struct {
		Admin struct {
			ID int64 `json:"id"`
		} `json:"admin"`
	}
	_ = json.Unmarshal(recCreateAdmin.Body.Bytes(), &createdAdminResp)

	if rec := doOwnerJSONRequest(t, srv, owner, http.MethodPut,
		fmt.Sprintf("/api/admin/admins/%d", createdAdminResp.Admin.ID),
		`{"role":"admin","notes":"十一-updated","group_scope":[]}`); rec.Code != http.StatusOK {
		t.Fatalf("update admin status = %d", rec.Code)
	}
	if rec := doOwnerJSONRequest(t, srv, owner, http.MethodDelete,
		fmt.Sprintf("/api/admin/admins/%d", createdAdminResp.Admin.ID), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete admin status = %d", rec.Code)
	}

	const adkillerSecret = "ak-live-secret-value-xyz"
	if rec := doOwnerJSONRequest(t, srv, owner, http.MethodPut, "/api/admin/adkiller",
		fmt.Sprintf(`{"api_key":%q}`, adkillerSecret)); rec.Code != http.StatusOK {
		t.Fatalf("put adkiller secret status = %d", rec.Code)
	}
	if rec := doOwnerJSONRequest(t, srv, owner, http.MethodPut, "/api/admin/adkiller",
		`{"clear":true}`); rec.Code != http.StatusOK {
		t.Fatalf("clear adkiller secret status = %d", rec.Code)
	}

	audits, err := fx.queries.ListAuditByChat(context.Background(), store.ListAuditByChatParams{
		Limit:  50,
		Global: true,
		Scope:  []int64{},
	})
	if err != nil {
		t.Fatalf("ListAuditByChat: %v", err)
	}

	byAction := map[string]store.ConfigAudit{}
	for _, entry := range audits {
		byAction[entry.Action] = entry
		if strings.Contains(string(entry.Before)+string(entry.After), adkillerSecret) {
			t.Fatalf("adkiller plaintext secret leaked into audit: %s", string(entry.After))
		}
	}
	for _, want := range []string{
		"upsert_admin",
		"update_admin",
		"delete_admin",
		"update_adkiller_secret",
		"clear_adkiller_secret",
		"miniapp_login",
		"web_challenge_login",
	} {
		if _, ok := byAction[want]; !ok {
			t.Fatalf("missing audit action %q", want)
		}
	}
	var webLoginAfter map[string]any
	if err := json.Unmarshal(byAction["web_challenge_login"].After, &webLoginAfter); err != nil {
		t.Fatalf("unmarshal web_challenge_login audit: %v", err)
	}
	if webLoginAfter["ip"] != "198.51.100.99" || webLoginAfter["approver_ip"] != "203.0.113.77" {
		t.Fatalf("web_challenge_login audit missing requester/approver IP: %s", string(byAction["web_challenge_login"].After))
	}
	var miniLoginAfter map[string]any
	if err := json.Unmarshal(byAction["miniapp_login"].After, &miniLoginAfter); err != nil {
		t.Fatalf("unmarshal miniapp_login audit: %v", err)
	}
	if miniLoginAfter["ip"] != "198.51.100.55" {
		t.Fatalf("miniapp_login audit accepted spoofed XFF from direct peer: %s", string(byAction["miniapp_login"].After))
	}
}
