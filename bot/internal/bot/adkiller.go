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

func adKillerBands(cfg config.AdKillerPolicy) []adkiller.ScoreBand {
	bands := make([]adkiller.ScoreBand, 0, len(cfg.ScoreBands))
	for _, band := range cfg.ScoreBands {
		bands = append(bands, adkiller.ScoreBand{
			MinScore: band.MinScore,
			MaxScore: band.MaxScore,
			Action:   band.Action,
		})
	}
	return bands
}

func adKillerCheckOutput(result adkiller.Result, action string) ai.CheckOutput {
	return ai.CheckOutput{
		Verdict: ai.Verdict{
			Verdict:    "ad",
			Confidence: adkiller.Confidence(result.Score),
			Category:   adkiller.MapCategory(result.PrimaryCategory),
			Reason:     fmt.Sprintf("AdKiller 判定广告（score=%d, category=%s, action=%s）", result.Score, result.PrimaryCategory, adkiller.ActionLabel(action)),
		},
		Model:         "adkiller",
		PromptVersion: "adkiller-v1",
		LatencyMs:     int(result.Latency.Milliseconds()),
		Action:        action,
	}
}

type adKillerEval struct {
	Action  string
	Output  *ai.CheckOutput
	Result  adkiller.Result
	SkipLLM bool
}

func (s *Service) evaluateAdKiller(
	ctx context.Context,
	chatID, userID int64,
	messageID int,
	text string,
	cfg config.AdKillerPolicy,
) (adKillerEval, error) {
	if s == nil || !cfg.Enabled || !adkiller.ChatEnabled(cfg.EnabledChatIDs, chatID) {
		return adKillerEval{}, nil
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return adKillerEval{}, nil
	}
	if s.adkiller == nil || !s.adkiller.HasAPIKey() {
		s.logger.Warn("adkiller enabled but api key is not configured, falling back to llm",
			zap.Int64("chat_id", chatID),
			zap.Int("message_id", messageID),
		)
		skip, err := s.finishAdKillerFailure(cfg, "api_key_missing")
		return adKillerEval{SkipLLM: skip}, err
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
		release, ok = s.aiModerator.TryAcquireInflight(chatID)
		if !ok {
			s.logger.Warn("adkiller skipped because ai inflight limit reached, falling back to llm",
				zap.Int64("chat_id", chatID),
				zap.Int("message_id", messageID),
			)
			skip, err := s.finishAdKillerFailure(cfg, "inflight_limit")
			return adKillerEval{SkipLLM: skip}, err
		}
	}
	if release != nil {
		defer release()
	}

	result, err := s.adkiller.Score(callCtx, text)
	if err != nil {
		s.logger.Warn("adkiller score failed",
			zap.Error(redact.Error(err)),
			zap.Int64("chat_id", chatID),
			zap.Int64("user_id", userID),
			zap.Int("message_id", messageID),
			zap.Int("retry_after", result.RetryAfter),
		)
		skip, ferr := s.finishAdKillerFailure(cfg, "request_failed")
		return adKillerEval{SkipLLM: skip}, ferr
	}
	action := adkiller.ResolveAction(result.Score, adKillerBands(cfg))
	if action == adkiller.ActionNone {
		s.logger.Info("adkiller did not confirm ad, continuing to llm",
			zap.Int64("chat_id", chatID),
			zap.Int("message_id", messageID),
			zap.Int("score", result.Score),
			zap.String("level", result.Level),
			zap.String("action", action),
		)
		return adKillerEval{Action: action, Result: result}, nil
	}
	output := adKillerCheckOutput(result, action)
	return adKillerEval{Action: action, Output: &output, Result: result, SkipLLM: true}, nil
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
	eval, err := s.evaluateAdKiller(ctx, msg.Chat.ID, msg.Sender.ID, msg.ID, content.Text, cfg)
	if err != nil {
		return false, err
	}
	if eval.Output == nil || eval.Action == "" || eval.Action == adkiller.ActionNone {
		return eval.SkipLLM, nil
	}

	metadata, _ := json.Marshal(map[string]any{
		"source":           "adkiller",
		"score":            eval.Result.Score,
		"level":            eval.Result.Level,
		"primary_category": eval.Result.PrimaryCategory,
		"dimensions":       eval.Result.Dimensions,
		"truncated":        eval.Result.Truncated,
		"min_score":        cfg.MinScore,
		"timeout_ms":       cfg.TimeoutMs,
		"rate_remaining":   eval.Result.RateRemaining,
		"action":           eval.Action,
		"score_bands":      cfg.ScoreBands,
	})
	if err := s.recordAIDecision(ctx, msg, content.Text, *eval.Output, eval.Action, "message", metadata); err != nil {
		s.logger.Warn("record adkiller decision failed", zap.Error(err))
	}
	if err := s.applyAIAction(ctx, msg, policy, trust, *eval.Output, eval.Action, isEdited); err != nil {
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

func (s *Service) checkBioAdKiller(
	ctx context.Context,
	chatID int64,
	user *tele.User,
	bio string,
	policy config.GuardPolicy,
	logMode string,
) (string, *ai.CheckOutput, bool, error) {
	var userID int64
	if user != nil {
		userID = user.ID
	}
	eval, err := s.evaluateAdKiller(ctx, chatID, userID, 0, bio, policy.AI.AdKiller)
	if err != nil {
		return "", nil, false, err
	}
	if eval.Output == nil || eval.Action == "" || eval.Action == adkiller.ActionNone {
		return "", nil, eval.SkipLLM, nil
	}
	matched := "adkiller:" + eval.Output.Verdict.Category
	conf := float32(eval.Output.Verdict.Confidence)
	verdict := eval.Output.Verdict.Verdict
	s.enqueueProfileCheckLog(chatID, user, bio, logMode, "hit", &matched, &conf, &verdict)
	return matched, eval.Output, true, nil
}
