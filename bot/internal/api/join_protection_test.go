package api

import (
	"testing"

	"github.com/openclaw/clawguard/internal/store"
)

func TestValidateJoinProtectionConfigDocument(t *testing.T) {
	valid := []byte(`{"join_protection":{"enabled":true,"join_threshold":20,"join_window_seconds":60,"protection_duration_seconds":900,"temporary_ban_seconds":3600,"admin_notify_interval_seconds":300,"max_pending_verifications":30,"telegram_failure_cooldown_seconds":300}}`)
	if err := validateJoinProtectionConfigDocument(valid); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for _, invalid := range [][]byte{
		[]byte(`{"join_protection":{"join_threshold":0}}`),
		[]byte(`{"join_protection":{"join_threshold":1001}}`),
		[]byte(`{"join_protection":{"unknown":1}}`),
	} {
		if err := validateJoinProtectionConfigDocument(invalid); err == nil {
			t.Fatalf("invalid config accepted: %s", invalid)
		}
	}
}

func TestJoinProtectionGroupScope(t *testing.T) {
	owner := store.Admin{Role: "owner", GroupScope: []byte(`[1]`)}
	if !adminCanAccessChat(owner, 999) {
		t.Fatal("owner should access every group")
	}
	admin := store.Admin{Role: "admin", GroupScope: []byte(`[100,200]`)}
	if !adminCanAccessChat(admin, 100) {
		t.Fatal("scoped admin should access allowed group")
	}
	if adminCanAccessChat(admin, 300) {
		t.Fatal("scoped admin accessed another group")
	}
}

func TestPreserveCurrentJoinProtection(t *testing.T) {
	current := []byte(`{"verify":{"enabled":true},"join_protection":{"enabled":true,"join_threshold":20}}`)
	next := []byte(`{"verify":{"enabled":false},"join_protection":{"enabled":false,"join_threshold":999}}`)
	merged, err := preserveCurrentJoinProtection(current, next)
	if err != nil {
		t.Fatal(err)
	}
	if string(merged) != `{"join_protection":{"enabled":true,"join_threshold":20},"verify":{"enabled":false}}` {
		t.Fatalf("merged = %s", merged)
	}
}
