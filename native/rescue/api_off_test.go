package api

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/openclaw/clawguard/internal/config"
    "go.uber.org/zap"
)

func TestRescueAssistantAPIsAbsentGuardRoutesRemain(t *testing.T) {
    s:=NewServer(config.Config{},zap.NewNop(),nil,nil)
    groupRoute,registryRoute,webhookRoute:=false,false,false
    for _,route:=range s.echo.Routes() {
        if strings.Contains(route.Path,"assistant") {t.Fatal("rescue exposed legacy assistant write APIs",route.Path)}
        if route.Path=="/api/admin/groups/:chat_id" {groupRoute=true}
        if route.Path=="/api/admin/llm/models" {registryRoute=true}
        if route.Path=="/webhook/:secret" {webhookRoute=true}
    }
    if !groupRoute || !registryRoute || !webhookRoute {t.Fatal("old moderation/model registry/webhook routes missing")}
    w:=httptest.NewRecorder();s.echo.ServeHTTP(w,httptest.NewRequest(http.MethodGet,"/healthz",nil))
    if w.Code!=200 {t.Fatal("old guard health endpoint failed",w.Code)}
}
