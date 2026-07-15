//go:build integration

package store

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestProductionMigrationsExposeRequiredOperationsSchema(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("TEST_DATABASE_URL is required")
	}

	conn, err := pgx.Connect(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("connect database: %v", err)
	}
	defer conn.Close(context.Background())

	var version int64
	if err := conn.QueryRow(context.Background(), `
		SELECT COALESCE(MAX(version_id), 0)
		FROM goose_db_version
		WHERE is_applied = TRUE
	`).Scan(&version); err != nil {
		t.Fatalf("read migration version: %v", err)
	}
	if version < 28 {
		t.Fatalf("migration version = %d, want at least 28", version)
	}

	for _, index := range []string{
		"pending_verifications_chat_expires_idx",
		"idx_pending_verifications_due",
	} {
		var exists bool
		if err := conn.QueryRow(context.Background(), `
			SELECT to_regclass('public.' || $1) IS NOT NULL
		`, index).Scan(&exists); err != nil {
			t.Fatalf("check index %s: %v", index, err)
		}
		if !exists {
			t.Fatalf("required index %s is missing", index)
		}
	}
}
