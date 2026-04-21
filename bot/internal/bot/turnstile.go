package bot

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

var (
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
	sent, err := s.bot.Send(chat, prompt, &tele.SendOptions{
		ParseMode:             tele.ModeHTML,
		DisableWebPagePreview: true,
		ReplyMarkup:           markup,
	})
	if err != nil {
		s.logger.Error("send turnstile verification prompt", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return err
	}

	return s.storePendingVerification(ctx, chat.ID, user, "turnstile", payload, sent.ID, policy.Verify.TimeoutSeconds)
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

	member := tele.ChatMember{
		User:   user,
		Rights: tele.NoRestrictions(),
	}
	if err := s.bot.Restrict(chat, &member); err != nil {
		restoreErr := s.restorePendingVerification(ctx, pending)
		if restoreErr != nil {
			s.logger.Error("restore turnstile pending after restrict failure", zap.Error(restoreErr), zap.Int64("chat_id", pending.ChatID), zap.Int64("user_id", pending.UserID))
		}
		return fmt.Errorf("unrestrict member: %w", err)
	}

	s.deleteVerificationMessage(chat, pending.JoinMessageID)
	s.sendWelcomeMessage(ctx, chat, user)

	s.logger.Info("user verified via turnstile", zap.Int64("chat_id", pending.ChatID), zap.Int64("user_id", pending.UserID))
	return nil
}

func (s *Service) verifyTurnstileWithCloudflare(ctx context.Context, cfResponse, remoteIP string) (turnstileSiteVerifyResponse, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	fields := map[string]string{
		"secret":   s.cfg.TurnstileSecret,
		"response": cfResponse,
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://challenges.cloudflare.com/turnstile/v0/siteverify", body)
	if err != nil {
		return turnstileSiteVerifyResponse{}, fmt.Errorf("new turnstile request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
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
