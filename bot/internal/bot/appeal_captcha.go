package bot

import (
	"context"
	"net/url"
	"strings"
)

// VerifyAppealProviders verifies a fresh appeal challenge, without invoking or
// consuming any join-verification state.
func (s *Service) VerifyAppealProviders(ctx context.Context, challenge, cf, hc, ip string) error {
	if !s.cfg.DualCaptchaConfigured() || strings.TrimSpace(challenge) == "" {
		return ErrDualCaptchaUnavailable
	}
	u, err := url.Parse(s.cfg.PublicBaseURL)
	if err != nil || u.Hostname() == "" {
		return ErrDualCaptchaUnavailable
	}
	return verifyDualProviders(ctx, dualCaptchaHTTPClient, s.cfg.TurnstileSecret, s.cfg.HCaptchaSecret, s.cfg.HCaptchaSiteKey, u.Hostname(), challenge, cf, hc, ip)
}
