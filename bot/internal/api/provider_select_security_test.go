//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"go.uber.org/zap"
)

type selectProviders struct{}

func (selectProviders) GetByKey(string) (ai.Provider, bool) { return ai.Provider{}, false }
func (selectProviders) Client(string) (ai.LLMClient, bool)  { return nil, false }
func (selectProviders) Reload(context.Context) error        { return nil }
func (selectProviders) List() []ai.Provider {
	return []ai.Provider{{Key: "example", Label: "Example", BaseURL: "https://example.invalid/?api_key=fixture-secret", ExtraHeaders: map[string]string{"X-API-Key": "fixture-secret"}}}
}

type selectModels struct{}

func (selectModels) Get(ai.ModelRef) (ai.Model, bool) { return ai.Model{}, false }
func (selectModels) Reload(context.Context) error     { return nil }
func (selectModels) List(ai.ModelFilter) []ai.Model {
	return []ai.Model{{ProviderKey: "example", ModelKey: "model", Label: "Model"}}
}
func TestModelSelectorNeverExposesProviderSecrets(t *testing.T) {
	fx := openH1Fixture(t)
	old := http.DefaultTransport
	http.DefaultTransport = exemptTelegramTransport{}
	t.Cleanup(func() { http.DefaultTransport = old })
	svc, err := bot.New(context.Background(), config.Config{BotToken: "123:TEST"}, zap.NewNop(), fx.queries, nil, selectProviders{}, selectModels{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	srv := NewServer(config.Config{}, zap.NewNop(), svc, nil)
	w := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest("GET", "/api/admin/ai-providers", nil), w)
	if err = srv.handleListAIModels(c); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), "fixture-secret") || strings.Contains(w.Body.String(), "extra_headers") || strings.Contains(w.Body.String(), "base_url") || !strings.Contains(w.Body.String(), `"key":"model"`) {
		t.Fatalf("unsafe or unusable response: %s", w.Body.String())
	}
}
