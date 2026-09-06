package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/openclaw/clawguard/internal/store"
)

type fakeAuthorizedGroupWriter struct {
	existing    *store.AuthorizedGroup
	getErr      error
	created     store.AuthorizedGroup
	upsertErr   error
	upsertCalls []store.UpsertAuthorizedGroupParams
}

func (f *fakeAuthorizedGroupWriter) GetAuthorizedGroupByChatID(_ context.Context, chatID int64) (store.AuthorizedGroup, error) {
	if f.getErr != nil {
		return store.AuthorizedGroup{}, f.getErr
	}
	if f.existing != nil && f.existing.ChatID == chatID {
		return *f.existing, nil
	}
	return store.AuthorizedGroup{}, pgx.ErrNoRows
}

func (f *fakeAuthorizedGroupWriter) UpsertAuthorizedGroup(_ context.Context, arg store.UpsertAuthorizedGroupParams) (store.AuthorizedGroup, error) {
	f.upsertCalls = append(f.upsertCalls, arg)
	if f.upsertErr != nil {
		return store.AuthorizedGroup{}, f.upsertErr
	}
	if f.created.ChatID != 0 {
		return f.created, nil
	}
	title := ""
	if arg.Title != nil {
		title = *arg.Title
	}
	enabled := true
	if arg.Enabled != nil {
		enabled = *arg.Enabled
	}
	notes := ""
	if arg.Notes != nil {
		notes = *arg.Notes
	}
	return store.AuthorizedGroup{
		ChatID:       arg.ChatID,
		Title:        title,
		AuthorizedBy: arg.AuthorizedBy,
		Enabled:      enabled,
		Notes:        notes,
		AuthorizedAt: time.Unix(1_700_000_000, 0).UTC(),
	}, nil
}

func postCreateAuthorizedGroup(t *testing.T, queries authorizedGroupWriter, body string, afterCreate func(context.Context, store.AuthorizedGroup)) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/authorized-groups", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(adminContextKey, store.Admin{ID: 1, TelegramID: 42, Role: "owner"})
	if err := createAuthorizedGroupFromContext(c, queries, afterCreate); err != nil {
		t.Fatalf("createAuthorizedGroupFromContext: %v", err)
	}
	return rec
}

func TestCreateAuthorizedGroupPOSTConflictDoesNotOverwriteEnabledOrNotes(t *testing.T) {
	existing := store.AuthorizedGroup{
		ChatID:  -1001234567890,
		Title:   "old title",
		Enabled: false,
		Notes:   "keep-me",
	}
	fake := &fakeAuthorizedGroupWriter{existing: &existing}
	afterCreateCalls := 0

	rec := postCreateAuthorizedGroup(t, fake, `{"chat_id":-1001234567890,"title":"dup title"}`, func(context.Context, store.AuthorizedGroup) {
		afterCreateCalls++
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload["error"] != authorizedGroupAlreadyExistsMessage {
		t.Fatalf("error = %q, want %q", payload["error"], authorizedGroupAlreadyExistsMessage)
	}
	if !strings.Contains(payload["error"], "已存在") {
		t.Fatalf("error %q should mention 已存在 for frontend toast", payload["error"])
	}
	if len(fake.upsertCalls) != 0 {
		t.Fatalf("upsert called %d times, want 0 (must not overwrite enabled/notes)", len(fake.upsertCalls))
	}
	if afterCreateCalls != 0 {
		t.Fatalf("afterCreate called %d times, want 0", afterCreateCalls)
	}
	if existing.Enabled {
		t.Fatal("existing.enabled mutated to true")
	}
	if existing.Notes != "keep-me" {
		t.Fatalf("existing.notes = %q, want keep-me", existing.Notes)
	}
}

func TestCreateAuthorizedGroupPOSTCreatesNewGroup(t *testing.T) {
	fake := &fakeAuthorizedGroupWriter{}
	afterCreateCalls := 0

	rec := postCreateAuthorizedGroup(t, fake, `{"chat_id":-1009876543210,"title":"new group"}`, func(context.Context, store.AuthorizedGroup) {
		afterCreateCalls++
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if afterCreateCalls != 1 {
		t.Fatalf("afterCreate called %d times, want 1", afterCreateCalls)
	}
	if len(fake.upsertCalls) != 1 {
		t.Fatalf("upsert called %d times, want 1", len(fake.upsertCalls))
	}
	arg := fake.upsertCalls[0]
	if arg.ChatID != -1009876543210 {
		t.Fatalf("upsert chat_id = %d", arg.ChatID)
	}
	if arg.Title == nil || *arg.Title != "new group" {
		t.Fatalf("upsert title = %#v", arg.Title)
	}
	if arg.Enabled != nil {
		t.Fatalf("new-group POST should omit enabled, got %#v", arg.Enabled)
	}
	if arg.Notes != nil {
		t.Fatalf("new-group POST should omit notes, got %#v", arg.Notes)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	group, _ := body["group"].(map[string]any)
	if group == nil {
		t.Fatalf("missing group in %s", rec.Body.String())
	}
	if chatID, _ := group["chat_id"].(float64); chatID != -1009876543210 {
		t.Fatalf("group.chat_id = %#v", group["chat_id"])
	}
	if enabled, _ := group["enabled"].(bool); !enabled {
		t.Fatalf("new group enabled = %#v, want default true", group["enabled"])
	}
	if notes, _ := group["notes"].(string); notes != "" {
		t.Fatalf("new group notes = %#v, want empty", group["notes"])
	}
}

func TestCreateAuthorizedGroupIfAbsentSkipsUpsertWhenPresent(t *testing.T) {
	existing := store.AuthorizedGroup{
		ChatID:  99,
		Enabled: false,
		Notes:   "do-not-clear",
	}
	fake := &fakeAuthorizedGroupWriter{existing: &existing}
	title := "ignored"
	_, err := createAuthorizedGroupIfAbsent(context.Background(), fake, store.UpsertAuthorizedGroupParams{
		ChatID:  99,
		Title:   &title,
		Enabled: nil,
		Notes:   nil,
	})
	if !errors.Is(err, errAuthorizedGroupAlreadyExists) {
		t.Fatalf("error = %v, want %v", err, errAuthorizedGroupAlreadyExists)
	}
	if len(fake.upsertCalls) != 0 {
		t.Fatalf("upsert called on duplicate create: %+v", fake.upsertCalls)
	}
	if existing.Enabled || existing.Notes != "do-not-clear" {
		t.Fatalf("existing mutated: enabled=%v notes=%q", existing.Enabled, existing.Notes)
	}
}

func TestCreateAuthorizedGroupIfAbsentInsertsWhenMissing(t *testing.T) {
	fake := &fakeAuthorizedGroupWriter{}
	title := "fresh"
	created, err := createAuthorizedGroupIfAbsent(context.Background(), fake, store.UpsertAuthorizedGroupParams{
		ChatID: 100,
		Title:  &title,
	})
	if err != nil {
		t.Fatalf("create new group: %v", err)
	}
	if created.ChatID != 100 || created.Title != "fresh" || !created.Enabled || created.Notes != "" {
		t.Fatalf("created = %+v", created)
	}
	if len(fake.upsertCalls) != 1 {
		t.Fatalf("upsert called %d times, want 1", len(fake.upsertCalls))
	}
}
