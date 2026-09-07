package bot

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type assistantFetchMeta struct {
	URL         string
	ContentType string
	Truncated   bool
	Code        string
	Message     string
}

type assistantResolveFunc func(context.Context, string) ([]net.IP, error)
type assistantDialFunc func(context.Context, string, string) (net.Conn, error)
type assistantTransportFactory func(*url.URL, []net.IP, string, assistantDialFunc) *http.Transport

type assistantFetchDependencies struct {
	Resolve   assistantResolveFunc
	Dial      func([]net.IP, string) assistantDialFunc
	Transport assistantTransportFactory
}

var (
	assistantFetchDependenciesMu sync.RWMutex
	assistantFetchDeps           = assistantFetchDependencies{
		Resolve: assistantResolveFunc(resolvePublicAssistantHost),
		Dial: func(ips []net.IP, port string) assistantDialFunc {
			return assistantDialFunc(fixedAssistantDialer(ips, port))
		},
		Transport: assistantTransportFactory(newAssistantTransport),
	}
)

func snapshotAssistantFetchDependencies() assistantFetchDependencies {
	assistantFetchDependenciesMu.RLock()
	defer assistantFetchDependenciesMu.RUnlock()
	return assistantFetchDeps
}

// setAssistantFetchDependenciesForTest is an internal-only hook for local
// tests. Public callers cannot pass a resolver, dialer, or transport through
// the tool/API surface; production defaults remain the pinned implementation.
func setAssistantFetchDependenciesForTest(resolve assistantResolveFunc, dial func([]net.IP, string) assistantDialFunc, transport assistantTransportFactory) func() {
	assistantFetchDependenciesMu.Lock()
	previous := assistantFetchDeps
	if resolve != nil {
		assistantFetchDeps.Resolve = resolve
	}
	if dial != nil {
		assistantFetchDeps.Dial = dial
	}
	if transport != nil {
		assistantFetchDeps.Transport = transport
	}
	assistantFetchDependenciesMu.Unlock()
	return func() {
		assistantFetchDependenciesMu.Lock()
		assistantFetchDeps = previous
		assistantFetchDependenciesMu.Unlock()
	}
}

func fetchAssistantURL(ctx context.Context, raw string, allowDomains []string) (string, assistantFetchMeta, error) {
	if len(raw) > 2048 {
		return "", assistantFetchMeta{Code: "invalid_url", Message: "URL过长"}, fmt.Errorf("url too long")
	}
	current, err := validateAssistantURL(raw, allowDomains)
	if err != nil {
		return "", assistantFetchMeta{Code: "url_denied", Message: err.Error()}, err
	}
	deps := snapshotAssistantFetchDependencies()
	for redirect := 0; redirect <= assistantMaxWebRedirects; redirect++ {
		ips, err := deps.Resolve(ctx, current.Hostname())
		if err != nil {
			return "", assistantFetchMeta{Code: "network_denied", Message: "目标地址未通过公网DNS校验"}, err
		}
		port := current.Port()
		if port == "" {
			if current.Scheme == "http" {
				port = "80"
			} else {
				port = "443"
			}
		}
		transport := deps.Transport(current, ips, port, deps.Dial(ips, port))
		if transport == nil {
			return "", assistantFetchMeta{Code: "network_error", Message: "网页读取依赖不可用"}, fmt.Errorf("nil fetch transport")
		}
		client := &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
		reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		req, reqErr := http.NewRequestWithContext(reqCtx, http.MethodGet, current.String(), nil)
		if reqErr != nil {
			cancel()
			return "", assistantFetchMeta{Code: "invalid_url", Message: "URL无效"}, reqErr
		}
		req.Header.Set("Accept", "text/plain,text/html,application/json;q=0.9")
		req.Header.Set("User-Agent", "ClawGuard-GroupAssistant/1.0")
		resp, doErr := client.Do(req)
		if doErr != nil {
			cancel()
			return "", assistantFetchMeta{Code: "network_error", Message: "公开网页读取失败"}, doErr
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			location := strings.TrimSpace(resp.Header.Get("Location"))
			resp.Body.Close()
			cancel()
			if redirect == assistantMaxWebRedirects || location == "" {
				return "", assistantFetchMeta{Code: "redirect_denied", Message: "重定向次数或目标无效"}, fmt.Errorf("redirect limit")
			}
			next, parseErr := current.Parse(location)
			if parseErr != nil {
				return "", assistantFetchMeta{Code: "redirect_denied", Message: "重定向URL无效"}, parseErr
			}
			current, err = validateAssistantURL(next.String(), allowDomains)
			if err != nil {
				return "", assistantFetchMeta{Code: "redirect_denied", Message: "重定向目标不在域名白名单"}, err
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			cancel()
			return "", assistantFetchMeta{Code: "http_error", Message: "公开网页返回错误"}, fmt.Errorf("http status %d", resp.StatusCode)
		}
		contentType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
		if contentType == "" || (contentType != "text/plain" && contentType != "text/html" && contentType != "application/json" && contentType != "application/xml" && contentType != "text/xml") {
			resp.Body.Close()
			cancel()
			return "", assistantFetchMeta{Code: "content_type_denied", Message: "网页内容类型不受支持"}, fmt.Errorf("content type denied")
		}
		if resp.ContentLength > assistantMaxWebBytes {
			resp.Body.Close()
			cancel()
			return "", assistantFetchMeta{Code: "body_too_large", Message: "网页正文超过大小限制"}, fmt.Errorf("body too large")
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, assistantMaxWebBytes+1))
		resp.Body.Close()
		cancel()
		if readErr != nil {
			return "", assistantFetchMeta{Code: "read_error", Message: "网页正文读取失败"}, readErr
		}
		truncated := len(body) > assistantMaxWebBytes
		if truncated {
			body = body[:assistantMaxWebBytes]
		}
		return string(body), assistantFetchMeta{URL: current.String(), ContentType: contentType, Truncated: truncated}, nil
	}
	return "", assistantFetchMeta{Code: "redirect_denied", Message: "重定向次数超限"}, fmt.Errorf("redirect limit")
}

func validateAssistantURL(raw string, allowDomains []string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, fmt.Errorf("仅支持http或https公开URL")
	}
	if u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return nil, fmt.Errorf("URL包含不允许的凭据或片段")
	}
	if u.Port() != "" && u.Port() != "80" && u.Port() != "443" {
		return nil, fmt.Errorf("URL端口不受支持")
	}
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(u.Hostname()), "."))
	explicitPort := u.Port()
	for _, r := range host {
		if r > 127 {
			return nil, fmt.Errorf("不允许非ASCII混淆主机名")
		}
	}
	if net.ParseIP(host) != nil || strings.Contains(host, "@") {
		return nil, fmt.Errorf("不允许IP地址或混淆主机名")
	}
	if !assistantDomainAllowed(host, allowDomains) {
		return nil, fmt.Errorf("域名不在管理员白名单")
	}
	u.Host = host
	if explicitPort != "" {
		u.Host = net.JoinHostPort(host, explicitPort)
	}
	return u, nil
}

func assistantDomainAllowed(host string, domains []string) bool {
	for _, raw := range domains {
		domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
		if domain == "" || net.ParseIP(domain) != nil || !strings.Contains(domain, ".") {
			continue
		}
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func resolvePublicAssistantHost(ctx context.Context, host string) ([]net.IP, error) {
	resolver := net.DefaultResolver
	ips, err := resolver.LookupIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("DNS lookup failed")
	}
	for _, ip := range ips {
		if !isPublicAssistantIP(ip) {
			return nil, fmt.Errorf("DNS resolved to a private or reserved address")
		}
	}
	return ips, nil
}

func isPublicAssistantIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() || !ip.IsGlobalUnicast() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		return isPublicAssistantIPv4(ip4)
	}
	// Global-unicast is not a sufficient public-address test: documentation,
	// benchmark, transition, and reserved IPv6 ranges are routable-looking.
	for _, cidr := range []string{
		"100::/64", "2001:2::/48", "2001:10::/28", "2001:20::/28", "2001:db8::/32",
		"2001::/32", "2002::/16", "3fff::/20", "64:ff9b::/96", "64:ff9b:1::/48",
	} {
		if assistantIPInCIDR(ip, cidr) {
			return false
		}
	}
	return true
}

func isPublicAssistantIPv4(ip net.IP) bool {
	if ip == nil || len(ip) < net.IPv4len {
		return false
	}
	if ip[0] == 0 || ip[0] == 10 || ip[0] == 127 || ip[0] >= 240 || ip[0] == 255 {
		return false
	}
	if ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127 {
		return false
	}
	if ip[0] == 169 && ip[1] == 254 {
		return false
	}
	if ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31 {
		return false
	}
	if ip[0] == 192 && (ip[1] == 0 || ip[1] == 2 || ip[1] == 168) {
		return false
	}
	if ip[0] == 192 && ip[1] == 88 && ip[2] == 99 {
		return false
	}
	if ip[0] == 198 && (ip[1] == 18 || ip[1] == 19 || ip[1] == 51 && ip[2] == 100) {
		return false
	}
	if ip[0] == 203 && ip[1] == 0 && ip[2] == 113 {
		return false
	}
	return true
}

func assistantIPInCIDR(ip net.IP, cidr string) bool {
	_, network, err := net.ParseCIDR(cidr)
	return err == nil && network.Contains(ip)
}

func newAssistantTransport(current *url.URL, _ []net.IP, _ string, dial assistantDialFunc) *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, ServerName: current.Hostname()},
		DialContext:           dial,
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 10 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
	}
}

func fixedAssistantDialer(ips []net.IP, port string) func(context.Context, string, string) (net.Conn, error) {
	if port == "" {
		port = "443"
	}
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		var last error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		if last == nil {
			last = fmt.Errorf("no resolved address")
		}
		return nil, last
	}
}
