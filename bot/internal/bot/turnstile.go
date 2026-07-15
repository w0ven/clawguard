package bot

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

const (
	turnstileSiteverifyURLDefault = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	turnstileSiteverifyTimeout    = 10 * time.Second
	turnstileIdempotencyTTL       = 60 * time.Second // seconds: Cloudflare idempotency key reuse window per cf_response.
)

var (
	turnstileSiteverifyURL    = turnstileSiteverifyURLDefault
	turnstileSiteverifyClient = &http.Client{
		Timeout: turnstileSiteverifyTimeout,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			MaxConnsPerHost:       50,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}

	ErrTurnstileNotConfigured = errors.New("turnstile not configured")
	ErrTurnstileTokenNotFound = errors.New("turnstile token not found")
	ErrTurnstileVerifyFailed  = errors.New("turnstile verification failed")
)

type turnstilePendingPayload struct {
	Token string `json:"token"`
}

type turnstileSiteVerifyResponse struct {
	Success    bool     `json:"success"`
	ErrorCodes []string `json:"error-codes"`
}

func (s *Service) startTurnstileVerification(ctx context.Context, chat *tele.Chat, user *tele.User, policy config.GuardPolicy) error {
	if strings.TrimSpace(s.cfg.TurnstileSiteKey) == "" || strings.TrimSpace(s.cfg.TurnstileSecret) == "" {
		s.logger.Warn("turnstile keys missing, fallback to button", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return s.startButtonVerification(ctx, chat, user, policy)
	}

	token, err := generateVerificationToken()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(turnstilePendingPayload{Token: token})
	if err != nil {
		return err
	}

	verifyURL := strings.TrimRight(s.cfg.PublicBaseURL, "/") + "/verify/" + token

	markup := &tele.ReplyMarkup{}
	button := markup.URL("打开验证页", verifyURL)
	markup.Inline(markup.Row(button))

	prompt := fmt.Sprintf(`%s 你好，请在 %s 内点击下方链接完成人机验证`, mentionHTML(user), formatTimeout(policy.Verify.TimeoutSeconds))
	sent, err := s.sendThrottled(ctx, chat, prompt, &tele.SendOptions{
		ParseMode:             tele.ModeHTML,
		DisableWebPagePreview: true,
		ReplyMarkup:           markup,
	})
	if err != nil {
		s.logger.Error("send turnstile verification prompt", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return err
	}

	if err := s.storePendingVerification(ctx, chat.ID, user, "turnstile", payload, sent.ID, policy.Verify.TimeoutSeconds, policy.Verify.FailAction); err != nil {
		s.deleteVerificationMessage(chat, int64Ptr(int64(sent.ID)))
		return err
	}
	return nil
}

func (s *Service) VerifyTurnstileToken(ctx context.Context, token, cfResponse, remoteIP string) error {
	token = strings.TrimSpace(token)
	cfResponse = strings.TrimSpace(cfResponse)

	if token == "" || cfResponse == "" {
		return fmt.Errorf("%w: missing token or cf_response", ErrTurnstileVerifyFailed)
	}
	if strings.TrimSpace(s.cfg.TurnstileSecret) == "" {
		return ErrTurnstileNotConfigured
	}

	verified, err := s.verifyTurnstileWithCloudflare(ctx, cfResponse, remoteIP)
	if err != nil {
		return err
	}
	if !verified.Success {
		reason := strings.Join(verified.ErrorCodes, ", ")
		if reason == "" {
			reason = "unknown_error"
		}
		return fmt.Errorf("%w: %s", ErrTurnstileVerifyFailed, reason)
	}

	pending, err := s.queries.DeletePendingVerificationByToken(ctx, token)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ErrTurnstileTokenNotFound
		}
		return fmt.Errorf("consume turnstile token: %w", err)
	}
	chat := &tele.Chat{ID: pending.ChatID}
	user := &tele.User{
		ID:        pending.UserID,
		Username:  derefString(pending.Username),
		FirstName: derefString(pending.FirstName),
	}

	policy, polErr := s.LoadGuardPolicy(ctx, chat.ID)
	if polErr != nil {
		s.logger.Warn("load guard policy before turnstile permission sync failed, using defaults", zap.Error(polErr), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		policy = config.DefaultPolicy
	}
	if err := s.applyVerificationPassPermissions(ctx, chat, user, policy); err != nil {
		restoreErr := s.restorePendingVerification(ctx, pending)
		if restoreErr != nil {
			s.logger.Error("restore turnstile pending after restrict failure", zap.Error(restoreErr), zap.Int64("chat_id", pending.ChatID), zap.Int64("user_id", pending.UserID))
		}
		return fmt.Errorf("sync verification pass permissions: %w", err)
	}
	s.ensureRuntimeGuards()
	s.joinProtector.ReleasePending(pending.ChatID)

	s.deleteVerificationMessage(chat, pending.JoinMessageID)
	s.sendWelcomeMessage(ctx, chat, user)

	s.logger.Info("user verified via turnstile", zap.Int64("chat_id", pending.ChatID), zap.Int64("user_id", pending.UserID))
	return nil
}

func (s *Service) verifyTurnstileWithCloudflare(ctx context.Context, cfResponse, remoteIP string) (turnstileSiteVerifyResponse, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	idempotencyKey, err := s.turnstileIdempotencyKey(ctx, cfResponse)
	if err != nil {
		return turnstileSiteVerifyResponse{}, err
	}

	fields := map[string]string{
		"secret":          s.cfg.TurnstileSecret,
		"response":        cfResponse,
		"idempotency_key": idempotencyKey,
	}
	if strings.TrimSpace(remoteIP) != "" {
		fields["remoteip"] = strings.TrimSpace(remoteIP)
	}

	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return turnstileSiteVerifyResponse{}, fmt.Errorf("write turnstile form field: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return turnstileSiteVerifyResponse{}, fmt.Errorf("close turnstile form: %w", err)
	}

	requestCtx, cancel := context.WithTimeout(ctx, turnstileSiteverifyTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, turnstileSiteverifyURL, body)
	if err != nil {
		return turnstileSiteVerifyResponse{}, fmt.Errorf("new turnstile request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := turnstileSiteverifyClient.Do(req)
	if err != nil {
		return turnstileSiteVerifyResponse{}, fmt.Errorf("call turnstile siteverify: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return turnstileSiteVerifyResponse{}, fmt.Errorf("turnstile siteverify status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var payload turnstileSiteVerifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return turnstileSiteVerifyResponse{}, fmt.Errorf("decode turnstile response: %w", err)
	}
	return payload, nil
}

func (s *Service) turnstileIdempotencyKey(ctx context.Context, cfResponse string) (string, error) {
	redisClient := s.Redis()
	if redisClient == nil {
		s.logger.Warn("turnstile idempotency redis unavailable; generating one-shot key")
		return generateTurnstileIdempotencyKey()
	}

	cacheKey := "turnstile_idempotency:" + sha256HexString(cfResponse)
	if cached, err := redisClient.Get(ctx, cacheKey).Result(); err == nil && strings.TrimSpace(cached) != "" {
		return cached, nil
	} else if err != nil && !errors.Is(err, redis.Nil) {
		s.logger.Warn("get turnstile idempotency key failed; generating one-shot key", zap.Error(err))
		return generateTurnstileIdempotencyKey()
	}

	generated, err := generateTurnstileIdempotencyKey()
	if err != nil {
		return "", err
	}
	stored, err := redisClient.SetNX(ctx, cacheKey, generated, turnstileIdempotencyTTL).Result()
	if err != nil {
		s.logger.Warn("store turnstile idempotency key failed; using one-shot key", zap.Error(err))
		return generated, nil
	}
	if stored {
		return generated, nil
	}
	if cached, err := redisClient.Get(ctx, cacheKey).Result(); err == nil && strings.TrimSpace(cached) != "" {
		return cached, nil
	}
	return generated, nil
}

func generateTurnstileIdempotencyKey() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate turnstile idempotency key: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func sha256HexString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s *Service) restorePendingVerification(ctx context.Context, pending store.PendingVerification) error {
	_, err := s.queries.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{
		ChatID:        pending.ChatID,
		UserID:        pending.UserID,
		Username:      pending.Username,
		FirstName:     pending.FirstName,
		Method:        pending.Method,
		Payload:       pending.Payload,
		JoinMessageID: pending.JoinMessageID,
		ExpiresAt:     pending.ExpiresAt,
	})
	return err
}

func generateVerificationToken() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}
