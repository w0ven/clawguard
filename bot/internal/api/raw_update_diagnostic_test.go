package api

import (
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/openclaw/clawguard/internal/config"
)

func TestRawTelegramUpdateDiagnosticSummaryTargetsForwardRing(t *testing.T) {
	raw := []byte(`{"update_id":7,"message":{"message_id":42,"text":"💍","forward_origin":{"type":"user","sender_user":{"id":1,"first_name":"Dexter"},"date":1710000000},"photo":[{"file_id":"p"}]}}`)

	summary, ok := rawTelegramUpdateDiagnosticSummary(raw)
	if !ok {
		t.Fatal("summary ok = false, want true")
	}
	if summary["has_forward_origin"] != true {
		t.Fatalf("has_forward_origin = %v, want true", summary["has_forward_origin"])
	}
	if summary["has_photo"] != true {
		t.Fatalf("has_photo = %v, want true", summary["has_photo"])
	}
	if summary["matched_ring"] != true {
		t.Fatalf("matched_ring = %v, want true", summary["matched_ring"])
	}
}

func TestRawTelegramUpdateDiagnosticSummaryTargetsShortContext(t *testing.T) {
	raw := []byte(`{"update_id":8,"message":{"message_id":43,"text":"ok","external_reply":{"origin":{"type":"channel","chat":{"id":-100,"title":"Ads"},"message_id":5},"link_preview_options":{"url":"https://example.test"}}}}`)

	summary, ok := rawTelegramUpdateDiagnosticSummary(raw)
	if !ok {
		t.Fatal("summary ok = false, want true")
	}
	if summary["has_external_reply"] != true {
		t.Fatalf("has_external_reply = %v, want true", summary["has_external_reply"])
	}
	if summary["matched_short_context"] != true {
		t.Fatalf("matched_short_context = %v, want true", summary["matched_short_context"])
	}
	externalSummary, ok := summary["external_reply_summary"].(map[string]bool)
	if !ok {
		t.Fatalf("external_reply_summary type = %T, want map[string]bool", summary["external_reply_summary"])
	}
	if !externalSummary["has_link_preview_options"] {
		t.Fatalf("external_reply_summary has_link_preview_options = false, want true")
	}
}

func TestRawTelegramUpdateDiagnosticSummarySkipsOrdinaryMessage(t *testing.T) {
	raw := []byte(`{"update_id":9,"message":{"message_id":44,"text":"ordinary chat message with no target fields"}}`)

	if _, ok := rawTelegramUpdateDiagnosticSummary(raw); ok {
		t.Fatal("summary ok = true, want false")
	}
}

func TestLogRawTelegramUpdateStoresSummaryOnly(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	server := &Server{
		cfg:    config.Config{LogRawUpdates: true},
		logger: zap.New(core),
	}
	raw := []byte(`{"update_id":10,"message":{"message_id":45,"text":"💍 private message","forward_sender_name":"Private Sender"}}`)

	server.logRawTelegramUpdateIfTarget(raw)

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("log entry count = %d, want 1", len(entries))
	}
	fields := entries[0].ContextMap()
	if _, ok := fields["summary"]; !ok {
		t.Fatal("summary field missing")
	}
	if _, ok := fields["raw_update_json"]; ok {
		t.Fatal("raw_update_json must not be logged")
	}
}
