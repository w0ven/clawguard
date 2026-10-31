package api

import (
	"strings"
	"testing"

	"github.com/openclaw/clawguard/internal/config"
)

func TestDualCaptchaAvailabilityAndLogSafeRateKey(t *testing.T) {
	policy := config.DefaultPolicy
	policy.Verify.Method = "turnstile_hcaptcha"
	if err := config.ValidateGuardPolicy(policy); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{TurnstileSiteKey: "cf-site", TurnstileSecret: "cf-secret", HCaptchaSiteKey: "h-site", HCaptchaSecret: "ES_fixture-secret"}
	if err := validateDualCaptchaAvailability(config.DefaultPolicy, policy, cfg); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []config.Config{{}, {TurnstileSiteKey: "__FROM_CLOUDFLARE__", TurnstileSecret: "secret", HCaptchaSiteKey: "site", HCaptchaSecret: "secret"}, {TurnstileSiteKey: "site", TurnstileSecret: "secret", HCaptchaSiteKey: "site"}} {
		if validateDualCaptchaAvailability(config.DefaultPolicy, policy, bad) == nil {
			t.Fatal("missing/placeholder key accepted")
		}
	}
	if err := validateDualCaptchaAvailability(policy, policy, config.Config{}); err != nil {
		t.Fatal("existing dual setting should not block unrelated edits:", err)
	}
	previousDisabled := policy
	previousDisabled.Verify.Enabled = false
	if validateDualCaptchaAvailability(previousDisabled, policy, config.Config{}) == nil {
		t.Fatal("enabling dual verification without keys was accepted")
	}
	policy.Verify.Method = "math_image"
	if err := validateDualCaptchaAvailability(config.DefaultPolicy, policy, config.Config{}); err != nil {
		t.Fatal(err)
	}
	// The public endpoint limits by a hash, never a credential-bearing raw token.
	token := "fixture-sensitive-token"
	key := (&Server{}).rateLimitKey("verify:dual_token", dualCaptchaRateHash(token))
	if strings.Contains(key, token) || !strings.HasSuffix(key, dualCaptchaRateHash(token)) {
		t.Fatal("raw token in rate key")
	}
}
