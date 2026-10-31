package bot

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type dualRoundTrip func(*http.Request) (*http.Response, error)

func (f dualRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDualProviderANDAndBinding(t *testing.T) {
	cases := []struct {
		name, cf, h string
		ok          bool
	}{
		{"both", `{"success":true,"hostname":"example.com","action":"clawguard_dual","cdata":"pending"}`, `{"success":true,"hostname":"example.com"}`, true},
		{"cf fails", `{"success":false}`, `{"success":true,"hostname":"example.com"}`, false},
		{"h fails", `{"success":true,"hostname":"example.com","action":"clawguard_dual","cdata":"pending"}`, `{"success":false}`, false},
		{"other task", `{"success":true,"hostname":"example.com","action":"clawguard_dual","cdata":"other"}`, `{"success":true,"hostname":"example.com"}`, false},
		{"other hostname", `{"success":true,"hostname":"example.com","action":"clawguard_dual","cdata":"pending"}`, `{"success":true,"hostname":"other.example"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: dualRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.Method != "POST" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
					t.Fatal("wrong request")
				}
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				var body string
				switch r.URL.String() {
				case dualTurnstileURL:
					if r.Form.Get("secret") != "cf-secret" || r.Form.Get("response") != "cf-token" || r.Form.Get("remoteip") != "192.0.2.1" {
						t.Fatal("wrong CF fields")
					}
					body = tc.cf
				case dualHCaptchaURL:
					if r.Form.Get("secret") != "h-secret" || r.Form.Get("response") != "h-token" || r.Form.Get("sitekey") != "h-site" || r.Form.Get("remoteip") != "192.0.2.1" {
						t.Fatal("wrong hCaptcha fields")
					}
					body = tc.h
				default:
					t.Fatal("unexpected destination")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}
			err := verifyDualProviders(context.Background(), client, "cf-secret", "h-secret", "h-site", "example.com", "pending", "cf-token", "h-token", "192.0.2.1")
			if (err == nil) != tc.ok {
				t.Fatalf("unexpected result: %v", err)
			}
			if err != nil && (strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "token")) {
				t.Fatal("sensitive data in error")
			}
		})
	}
}

func TestDualProviderBoundsAndRedirect(t *testing.T) {
	calls := 0
	client := &http.Client{CheckRedirect: dualCaptchaHTTPClient.CheckRedirect, Transport: dualRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "challenges.cloudflare.com" {
			t.Fatal("followed redirect to unexpected host")
		}
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://attacker.invalid/"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	check := func(cf, h string) error {
		return verifyDualProviders(context.Background(), client, "cf-secret", "h-secret", "h-site", "example.com", "pending", cf, h, "")
	}
	if err := check("", "h"); err == nil || calls != 0 {
		t.Fatal("empty token sent to provider")
	}
	if err := check(strings.Repeat("x", 2049), "h"); err == nil || calls != 0 {
		t.Fatal("oversized token sent to provider")
	}
	if err := check("cf", "h"); err == nil || calls != 1 {
		t.Fatalf("redirect accepted or followed: %v calls=%d", err, calls)
	}
}

func TestDualProviderTimeoutDoesNotPass(t *testing.T) {
	client := &http.Client{Transport: dualRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == dualTurnstileURL {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"success":true,"hostname":"example.com","action":"clawguard_dual","cdata":"pending"}`))}, nil
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := verifyDualProviders(ctx, client, "cf-secret", "h-secret", "h-site", "example.com", "pending", "cf", "h", "")
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("timeout not fail closed: %v", err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "cf") {
		t.Fatal("error leaked request data")
	}
}
