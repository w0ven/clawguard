package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/config"
)

func TestHTTPServerTimeoutsConfigured(t *testing.T) {
	server := NewServer(config.Config{HTTPPort: 8080}, zap.NewNop(), nil, nil)
	httpServer := server.httpServer()

	if httpServer.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s, want 10s", httpServer.ReadHeaderTimeout)
	}
	if httpServer.ReadTimeout != 30*time.Second {
		t.Fatalf("ReadTimeout = %s, want 30s", httpServer.ReadTimeout)
	}
	if httpServer.WriteTimeout != 60*time.Second {
		t.Fatalf("WriteTimeout = %s, want 60s", httpServer.WriteTimeout)
	}
	if httpServer.IdleTimeout != 120*time.Second {
		t.Fatalf("IdleTimeout = %s, want 120s", httpServer.IdleTimeout)
	}
	if httpServer.MaxHeaderBytes != 1<<20 {
		t.Fatalf("MaxHeaderBytes = %d, want %d", httpServer.MaxHeaderBytes, 1<<20)
	}
}

func TestHealthEndpointsHaveDistinctLivenessAndReadinessSemantics(t *testing.T) {
	server := NewServer(config.Config{HTTPPort: 8080}, zap.NewNop(), nil, nil)

	healthRequest := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthRecorder := httptest.NewRecorder()
	server.echo.ServeHTTP(healthRecorder, healthRequest)
	if healthRecorder.Code != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, want %d", healthRecorder.Code, http.StatusOK)
	}

	readyRequest := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	readyRecorder := httptest.NewRecorder()
	server.echo.ServeHTTP(readyRecorder, readyRequest)
	if readyRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz status = %d, want %d", readyRecorder.Code, http.StatusServiceUnavailable)
	}
	var body map[string]any
	if err := json.Unmarshal(readyRecorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode readiness body: %v", err)
	}
	if body["status"] != "not_ready" {
		t.Fatalf("GET /readyz status body = %v, want not_ready", body["status"])
	}
}
