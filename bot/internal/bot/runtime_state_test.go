package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

type unavailableRuntimeDB struct{}

func (unavailableRuntimeDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("database unavailable")
}

func (unavailableRuntimeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("database unavailable")
}

func (unavailableRuntimeDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return unavailableRuntimeRow{}
}

type unavailableRuntimeRow struct{}

func (unavailableRuntimeRow) Scan(...any) error {
	return errors.New("database unavailable")
}

func TestRuntimeStateUsesLastKnownGoodSnapshots(t *testing.T) {
	svc := &Service{queries: store.New(unavailableRuntimeDB{}), logger: zap.NewNop()}
	svc.authorizedGroups.Store(int64(100), true)
	svc.systemState.Store(store.SystemState{ActionsPaused: true, UpdatedAt: time.Now()})
	policy := config.DefaultPolicy
	policy.JoinProtection.JoinThreshold = 42
	svc.policySnapshots.Store(int64(100), policy)

	authorized, err := svc.IsAuthorizedGroup(context.Background(), 100)
	if err != nil || !authorized {
		t.Fatalf("authorized=%t err=%v", authorized, err)
	}
	state, err := svc.GetSystemState(context.Background())
	if err != nil || !state.ActionsPaused {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	loaded, err := svc.LoadGuardPolicy(context.Background(), 100)
	if err != nil || loaded.JoinProtection.JoinThreshold != 42 {
		t.Fatalf("policy threshold=%d err=%v", loaded.JoinProtection.JoinThreshold, err)
	}
}

func TestVerificationFailActionSnapshotIsStable(t *testing.T) {
	raw, err := withVerificationFailActionSnapshot([]byte(`{"answer":7}`), "ban")
	if err != nil {
		t.Fatal(err)
	}
	if got := verificationFailActionSnapshot(raw, "kick"); got != "ban" {
		t.Fatalf("snapshot action = %q, want ban", got)
	}
	if got := verificationFailActionSnapshot([]byte(`{"answer":7}`), "mute_permanent"); got != "mute_permanent" {
		t.Fatalf("legacy fallback action = %q, want mute_permanent", got)
	}
}
