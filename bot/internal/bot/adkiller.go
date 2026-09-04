package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/adkiller"
	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/redact"
	"github.com/openclaw/clawguard/internal/store"
)

func (s *Service) ReloadAdKillerSecret(ctx context.Context) error {
	if s == nil || s.adkiller == nil {
		return nil
	}
	if s.queries == nil {
		s.adkiller.SetAPIKey("")
		return nil
	}
	secret, err := s.queries.GetAdkillerSecret(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.adkiller.SetAPIKey("")
			return nil
		}
		return fmt.Errorf("load adkiller secret: %w", err)
	}
	enc := strings.TrimSpace(secret.ApiKeyEnc)
	if enc == "" {
		s.adkiller.SetAPIKey("")
		return nil
	}
	plain, err := ai.DecryptAPIKey(enc)
	if err != nil {
		s.adkiller.SetAPIKey("")
		return fmt.Errorf("decrypt adkiller api key: %w", err)
	}
	s.adkiller.SetAPIKey(plain)
	return nil
}

func (s *Service) AdKillerConfigured() bool {
	return s != nil && s.adkiller != nil && s.adkiller.HasAPIKey()
}

func (s *Service) applyAdKillerPrefilter(
	ctx context.Context,
	msg *tele.Message,
	policy config.GuardPolicy,
	trust store.UserTrust,
	content reviewableContent,
	isEdited bool,
) (bool, error) {
	if s == nil || msg == nil || msg.Chat == nil || msg.Sender == nil {
		return false, nil
	}
	cfg := policy.AI.AdKiller
	if !cfg.Enabled || !adkiller.ChatEnabled(cfg.EnabledChatIDs, msg.Chat.ID) {
		return false, nil
	}
	text := strings.TrimSpace(content.Text)
	if text == "" {
		return false, nil
	}
	if s.adkiller == nil || !s.adkiller.HasAPIKey() {
		s.logger.Warn("adkiller enabled but api key is not configured, falling back to llm",
			zap.Int64("chat_id", msg.Chat.ID),
			zap.Int("message_id", msg.ID),
		)
		return s.finishAdKillerFailure(cfg, "api_key_missing")
	}

	timeout := time.Duration(cfg.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = adkiller.DefaultTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var release func()
	if s.aiModerator != nil {
		var ok bool
		release, ok = s.aiModerator.TryAcquireInflight(msg.Chat.ID)
		if !ok {
			s.logger.Warn("adkiller skipped because ai inflight limit reached, falling back to llm",
				zap.Int64("chat_id", msg.Chat.ID),
				zap.Int("message_id", msg.ID),
			)
			return s.finishAdKillerFailure(cfg, "inflight_limit")
		}
	}
	if release != nil {
		defer release()
	}

	result, err := s.adkiller.Score(callCtx, text)
	if err != nil {
		s.logger.Warn("adkiller score failed",
			zap.Error(redact.Error(err)),
			zap.Int64("chat_id", msg.Chat.ID),
			zap.Int64("user_id", msg.Sender.ID),
			zap.Int("message_id", msg.ID),
			zap.Int("retry_after", result.RetryAfter),
		)
		return s.finishAdKillerFailure(cfg, "request_failed")
	}
	bands := make([]adkiller.ScoreBand, 0, len(cfg.ScoreBands))
	for _, band := range cfg.ScoreBands {
		bands = append(bands, adkiller.ScoreBand{
			MinScore: band.MinScore,
			MaxScore: band.MaxScore,
			Action:   band.Action,
		})
	}
	action := adkiller.ResolveAction(result.Score, bands)
	if action == adkiller.ActionNone {
		s.logger.Info("adkiller did not confirm ad, continuing to llm",
			zap.Int64("chat_id", msg.Chat.ID),
			zap.Int("message_id", msg.ID),
			zap.Int("score", result.Score),
			zap.String("level", result.Level),
			zap.String("action", action),
		)
		return false, nil
	}

	category := adkiller.MapCategory(result.PrimaryCategory)
	output := ai.CheckOutput{
		Verdict: ai.Verdict{
			Verdict:    "ad",
			Confidence: adkiller.Confidence(result.Score),
			Category:   category,
			Reason:     fmt.Sprintf("AdKiller 判定广告（score=%d, category=%s, action=%s）", result.Score, result.PrimaryCategory, adkiller.ActionLabel(action)),
		},
		Model:         "adkiller",
		PromptVersion: "adkiller-v1",
		LatencyMs:     int(result.Latency.Milliseconds()),
	}
	metadata, _ := json.Marshal(map[string]any{
		"source":           "adkiller",
		"score":            result.Score,
		"level":            result.Level,
		"primary_category": result.PrimaryCategory,
		"dimensions":       result.Dimensions,
		"truncated":        result.Truncated,
		"min_score":        cfg.MinScore,
		"timeout_ms":       cfg.TimeoutMs,
		"rate_remaining":   result.RateRemaining,
		"action":           action,
		"score_bands":      cfg.ScoreBands,
	})
	if err := s.recordAIDecision(ctx, msg, content.Text, output, action, "message", metadata); err != nil {
		s.logger.Warn("record adkiller decision failed", zap.Error(err))
	}
	if err := s.applyAIAction(ctx, msg, policy, trust, output, action, isEdited); err != nil {
		return true, err
	}
	return true, nil
}

func (s *Service) finishAdKillerFailure(cfg config.AdKillerPolicy, reason string) (bool, error) {
	if strings.EqualFold(strings.TrimSpace(cfg.OnFailure), adkiller.OnFailureSkip) {
		s.logger.Info("adkiller on_failure=skip, not calling llm", zap.String("reason", reason))
		return true, nil
	}
	return false, nil
}
