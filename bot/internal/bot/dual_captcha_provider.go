package bot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	dualTurnstileURL  = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	dualHCaptchaURL   = "https://api.hcaptcha.com/siteverify"
	dualCaptchaAction = "clawguard_dual"
)

var errDualProvider = errors.New("captcha verification failed; refresh both challenges and retry")

// The transport never follows a redirect, which prevents forwarding provider
// credentials to a different origin. Tokens and provider responses are never
// included in the returned errors.
var dualCaptchaHTTPClient = &http.Client{
	Timeout: 8 * time.Second,
	Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
	},
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
}

type dualProviderResult struct {
	Success  bool   `json:"success"`
	Hostname string `json:"hostname"`
	Action   string `json:"action"`
	CData    string `json:"cdata"`
}

func verifyDualProviders(ctx context.Context, client *http.Client, cfSecret, hSecret, hSiteKey, hostname, pendingToken, cfToken, hToken, remoteIP string) error {
	if client == nil || strings.TrimSpace(cfSecret) == "" || strings.TrimSpace(hSecret) == "" || strings.TrimSpace(hSiteKey) == "" || hostname == "" || pendingToken == "" || len(cfToken) == 0 || len(cfToken) > 2048 || len(hToken) == 0 || len(hToken) > 8192 {
		return errDualProvider
	}
	cfFields := url.Values{"secret": {cfSecret}, "response": {cfToken}}
	hFields := url.Values{"secret": {hSecret}, "response": {hToken}, "sitekey": {hSiteKey}}
	if remoteIP != "" {
		cfFields.Set("remoteip", remoteIP)
		hFields.Set("remoteip", remoteIP)
	}
	cf, err := dualSiteverify(ctx, client, dualTurnstileURL, cfFields)
	if err != nil || !cf.Success || !strings.EqualFold(cf.Hostname, hostname) || cf.Action != dualCaptchaAction || cf.CData != pendingToken {
		return errDualProvider
	}
	h, err := dualSiteverify(ctx, client, dualHCaptchaURL, hFields)
	if err != nil || !h.Success || !strings.EqualFold(h.Hostname, hostname) {
		return errDualProvider
	}
	return nil
}

func dualSiteverify(ctx context.Context, client *http.Client, endpoint string, fields url.Values) (dualProviderResult, error) {
	var result dualProviderResult
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(fields.Encode()))
	if err != nil {
		return result, errDualProvider
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(req)
	if err != nil {
		return result, errDualProvider
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result, errDualProvider
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16384)).Decode(&result); err != nil {
		return dualProviderResult{}, errDualProvider
	}
	return result, nil
}
