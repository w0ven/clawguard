package ai

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var blockedProviderPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

// ValidateProviderBaseURL validates and normalizes an outbound LLM provider base URL.
// Only public HTTP(S) endpoints without credentials, query strings, or fragments are allowed.
func ValidateProviderBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("base_url is required")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", errors.New("invalid base_url")
	}

	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "http" && scheme != "https" {
		return "", errors.New("base_url must use http or https")
	}
	if parsed.User != nil {
		return "", errors.New("base_url must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", errors.New("base_url must not contain query parameters or fragments")
	}

	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" {
		return "", errors.New("base_url host is required")
	}
	if isBlockedProviderHostname(host) {
		return "", errors.New("base_url host is not allowed")
	}

	if port := parsed.Port(); port != "" {
		if _, err := net.LookupPort("tcp", port); err != nil {
			return "", errors.New("base_url port is invalid")
		}
	}

	if addr, err := netip.ParseAddr(host); err == nil {
		if !isAllowedProviderAddr(addr) {
			return "", errors.New("base_url IP address is not a public routable address")
		}
	}

	normalized := url.URL{
		Scheme: scheme,
		Host:   canonicalHostPort(scheme, host, parsed.Port()),
		Path:   strings.TrimRight(parsed.EscapedPath(), "/"),
	}
	return strings.TrimRight(normalized.String(), "/"), nil
}

// SameProviderBaseURL reports whether two provider base URLs refer to the exact same normalized endpoint.
func SameProviderBaseURL(a, b string) bool {
	na, errA := canonicalComparableBaseURL(a)
	nb, errB := canonicalComparableBaseURL(b)
	if errA != nil || errB != nil {
		return strings.TrimRight(strings.TrimSpace(a), "/") == strings.TrimRight(strings.TrimSpace(b), "/")
	}
	return na == nb
}

func canonicalComparableBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("empty url")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if scheme == "" || host == "" {
		return "", errors.New("missing scheme or host")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return trimmed, nil
	}
	normalized := url.URL{
		Scheme: scheme,
		Host:   canonicalHostPort(scheme, host, parsed.Port()),
		Path:   strings.TrimRight(parsed.EscapedPath(), "/"),
	}
	return strings.TrimRight(normalized.String(), "/"), nil
}

func canonicalHostPort(scheme, host, port string) string {
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		if port != "" {
			return net.JoinHostPort(host, port)
		}
		return "[" + host + "]"
	}
	if port != "" {
		return net.JoinHostPort(host, port)
	}
	return host
}

func isBlockedProviderHostname(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || host == "localhost" {
		return true
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".home.arpa"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

func isAllowedProviderAddr(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	if addr.Zone() != "" {
		return false
	}
	if addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified() ||
		!addr.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range blockedProviderPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

type ipResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

func newSafeProviderDialContext(dialer *net.Dialer, resolver ipResolver) func(ctx context.Context, network, address string) (net.Conn, error) {
	if dialer == nil {
		dialer = &net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("invalid provider address %q: %w", address, err)
		}
		cleanHost := strings.ToLower(strings.TrimSpace(host))
		if isBlockedProviderHostname(cleanHost) {
			return nil, fmt.Errorf("provider host %q is blocked", host)
		}

		var addrs []netip.Addr
		if literal, parseErr := netip.ParseAddr(cleanHost); parseErr == nil {
			addrs = []netip.Addr{literal}
		} else {
			resolved, lookupErr := resolver.LookupNetIP(ctx, "ip", cleanHost)
			if lookupErr != nil {
				return nil, lookupErr
			}
			addrs = resolved
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("provider host %q resolved to no addresses", host)
		}
		for _, addr := range addrs {
			if !isAllowedProviderAddr(addr) {
				return nil, fmt.Errorf("provider address %s is blocked (non-public IP)", addr.Unmap().String())
			}
		}

		var lastErr error
		for _, addr := range addrs {
			target := net.JoinHostPort(addr.Unmap().String(), port)
			conn, dialErr := dialer.DialContext(ctx, network, target)
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
}

func newSafeOpenAIHTTPClient() *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &http.Client{
		Timeout: 5 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DialContext:           newSafeProviderDialContext(dialer, net.DefaultResolver),
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
}
