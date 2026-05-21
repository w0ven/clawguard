package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/store"
)

const (
	authorizedGroupCacheTTL = 60 * time.Second
	systemStateCacheTTL     = 5 * time.Second
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
	if s.redis != nil {
		raw, err := s.redis.Get(ctx, s.authorizedGroupCacheKey(chatID)).Result()
		if err == nil {
			var cached authorizedGroupCacheEntry
			if jsonErr := json.Unmarshal([]byte(raw), &cached); jsonErr == nil {
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
		return false, err
	}

	authorized := group.Enabled
	s.cacheAuthorizedGroup(ctx, chatID, authorized)
	return authorized, nil
}

func (s *Service) cacheAuthorizedGroup(ctx context.Context, chatID int64, authorized bool) {
	if s.redis == nil {
		return
	}
	raw, err := json.Marshal(authorizedGroupCacheEntry{Authorized: authorized})
	if err != nil {
		return
	}
	_ = s.redis.Set(ctx, s.authorizedGroupCacheKey(chatID), raw, authorizedGroupCacheTTL).Err()
}

func (s *Service) InvalidateAuthorizedGroupCache(ctx context.Context, chatID int64) {
	if s.redis == nil || chatID == 0 {
		return
	}
	_ = s.redis.Del(ctx, s.authorizedGroupCacheKey(chatID)).Err()
}

func (s *Service) GetSystemState(ctx context.Context) (store.SystemState, error) {
	if s.redis != nil {
		raw, err := s.redis.Get(ctx, s.systemStateCacheKey()).Result()
		if err == nil {
			var cached store.SystemState
			if jsonErr := json.Unmarshal([]byte(raw), &cached); jsonErr == nil {
				return cached, nil
			}
		}
	}

	state, err := s.queries.GetSystemState(ctx)
	if err != nil {
		return store.SystemState{}, err
	}
	s.cacheSystemState(ctx, state)
	return state, nil
}

func (s *Service) cacheSystemState(ctx context.Context, state store.SystemState) {
	if s.redis == nil {
		return
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = s.redis.Set(ctx, s.systemStateCacheKey(), raw, systemStateCacheTTL).Err()
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
