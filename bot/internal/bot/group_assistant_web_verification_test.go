package bot

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestAssistantVerificationURLBoundary(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "ftp://example.org/a", "http://evil.example/", "http://example.org.evil/", "http://evil-example.org/", "http://user:password@example.org/", "http://example.org:8080/", "http://127.0.0.1/", "http://169.254.169.254/", "http://[::1]/", "http://example.org/#x", "http://ｅxample.org/"} {
		t.Run(raw, func(t *testing.T) {
			if _, e := validateAssistantURL(raw, []string{"example.org"}); e == nil {
				t.Fatal("unsafe URL accepted")
			}
		})
	}
	for _, raw := range []string{"https://example.org/path", "https://sub.example.org/path", "http://EXAMPLE.ORG./path"} {
		if _, e := validateAssistantURL(raw, []string{"example.org"}); e != nil {
			t.Errorf("valid URL %s: %v", raw, e)
		}
	}
	t.Run("ExplicitAllowedPortPreserved", func(t *testing.T) {
		for _, raw := range []string{"https://example.org:80/a", "http://example.org:443/a"} {
			want, _ := url.Parse(raw)
			got, e := validateAssistantURL(raw, []string{"example.org"})
			if e != nil || got.Port() != want.Port() {
				t.Errorf("silently changed destination port: %s -> %v err=%v", raw, got, e)
			}
		}
	})
}
func TestAssistantVerificationPublicIP(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "100.127.255.254", "0.0.0.0", "224.0.0.1", "::", "::1", "fc00::1", "fe80::1", "ff02::1", "::ffff:127.0.0.1", "0.0.0.1", "240.0.0.1", "255.255.255.255", "192.0.2.1", "198.51.100.1", "203.0.113.1", "2001:db8::1", "64:ff9b::a00:1", "64:ff9b:1::7f00:1", "2002:7f00:1::", "2001:0:4136:e378:8000:63bf:3fff:fdd2"} {
		t.Run(raw, func(t *testing.T) {
			if isPublicAssistantIP(net.ParseIP(raw)) {
				t.Error("non-public/reserved address accepted")
			}
		})
	}
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !isPublicAssistantIP(net.ParseIP(raw)) {
			t.Errorf("public predicate rejected %s", raw)
		}
	}
}

// Local authoritative fake DNS. No DNS request leaves this test's loopback socket.
func verificationDNS(t *testing.T, addr [4]byte) *net.Resolver {
	t.Helper()
	c, e := net.ListenPacket("udp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			n, peer, e := c.ReadFrom(buf)
			if e != nil {
				return
			}
			var msg dnsmessage.Message
			if e = msg.Unpack(buf[:n]); e != nil {
				continue
			}
			out := dnsmessage.Message{Header: dnsmessage.Header{ID: msg.ID, Response: true, RecursionAvailable: true}, Questions: msg.Questions}
			for _, q := range msg.Questions {
				if q.Type == dnsmessage.TypeA {
					out.Answers = append(out.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 1}, Body: &dnsmessage.AResource{A: func() [4]byte {
						if strings.HasPrefix(q.Name.String(), "private.") {
							return [4]byte{10, 0, 0, 1}
						}
						return addr
					}()}})
				}
			}
			packed, e := out.Pack()
			if e == nil {
				_, _ = c.WriteTo(packed, peer)
			}
		}
	}()
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", c.LocalAddr().String())
	}}
}
func TestAssistantVerificationDNSAndNoPrivateFetch(t *testing.T) {
	old := net.DefaultResolver
	defer func() { net.DefaultResolver = old }()
	for _, tc := range []struct {
		name    string
		ip      [4]byte
		allowed bool
	}{{"public", [4]byte{8, 8, 8, 8}, true}, {"private", [4]byte{10, 0, 0, 1}, false}, {"metadata", [4]byte{169, 254, 169, 254}, false}, {"loopback", [4]byte{127, 0, 0, 1}, false}} {
		t.Run(tc.name, func(t *testing.T) {
			net.DefaultResolver = verificationDNS(t, tc.ip)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			ips, e := resolvePublicAssistantHost(ctx, "assistant-test.example")
			if tc.allowed {
				if e != nil || len(ips) != 1 || ips[0].String() != "8.8.8.8" {
					t.Fatalf("controlled public DNS %+v %v", ips, e)
				}
			} else {
				if e == nil {
					t.Fatalf("private DNS accepted %+v", ips)
				}
				_, meta, e := fetchAssistantURL(ctx, "http://assistant-test.example/", []string{"assistant-test.example"})
				if e == nil || meta.Code != "network_denied" {
					t.Fatalf("private fetch not rejected %+v %v", meta, e)
				}
			}
		})
	}
}
func TestAssistantVerificationFixedDialerIgnoresTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("local")) }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	dial := fixedAssistantDialer([]net.IP{net.ParseIP("127.0.0.1")}, u.Port())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, e := dial(ctx, "tcp", "must-not-resolve.invalid:80")
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	if !strings.HasPrefix(conn.RemoteAddr().String(), "127.0.0.1:") {
		t.Fatalf("dial not pinned: %s", conn.RemoteAddr())
	}
}

func TestAssistantVerificationFetchFullHTTP(t *testing.T) {
	old := net.DefaultResolver
	net.DefaultResolver = verificationDNS(t, [4]byte{8, 8, 8, 8})
	defer func() { net.DefaultResolver = old }()
	var requests, resolves, dials atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "GET" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Errorf("not anonymous read-only: %s %#v", r.Method, r.Header)
		}
		w.Header().Set("Content-Type", "text/plain")
		switch r.URL.Path {
		case "/ok":
			fmt.Fprint(w, "public local test")
		case "/empty-type":
			w.Header()["Content-Type"] = []string{""}
			fmt.Fprint(w, "opaque")
		case "/binary":
			w.Header().Set("Content-Type", "application/octet-stream")
			fmt.Fprint(w, "binary")
		case "/large-length":
			w.Header().Set("Content-Length", strconv.Itoa(assistantMaxWebBytes+1))
			w.WriteHeader(200)
		case "/large-stream":
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			_, _ = w.Write([]byte(strings.Repeat("x", assistantMaxWebBytes+128)))
		case "/redirect":
			w.Header().Set("Set-Cookie", "session=must-not-forward")
			http.Redirect(w, r, "http://sub.example.org/ok", 302)
		case "/redirect-denied":
			http.Redirect(w, r, "http://evil.invalid/ok", 302)
		case "/redirect-private":
			http.Redirect(w, r, "http://private.example.org/ok", 302)
		case "/loop":
			http.Redirect(w, r, "/loop", 302)
		case "/timeout":
			select {
			case <-r.Context().Done():
			case <-time.After(12 * time.Second):
			}
		default:
			if strings.HasPrefix(r.URL.Path, "/type/") {
				w.Header().Set("Content-Type", strings.TrimPrefix(r.URL.Path, "/type/"))
				fmt.Fprint(w, "supported")
			} else {
				w.WriteHeader(503)
			}
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	restore := setAssistantFetchDependenciesForTest(func(ctx context.Context, h string) ([]net.IP, error) {
		resolves.Add(1)
		// This controllable resolver must not leave canceled DNS goroutines using
		// net.DefaultResolver after the test restores the global dependency.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return resolvePublicAssistantHost(ctx, h)
	}, func(ips []net.IP, port string) assistantDialFunc {
		if len(ips) != 1 || ips[0].String() != "8.8.8.8" || port != "80" {
			t.Errorf("not pinned verified public address: %v port=%s", ips, port)
		}
		return func(ctx context.Context, network, address string) (net.Conn, error) {
			dials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, "tcp", u.Host)
		}
	}, func(target *url.URL, ips []net.IP, port string, dial assistantDialFunc) *http.Transport {
		tr := newAssistantTransport(target, ips, port, dial)
		if tr.Proxy != nil {
			t.Error("proxy enabled")
		}
		return tr
	})
	defer restore()
	for _, tc := range []struct {
		path, code string
		length     int
		truncated  bool
		reqs       int
	}{{"/ok", "", 17, false, 1}, {"/empty-type", "content_type_denied", 0, false, 1}, {"/binary", "content_type_denied", 0, false, 1}, {"/large-length", "body_too_large", 0, false, 1}, {"/large-stream", "", assistantMaxWebBytes, true, 1}, {"/redirect", "", 17, false, 2}, {"/redirect-denied", "redirect_denied", 0, false, 1}, {"/redirect-private", "network_denied", 0, false, 1}, {"/loop", "redirect_denied", 0, false, 4}} {
		t.Run(tc.path, func(t *testing.T) {
			before := requests.Load()
			dnsBefore := resolves.Load()
			body, meta, e := fetchAssistantURL(context.Background(), "http://example.org"+tc.path, []string{"example.org"})
			if meta.Code != tc.code || (e == nil) != (tc.code == "") || len(body) != tc.length || meta.Truncated != tc.truncated || requests.Load()-before != int32(tc.reqs) {
				t.Fatalf("fetch len=%d meta=%+v err=%v requests=%d", len(body), meta, e, requests.Load()-before)
			}
			if tc.path == "/redirect" && resolves.Load()-dnsBefore != 2 {
				t.Error("redirect not re-resolved")
			}
		})
	}
	for _, ct := range []string{"text/plain", "text/html", "application/json", "application/xml", "text/xml"} {
		t.Run(ct, func(t *testing.T) {
			_, meta, e := fetchAssistantURL(context.Background(), "http://example.org/type/"+ct, []string{"example.org"})
			if e != nil || meta.ContentType != ct {
				t.Fatalf("valid type rejected %+v %v", meta, e)
			}
		})
	}
	t.Run("SingleTLDRejected", func(t *testing.T) {
		before := requests.Load()
		_, _, e := fetchAssistantURL(context.Background(), "http://example.org/ok", []string{"org"})
		if e == nil || requests.Load() != before {
			t.Fatal("single TLD allowed")
		}
	})
	t.Run("RealTenSecondTimeout", func(t *testing.T) {
		start := time.Now()
		_, _, e := fetchAssistantURL(context.Background(), "http://example.org/timeout", []string{"example.org"})
		elapsed := time.Since(start)
		if e == nil || elapsed < 9*time.Second || elapsed > 11*time.Second {
			t.Fatalf("10s timeout err=%v elapsed=%s", e, elapsed)
		}
	})
	t.Run("ParentCancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		before := requests.Load()
		_, _, e := fetchAssistantURL(ctx, "http://example.org/ok", []string{"example.org"})
		if e == nil || requests.Load() != before {
			t.Fatal("canceled fetch sent request")
		}
	})
}
