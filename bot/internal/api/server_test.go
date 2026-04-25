package api

import (
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
