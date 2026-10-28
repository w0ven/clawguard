package bot

import (
	"context"
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

const adKillerImageTimeout = 12 * time.Second

type adKillerParentContextKey struct{}

// Continue only from a live caller, never from a cancelled parent. This gives
// a completed model chain its own bounded scoring/action budget after a local
// review deadline, without extending user cancellation or service shutdown.
func adKillerFallbackContext(ctx context.Context, cfg config.AdKillerPolicy) (context.Context, context.CancelFunc) {
	parent := ctx
	if ctx.Err() != nil {
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ctx, func() {}
		}
		p, ok := ctx.Value(adKillerParentContextKey{}).(context.Context)
		if !ok || p.Err() != nil {
			return ctx, func() {}
		}
		parent = p
	}
	timeout := time.Duration(cfg.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = adkiller.DefaultTimeout
	}
	return context.WithTimeout(parent, timeout+3*time.Second)
}

// Recheck global AI pause in addition to the regular action freeze gates.
func (s *Service) adKillerAllowed(ctx context.Context, chatID int64) bool {
	if ctx.Err() != nil || !s.automaticActionAllowed(ctx, chatID) {
		return false
	}
	state, err := s.GetSystemState(ctx)
	return err == nil && !state.AIPaused
}

func (s *Service) AdKillerConfigured() bool {
	return s != nil && s.adkiller != nil && s.adkiller.HasAPIKey()
}

func isAdKillerTextModel(model string) bool {
	return strings.EqualFold(model, "adkiller") || strings.EqualFold(model, "adkiller-text-fallback")
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
		Model:         "adkiller-text-fallback",
		PromptVersion: "",
		LatencyMs:     int(result.Latency.Milliseconds()),
		Action:        action,
	}
}

type adKillerEval struct {
	Succeeded bool
	Action    string
	Output    *ai.CheckOutput
	Result    adkiller.Result
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
		s.logger.Warn("adkiller enabled but api key is not configured",
			zap.Int64("chat_id", chatID),
			zap.Int("message_id", messageID),
		)
		return adKillerEval{}, nil
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
			s.logger.Warn("adkiller skipped because ai inflight limit reached",
				zap.Int64("chat_id", chatID),
				zap.Int("message_id", messageID),
			)
			return adKillerEval{}, nil
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
		return adKillerEval{}, nil
	}
	action := adkiller.ResolveAction(result.Score, adKillerBands(cfg))
	if action == adkiller.ActionNone {
		s.logger.Info("adkiller text fallback did not confirm ad",
			zap.Int64("chat_id", chatID),
			zap.Int("message_id", messageID),
			zap.Int("score", result.Score),
			zap.String("level", result.Level),
			zap.String("action", action),
		)
		return adKillerEval{Action: action, Result: result, Succeeded: true}, nil
	}
	output := adKillerCheckOutput(result, action)
	return adKillerEval{Action: action, Output: &output, Result: result, Succeeded: true}, nil
}

// Image scoring is OCR over the downloaded bytes, not visual classification.
func (s *Service) evaluateAdKillerImage(ctx context.Context, chatID, userID int64, messageID int, image []byte, caption string, cfg config.AdKillerPolicy) adKillerEval {
	if s == nil || !cfg.Enabled || !adkiller.ChatEnabled(cfg.EnabledChatIDs, chatID) || s.adkiller == nil || !s.adkiller.HasAPIKey() {
		return adKillerEval{}
	}
	callCtx, cancel := context.WithTimeout(ctx, adKillerImageTimeout)
	defer cancel()
	if s.aiModerator != nil {
		release, ok := s.aiModerator.TryAcquireInflight(chatID)
		if !ok {
			return adKillerEval{}
		}
		defer release()
	}
	result, err := s.adkiller.ScoreImage(callCtx, image, caption)
	if err != nil {
		s.logger.Warn("adkiller image scoring failed; continuing to vision", zap.Int64("chat_id", chatID), zap.Int("message_id", messageID), zap.Error(redact.Error(err)))
		return adKillerEval{}
	}
	if strings.TrimSpace(result.OCRText) == "" && strings.TrimSpace(caption) == "" {
		return adKillerEval{Result: result, Action: adkiller.ActionNone}
	}
	action := adkiller.ResolveAction(result.Score, adKillerBands(cfg))
	if action == adkiller.ActionNone {
		return adKillerEval{Result: result, Action: action}
	}
	output := adKillerCheckOutput(result, action)
	output.Model = "adkiller-image-ocr"
	output.Verdict.Reason = fmt.Sprintf("AdKiller 图片文字 OCR 判定广告（score=%d, action=%s）", result.Score, adkiller.ActionLabel(action))
	return adKillerEval{Action: action, Output: &output, Result: result}
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
		if eval.Succeeded {
			s.enqueueProfileCheckLog(chatID, user, bio, logMode, "pass", nil, nil, nil)
		}
		return "", nil, eval.Succeeded, nil
	}
	matched := "adkiller:" + eval.Output.Verdict.Category
	conf := float32(eval.Output.Verdict.Confidence)
	verdict := eval.Output.Verdict.Verdict
	s.enqueueProfileCheckLog(chatID, user, bio, logMode, "hit", &matched, &conf, &verdict)
	return matched, eval.Output, true, nil
}
