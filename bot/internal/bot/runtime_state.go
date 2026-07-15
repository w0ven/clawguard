package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

const (
	authorizedGroupCacheTTL      = 60 * time.Second
	authorizedGroupLocalCacheTTL = 5 * time.Second
	systemStateCacheTTL          = 5 * time.Second
	systemStateLocalCacheTTL     = time.Second
	guardPolicySnapshotTTL       = 30 * time.Second
)

type authorizedGroupCacheEntry struct {
	Authorized bool `json:"authorized"`
}

func (s *Service) authorizedGroupCacheKey(chatID int64) string {
	return fmt.Sprintf("clawguard:authorized-group:%d", chatID)
}

func (s *Service) systemStateCacheKey() string {
	return "clawguard:system-state"
}

func (s *Service) IsAuthorizedGroup(ctx context.Context, chatID int64) (bool, error) {
	if chatID == 0 {
		return false, nil
	}
	if cached, ok := s.authorizedGroups.Load(chatID); ok {
		if cachedAt, timeOK := s.authorizedGroupAt.Load(chatID); timeOK && time.Since(cachedAt.(time.Time)) < authorizedGroupLocalCacheTTL {
			return cached.(bool), nil
		}
	}
	if s.redis != nil {
		raw, err := s.redis.Get(ctx, s.authorizedGroupCacheKey(chatID)).Result()
		if err == nil {
			var cached authorizedGroupCacheEntry
			if jsonErr := json.Unmarshal([]byte(raw), &cached); jsonErr == nil {
				s.authorizedGroups.Store(chatID, cached.Authorized)
				s.authorizedGroupAt.Store(chatID, time.Now())
				return cached.Authorized, nil
			}
		}
	}

	group, err := s.queries.GetAuthorizedGroupByChatID(ctx, chatID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.cacheAuthorizedGroup(ctx, chatID, false)
			return false, nil
		}
		if cached, ok := s.authorizedGroups.Load(chatID); ok {
			s.warnRuntimeSnapshotFallback("authorized_group", chatID, err)
			s.authorizedGroupAt.Store(chatID, time.Now())
			return cached.(bool), nil
		}
		return false, err
	}

	authorized := group.Enabled
	s.cacheAuthorizedGroup(ctx, chatID, authorized)
	return authorized, nil
}

func (s *Service) cacheAuthorizedGroup(ctx context.Context, chatID int64, authorized bool) {
	s.authorizedGroups.Store(chatID, authorized)
	s.authorizedGroupAt.Store(chatID, time.Now())
	if s.redis == nil {
		return
	}
	raw, err := json.Marshal(authorizedGroupCacheEntry{Authorized: authorized})
	if err != nil {
		return
	}
	_ = s.redis.Set(ctx, s.authorizedGroupCacheKey(chatID), raw, authorizedGroupCacheTTL).Err()
}

func (s *Service) SetAuthorizedGroupCache(ctx context.Context, chatID int64, authorized bool) {
	if chatID == 0 {
		return
	}
	s.cacheAuthorizedGroup(ctx, chatID, authorized)
}

func (s *Service) InvalidateAuthorizedGroupCache(ctx context.Context, chatID int64) {
	if chatID == 0 {
		return
	}
	s.authorizedGroups.Delete(chatID)
	s.authorizedGroupAt.Delete(chatID)
	if s.redis != nil {
		_ = s.redis.Del(ctx, s.authorizedGroupCacheKey(chatID)).Err()
	}
}

func (s *Service) GetSystemState(ctx context.Context) (store.SystemState, error) {
	if cached := s.systemState.Load(); cached != nil {
		cachedAt := s.systemStateAt.Load()
		if cachedAt > 0 && time.Since(time.Unix(0, cachedAt)) < systemStateLocalCacheTTL {
			return cached.(store.SystemState), nil
		}
	}
	if s.redis != nil {
		raw, err := s.redis.Get(ctx, s.systemStateCacheKey()).Result()
		if err == nil {
			var cached store.SystemState
			if jsonErr := json.Unmarshal([]byte(raw), &cached); jsonErr == nil {
				s.systemState.Store(cached)
				s.systemStateAt.Store(time.Now().UnixNano())
				return cached, nil
			}
		}
	}

	state, err := s.queries.GetSystemState(ctx)
	if err != nil {
		if cached := s.systemState.Load(); cached != nil {
			s.warnRuntimeSnapshotFallback("system_state", 0, err)
			s.systemStateAt.Store(time.Now().UnixNano())
			return cached.(store.SystemState), nil
		}
		return store.SystemState{}, err
	}
	s.cacheSystemState(ctx, state)
	return state, nil
}

func (s *Service) cacheSystemState(ctx context.Context, state store.SystemState) {
	s.systemState.Store(state)
	s.systemStateAt.Store(time.Now().UnixNano())
	if s.redis == nil {
		return
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = s.redis.Set(ctx, s.systemStateCacheKey(), raw, systemStateCacheTTL).Err()
}

func (s *Service) LoadGuardPolicy(ctx context.Context, chatID int64) (config.GuardPolicy, error) {
	if cached, ok := s.policySnapshots.Load(chatID); ok {
		if cachedAt, timeOK := s.policySnapshotAt.Load(chatID); timeOK && time.Since(cachedAt.(time.Time)) < guardPolicySnapshotTTL {
			return cached.(config.GuardPolicy), nil
		}
	}
	policy, err := config.LoadPolicy(ctx, s.queries, chatID)
	if err == nil {
		s.policySnapshots.Store(chatID, policy)
		s.policySnapshotAt.Store(chatID, time.Now())
		return policy, nil
	}
	if cached, ok := s.policySnapshots.Load(chatID); ok {
		s.warnRuntimeSnapshotFallback("guard_policy", chatID, err)
		s.policySnapshotAt.Store(chatID, time.Now())
		return cached.(config.GuardPolicy), nil
	}
	return config.GuardPolicy{}, err
}

func (s *Service) RefreshGuardPolicySnapshot(ctx context.Context, chatID int64) (config.GuardPolicy, error) {
	policy, err := config.LoadPolicy(ctx, s.queries, chatID)
	if err != nil {
		return config.GuardPolicy{}, err
	}
	s.policySnapshots.Store(chatID, policy)
	s.policySnapshotAt.Store(chatID, time.Now())
	return policy, nil
}

func (s *Service) RefreshAllGuardPolicySnapshots(ctx context.Context) error {
	groups, err := s.queries.ListAuthorizedGroups(ctx)
	if err != nil {
		return fmt.Errorf("list authorized groups: %w", err)
	}
	for _, group := range groups {
		s.cacheAuthorizedGroup(ctx, group.ChatID, group.Enabled)
		if !group.Enabled {
			continue
		}
		if _, err := s.RefreshGuardPolicySnapshot(ctx, group.ChatID); err != nil {
			return fmt.Errorf("load policy for chat %d: %w", group.ChatID, err)
		}
	}
	return nil
}

func (s *Service) PrimeRuntimeSnapshots(ctx context.Context) error {
	if err := s.RefreshAllGuardPolicySnapshots(ctx); err != nil {
		return err
	}
	state, err := s.queries.GetSystemState(ctx)
	if err != nil {
		return fmt.Errorf("load system state: %w", err)
	}
	s.cacheSystemState(ctx, state)
	return nil
}

func (s *Service) warnRuntimeSnapshotFallback(kind string, chatID int64, err error) {
	if s.logger == nil {
		return
	}
	fields := []zap.Field{
		zap.String("event", "runtime_snapshot_fallback"),
		zap.String("snapshot", kind),
		zap.Error(err),
	}
	if chatID != 0 {
		fields = append(fields, zap.Int64("chat_id", chatID))
	}
	s.logger.Warn("runtime dependency unavailable; using last known good snapshot", fields...)
}

func (s *Service) UpdateSystemState(ctx context.Context, params store.UpdateSystemStateParams) (store.SystemState, error) {
	state, err := s.queries.UpdateSystemState(ctx, params)
	if err != nil {
		return store.SystemState{}, err
	}
	s.cacheSystemState(ctx, state)
	return state, nil
}

func (s *Service) InvalidateSystemStateCache(ctx context.Context) {
	if s.redis == nil {
		return
	}
	_ = s.redis.Del(ctx, s.systemStateCacheKey()).Err()
}

func (s *Service) WriteRuntimeAudit(ctx context.Context, scope string, chatID *int64, action string, before, after any) {
	s.WriteRuntimeAuditWithAdmin(ctx, scope, chatID, 0, action, before, after)
}

func (s *Service) WriteRuntimeAuditWithAdmin(ctx context.Context, scope string, chatID *int64, adminID int64, action string, before, after any) {
	beforeRaw := mustJSONBytes(before)
	afterRaw := mustJSONBytes(after)
	diff := action
	if _, err := s.queries.InsertAuditEntry(ctx, store.InsertAuditEntryParams{
		Scope:   scope,
		ChatID:  chatID,
		AdminID: adminID,
		Action:  action,
		Before:  beforeRaw,
		After:   afterRaw,
		Diff:    &diff,
	}); err != nil {
		s.logger.Warn("write runtime audit failed", zap.Error(err), zap.String("scope", scope), zap.String("action", action), zap.Int64("admin_id", adminID))
	}
}

func mustJSONBytes(v any) []byte {
	if v == nil {
		return []byte("null")
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte(`"marshal_failed"`)
	}
	return raw
}
