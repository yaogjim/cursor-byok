package interaction

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"cursor/gen/agentv1"
)

func TestValidateWebFetchURLFreezesSchemeHostAndLiteralChecks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		rawURL  string
		wantErr string
	}{
		{name: "http allowed", rawURL: "http://example.com/path"},
		{name: "https allowed", rawURL: "https://example.com/path"},
		{name: "empty", rawURL: "  ", wantErr: "web fetch url is required"},
		{name: "ftp rejected", rawURL: "ftp://example.com/file", wantErr: "web fetch only supports http and https urls"},
		{name: "missing host", rawURL: "https://", wantErr: "web fetch url host is required"},
		{name: "localhost rejected", rawURL: "http://localhost/index", wantErr: "web fetch host is not public-web accessible"},
		{name: "localhost suffix rejected", rawURL: "http://app.localhost/", wantErr: "web fetch host is not public-web accessible"},
		{name: "loopback v4 rejected", rawURL: "http://127.0.0.1/", wantErr: "web fetch host is not public-web accessible"},
		{name: "loopback v6 rejected", rawURL: "http://[::1]/", wantErr: "web fetch host is not public-web accessible"},
		{name: "private v4 rejected", rawURL: "http://192.168.1.1/", wantErr: "web fetch host is not public-web accessible"},
		{name: "link local rejected", rawURL: "http://169.254.1.1/", wantErr: "web fetch host is not public-web accessible"},
		{name: "unspecified rejected", rawURL: "http://0.0.0.0/", wantErr: "web fetch host is not public-web accessible"},
		{name: "mapped loopback rejected", rawURL: "http://[::ffff:127.0.0.1]/", wantErr: "web fetch host is not public-web accessible"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			parsed, err := validateWebFetchURL(test.rawURL)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("validateWebFetchURL(%q) unexpected error: %v", test.rawURL, err)
				}
				if parsed == nil || parsed.String() == "" {
					t.Fatalf("validateWebFetchURL(%q) returned empty url", test.rawURL)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateWebFetchURL(%q) error = %v, want %q", test.rawURL, err, test.wantErr)
			}
		})
	}
}

func TestValidateWebFetchURLRejectsUserinfoAndNonPublicLiterals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		rawURL  string
		wantErr string
	}{
		{rawURL: "http://user:pass@example.com/", wantErr: "web fetch url must not include credentials"},
		{rawURL: "https://user@example.com/path", wantErr: "web fetch url must not include credentials"},
		{rawURL: "http://224.0.0.1/", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://100.64.0.1/", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://192.0.2.1/", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://198.51.100.1/", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://203.0.113.1/", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://198.18.0.1/", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://240.0.0.1/", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://255.255.255.255/", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://[2001:db8::1]/", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://[::ffff:10.0.0.1]/", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://[::ffff:198.18.1.1]/", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://localhost./", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://app.localhost./", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://127.0.0.1./", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://10.0.0.1./", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://[fe80::1%25lo]/", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://[2002:0a00:0001::1]/", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://[64:ff9b::10.0.0.1]/", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://[3fff::1]/", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://[fec0::1]/", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://[::2]/", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://[4000::1]/", wantErr: "web fetch host is not public-web accessible"},
		{rawURL: "http://@example.com/", wantErr: "web fetch url must not include credentials"},
		{rawURL: "http://:pass@example.com/", wantErr: "web fetch url must not include credentials"},
		{rawURL: "http://8.8.8.8/", wantErr: ""},
		{rawURL: "http://8.8.8.8./", wantErr: ""},
		{rawURL: "http://[2002:0808:0808::1]/", wantErr: ""},
		{rawURL: "http://[64:ff9b::8.8.8.8]/", wantErr: ""},
		{rawURL: "http://[2001:4860:4860::8888]/", wantErr: ""},
	}
	for _, test := range tests {
		t.Run(test.rawURL, func(t *testing.T) {
			t.Parallel()
			_, err := validateWebFetchURL(test.rawURL)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestApplyWebFetchResponsePreservesErrorProtocol(t *testing.T) {
	t.Parallel()
	bridge := NewBridge()
	args := &agentv1.WebFetchArgs{Url: "ftp://example.com/file"}
	result, payload := bridge.applyWebFetchResponse(&agentv1.WebFetchRequestResponse{
		Result: &agentv1.WebFetchRequestResponse_Approved_{
			Approved: &agentv1.WebFetchRequestResponse_Approved{},
		},
	}, args)
	if result.GetError() == nil {
		t.Fatal("approved invalid url did not return WebFetchError")
	}
	if result.GetError().GetUrl() != args.GetUrl() {
		t.Fatalf("error url = %q, want %q", result.GetError().GetUrl(), args.GetUrl())
	}
	if !strings.Contains(result.GetError().GetError(), "web fetch only supports http and https urls") {
		t.Fatalf("error message = %q", result.GetError().GetError())
	}
	if payload != result.GetError().GetError() {
		t.Fatalf("payload = %q, want %q", payload, result.GetError().GetError())
	}

	rejected, rejectedPayload := bridge.applyWebFetchResponse(&agentv1.WebFetchRequestResponse{
		Result: &agentv1.WebFetchRequestResponse_Rejected_{
			Rejected: &agentv1.WebFetchRequestResponse_Rejected{Reason: "user declined"},
		},
	}, args)
	if rejected.GetRejected().GetReason() != "user declined" || rejectedPayload != "user declined" {
		t.Fatalf("rejected result = %#v payload = %q", rejected, rejectedPayload)
	}
}

func TestTruncateWebFetchMarkdownUTF8AndLimit(t *testing.T) {
	t.Parallel()
	over := strings.Repeat("a", webFetchMarkdownLimit+8)
	got := truncateWebFetchMarkdown(over)
	if len(got) > webFetchMarkdownLimit {
		t.Fatalf("truncated length = %d, want <= %d", len(got), webFetchMarkdownLimit)
	}
	if !strings.Contains(got, "WebFetch") || !strings.Contains(got, "truncated") {
		t.Fatalf("missing truncation notice: %q", got)
	}

	prefix := strings.Repeat("b", webFetchMarkdownLimit-2)
	got = truncateWebFetchMarkdown(prefix + "世")
	if !utf8.ValidString(got) {
		t.Fatal("truncated markdown is not valid UTF-8")
	}
	if len(got) > webFetchMarkdownLimit {
		t.Fatalf("utf8 truncated length = %d, want <= %d", len(got), webFetchMarkdownLimit)
	}
}

func TestIsWebFetchTextContentType(t *testing.T) {
	t.Parallel()
	allowed := []string{
		"text/html; charset=utf-8",
		"text/plain",
		"application/json",
		"application/xhtml+xml",
		"application/rss+xml",
		"application/ld+json",
		"application/vnd.api+json",
	}
	for _, contentType := range allowed {
		if !isWebFetchTextContentType(contentType) {
			t.Fatalf("content type %q should be allowed", contentType)
		}
	}
	if isWebFetchTextContentType("image/png") {
		t.Fatal("image/png should be rejected")
	}
}

func TestWebFetchPublicDirectSuccessPinsIPAndKeepsHost(t *testing.T) {
	t.Parallel()
	rec := &webFetchCallRecorder{}
	bridge := &Bridge{webFetch: webFetchNetwork{
		timeout:         time.Second,
		lookupIPAddr:    rec.lookup("8.8.8.8"),
		proxyForRequest: rec.directProxy(),
		dialContext: rec.dialHTTP(func(_ string, req *http.Request) pipeHTTPResponse {
			if req.Host != "example.com" {
				t.Errorf("Host = %q, want example.com", req.Host)
			}
			return pipeHTTPResponse{body: "hello-public"}
		}),
	}}
	markdown, err := bridge.executeWebFetch("http://example.com/foo")
	if err != nil {
		t.Fatalf("executeWebFetch: %v", err)
	}
	if !strings.Contains(markdown, "hello-public") || !strings.Contains(markdown, "http://example.com/foo") {
		t.Fatalf("markdown = %q", markdown)
	}
	if got := rec.lookupHosts(); strings.Join(got, ",") != "example.com" {
		t.Fatalf("lookup hosts = %v", got)
	}
	if got := rec.dialedAddrs(); len(got) != 1 || got[0] != "8.8.8.8:80" {
		t.Fatalf("dialed = %v, want [8.8.8.8:80]", got)
	}
}

func TestWebFetchRejectsPrivateOrMixedResolution(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ips  []string
	}{
		{name: "private", ips: []string{"10.0.0.1"}},
		{name: "mixed", ips: []string{"8.8.8.8", "10.0.0.1"}},
		{name: "mapped private", ips: []string{"::ffff:192.168.0.5"}},
		{name: "fake ip", ips: []string{"198.18.0.20"}},
		{name: "nat64 private", ips: []string{"64:ff9b::10.0.0.1"}},
		{name: "6to4 private", ips: []string{"2002:0a00:0001::1"}},
		{name: "site local", ips: []string{"fec0::1"}},
		{name: "ipv4 compatible", ips: []string{"::2"}},
		{name: "reserved 4000", ips: []string{"4000::1"}},
		{name: "mixed reserved", ips: []string{"8.8.8.8", "4000::1"}},
		{name: "mixed 6to4 private", ips: []string{"8.8.8.8", "2002:0a00:0001::1"}},
		{name: "empty", ips: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rec := &webFetchCallRecorder{}
			bridge := &Bridge{webFetch: webFetchNetwork{
				timeout:         time.Second,
				lookupIPAddr:    rec.lookup(test.ips...),
				proxyForRequest: rec.directProxy(),
				dialContext:     rec.failDial(t),
			}}
			_, err := bridge.executeWebFetch("http://internal.example/")
			if err == nil || !strings.Contains(err.Error(), "web fetch host is not public-web accessible") {
				t.Fatalf("error = %v", err)
			}
			if got := rec.dialedAddrs(); len(got) != 0 {
				t.Fatalf("dialed = %v, want none", got)
			}
		})
	}
}

func TestWebFetchRedirectReevaluatesProxyAndDNS(t *testing.T) {
	t.Parallel()
	rec := &webFetchCallRecorder{}
	bridge := &Bridge{webFetch: webFetchNetwork{
		timeout: time.Second,
		lookupIPAddr: rec.lookupByHost(map[string][]string{
			"origin.example": {"8.8.8.8"},
			"cdn.example":    {"1.1.1.1"},
		}),
		proxyForRequest: rec.directProxy(),
		dialContext: rec.dialHTTP(func(_ string, req *http.Request) pipeHTTPResponse {
			switch req.Host {
			case "origin.example":
				return pipeHTTPResponse{
					status:  http.StatusFound,
					headers: http.Header{"Location": []string{"http://cdn.example/page"}},
				}
			case "cdn.example":
				return pipeHTTPResponse{body: "redirected-ok"}
			default:
				return pipeHTTPResponse{status: http.StatusBadRequest, body: "unexpected host " + req.Host}
			}
		}),
	}}
	markdown, err := bridge.executeWebFetch("http://origin.example/start")
	if err != nil {
		t.Fatalf("executeWebFetch: %v", err)
	}
	if !strings.Contains(markdown, "redirected-ok") {
		t.Fatalf("markdown = %q", markdown)
	}
	if got := rec.lookupHosts(); strings.Join(got, ",") != "origin.example,cdn.example" {
		t.Fatalf("lookup hosts = %v", got)
	}
	if got := rec.proxyHosts(); strings.Join(got, ",") != "origin.example,cdn.example" {
		t.Fatalf("proxy hosts = %v", got)
	}
	if got := rec.dialedAddrs(); strings.Join(got, ",") != "8.8.8.8:80,1.1.1.1:80" {
		t.Fatalf("dialed = %v", got)
	}
}

func TestWebFetchRedirectRejectsUserinfoAndNormalizedPrivateLiteral(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		location string
		wantErr  string
	}{
		{name: "userinfo", location: "http://user:pass@cdn.example/page", wantErr: "web fetch url must not include credentials"},
		{name: "trailing-dot private", location: "http://10.0.0.1./secret", wantErr: "web fetch host is not public-web accessible"},
		{name: "trailing-dot localhost", location: "http://localhost./secret", wantErr: "web fetch host is not public-web accessible"},
		{name: "site local", location: "http://[fec0::1]/", wantErr: "web fetch host is not public-web accessible"},
		{name: "ipv4 compatible", location: "http://[::2]/", wantErr: "web fetch host is not public-web accessible"},
		{name: "reserved 4000", location: "http://[4000::1]/", wantErr: "web fetch host is not public-web accessible"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rec := &webFetchCallRecorder{}
			bridge := &Bridge{webFetch: webFetchNetwork{
				timeout:         time.Second,
				lookupIPAddr:    rec.lookup("8.8.8.8"),
				proxyForRequest: rec.directProxy(),
				dialContext: rec.dialHTTP(func(_ string, req *http.Request) pipeHTTPResponse {
					return pipeHTTPResponse{
						status:  http.StatusFound,
						headers: http.Header{"Location": []string{test.location}},
					}
				}),
			}}
			_, err := bridge.executeWebFetch("http://origin.example/start")
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestWebFetchExplicitProxySkipsLocalResolveAndAllowsLoopbackProxy(t *testing.T) {
	t.Parallel()
	rec := &webFetchCallRecorder{}
	proxyURL := &neturl.URL{Scheme: "http", Host: "127.0.0.1:8080"}
	bridge := &Bridge{webFetch: webFetchNetwork{
		timeout: time.Second,
		lookupIPAddr: func(context.Context, string) ([]net.IPAddr, error) {
			t.Fatal("explicit proxy must not perform local DNS")
			return nil, fmt.Errorf("dns")
		},
		proxyForRequest: rec.fixedProxy(proxyURL),
		dialContext: rec.dialHTTP(func(address string, req *http.Request) pipeHTTPResponse {
			if address != "127.0.0.1:8080" {
				t.Errorf("dialed proxy %q, want 127.0.0.1:8080", address)
			}
			if req.Host != "example.com" {
				t.Errorf("Host = %q, want example.com", req.Host)
			}
			return pipeHTTPResponse{body: "via-proxy"}
		}),
	}}
	markdown, err := bridge.executeWebFetch("http://example.com/")
	if err != nil {
		t.Fatalf("executeWebFetch: %v", err)
	}
	if !strings.Contains(markdown, "via-proxy") {
		t.Fatalf("markdown = %q", markdown)
	}
	if got := rec.lookupHosts(); len(got) != 0 {
		t.Fatalf("lookup hosts = %v", got)
	}
}

func TestWebFetchExplicitProxyRejectsTrailingDotLocalhostWithoutDial(t *testing.T) {
	t.Parallel()
	rec := &webFetchCallRecorder{}
	proxyURL := &neturl.URL{Scheme: "http", Host: "127.0.0.1:8080"}
	bridge := &Bridge{webFetch: webFetchNetwork{
		timeout: time.Second,
		lookupIPAddr: func(context.Context, string) ([]net.IPAddr, error) {
			t.Fatal("blocked host must not perform local DNS")
			return nil, fmt.Errorf("dns")
		},
		proxyForRequest: rec.fixedProxy(proxyURL),
		dialContext:     rec.failDial(t),
	}}
	_, err := bridge.executeWebFetch("http://localhost./index")
	if err == nil || !strings.Contains(err.Error(), "web fetch host is not public-web accessible") {
		t.Fatalf("error = %v", err)
	}
	if got := rec.dialedAddrs(); len(got) != 0 {
		t.Fatalf("dialed = %v, want none", got)
	}
}

func TestWebFetchNoProxyUsesDirectPath(t *testing.T) {
	t.Parallel()
	rec := &webFetchCallRecorder{}
	proxyURL := &neturl.URL{Scheme: "http", Host: "127.0.0.1:8080"}
	bridge := &Bridge{webFetch: webFetchNetwork{
		timeout:         time.Second,
		lookupIPAddr:    rec.lookup("9.9.9.9"),
		proxyForRequest: rec.proxyExcept("bypass.example", proxyURL),
		dialContext: rec.dialHTTP(func(address string, req *http.Request) pipeHTTPResponse {
			if address == "127.0.0.1:8080" {
				t.Errorf("NO_PROXY host dialed proxy %s", address)
			}
			if req.Host != "bypass.example" {
				t.Errorf("Host = %q", req.Host)
			}
			return pipeHTTPResponse{body: "direct-bypass"}
		}),
	}}
	markdown, err := bridge.executeWebFetch("http://bypass.example/")
	if err != nil {
		t.Fatalf("executeWebFetch: %v", err)
	}
	if !strings.Contains(markdown, "direct-bypass") {
		t.Fatalf("markdown = %q", markdown)
	}
	if got := rec.lookupHosts(); strings.Join(got, ",") != "bypass.example" {
		t.Fatalf("lookup hosts = %v", got)
	}
	if got := rec.dialedAddrs(); strings.Join(got, ",") != "9.9.9.9:80" {
		t.Fatalf("dialed = %v", got)
	}
}

func TestWebFetchDirectFailureDoesNotFallbackToProxy(t *testing.T) {
	t.Parallel()
	rec := &webFetchCallRecorder{}
	bridge := &Bridge{webFetch: webFetchNetwork{
		timeout: time.Second,
		lookupIPAddr: func(context.Context, string) ([]net.IPAddr, error) {
			return nil, fmt.Errorf("nxdomain")
		},
		proxyForRequest: rec.directProxy(),
		dialContext:     rec.failDial(t),
	}}
	_, err := bridge.executeWebFetch("http://missing.example/")
	if err == nil || !strings.Contains(err.Error(), "web fetch dns lookup failed") {
		t.Fatalf("error = %v", err)
	}
	if got := rec.dialedAddrs(); len(got) != 0 {
		t.Fatalf("dialed = %v, want none", got)
	}
}

func TestWebFetchTimeoutBoundsDNS(t *testing.T) {
	t.Parallel()
	bridge := &Bridge{webFetch: webFetchNetwork{
		timeout: 40 * time.Millisecond,
		lookupIPAddr: func(ctx context.Context, _ string) ([]net.IPAddr, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		proxyForRequest: func(*http.Request) (*neturl.URL, error) { return nil, nil },
		dialContext: func(context.Context, string, string) (net.Conn, error) {
			t.Error("dial must not run after dns timeout")
			return nil, fmt.Errorf("dial")
		},
	}}
	start := time.Now()
	_, err := bridge.executeWebFetch("http://slow.example/")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed > time.Second {
		t.Fatalf("timeout not bounded: %v error=%v", elapsed, err)
	}
}

func TestWebFetchStopsAfter10Redirects(t *testing.T) {
	t.Parallel()
	rec := &webFetchCallRecorder{}
	var hops atomic.Int32
	bridge := &Bridge{webFetch: webFetchNetwork{
		timeout:         time.Second,
		lookupIPAddr:    rec.lookup("8.8.8.8"),
		proxyForRequest: rec.directProxy(),
		dialContext: rec.dialHTTP(func(_ string, req *http.Request) pipeHTTPResponse {
			n := hops.Add(1)
			return pipeHTTPResponse{
				status:  http.StatusFound,
				headers: http.Header{"Location": []string{fmt.Sprintf("http://next.example/h/%d", n)}},
			}
		}),
	}}
	_, err := bridge.executeWebFetch("http://next.example/start")
	if err == nil || !strings.Contains(err.Error(), "web fetch stopped after 10 redirects") {
		t.Fatalf("error = %v", err)
	}
}

func TestWebFetchBodyAndMarkdownLimits(t *testing.T) {
	t.Parallel()
	rec := &webFetchCallRecorder{}
	body := strings.Repeat("x", webFetchBodyLimit+64)
	bridge := &Bridge{webFetch: webFetchNetwork{
		timeout:         time.Second,
		lookupIPAddr:    rec.lookup("1.1.1.1"),
		proxyForRequest: rec.directProxy(),
		dialContext: rec.dialHTTP(func(_ string, _ *http.Request) pipeHTTPResponse {
			return pipeHTTPResponse{body: body}
		}),
	}}
	markdown, err := bridge.executeWebFetch("http://example.com/large")
	if err != nil {
		t.Fatalf("executeWebFetch: %v", err)
	}
	if len(markdown) > webFetchMarkdownLimit {
		t.Fatalf("markdown length = %d, want <= %d", len(markdown), webFetchMarkdownLimit)
	}
	if !strings.Contains(markdown, "truncated") {
		t.Fatalf("expected truncation notice, got %q", markdown[:min(len(markdown), 120)])
	}
}

func TestWebFetchLiteralPublicIPDoesNotLookup(t *testing.T) {
	t.Parallel()
	rec := &webFetchCallRecorder{}
	bridge := &Bridge{webFetch: webFetchNetwork{
		timeout: time.Second,
		lookupIPAddr: func(context.Context, string) ([]net.IPAddr, error) {
			t.Fatal("literal IP must not lookup DNS")
			return nil, fmt.Errorf("dns")
		},
		proxyForRequest: rec.directProxy(),
		dialContext: rec.dialHTTP(func(address string, req *http.Request) pipeHTTPResponse {
			if address != "8.8.8.8:80" {
				t.Errorf("dialed %q", address)
			}
			return pipeHTTPResponse{body: "literal-ok"}
		}),
	}}
	markdown, err := bridge.executeWebFetch("http://8.8.8.8/")
	if err != nil {
		t.Fatalf("executeWebFetch: %v", err)
	}
	if !strings.Contains(markdown, "literal-ok") {
		t.Fatalf("markdown = %q", markdown)
	}
}

func TestResolvePublicWebFetchIPsKeepsPublic6to4AndNAT64(t *testing.T) {
	t.Parallel()
	tests := []struct {
		host string
		want string
	}{
		{host: "2002:0808:0808::1234", want: "2002:808:808::1234"},
		{host: "64:ff9b::8.8.8.8", want: "64:ff9b::808:808"},
		{host: "::ffff:8.8.8.8", want: "8.8.8.8"},
	}
	for _, test := range tests {
		t.Run(test.host, func(t *testing.T) {
			t.Parallel()
			ips, err := resolvePublicWebFetchIPs(context.Background(), nil, test.host)
			if err != nil {
				t.Fatalf("resolvePublicWebFetchIPs: %v", err)
			}
			if len(ips) != 1 || ips[0].String() != test.want {
				t.Fatalf("ips = %v, want [%s]", ips, test.want)
			}
		})
	}

	for _, host := range []string{"fec0::1", "::2", "4000::1", "2002:0a00:0001::1", "64:ff9b::10.0.0.1"} {
		t.Run("reject "+host, func(t *testing.T) {
			t.Parallel()
			ips, err := resolvePublicWebFetchIPs(context.Background(), nil, host)
			if err == nil || !strings.Contains(err.Error(), "web fetch host is not public-web accessible") {
				t.Fatalf("error = %v ips = %v", err, ips)
			}
			if len(ips) != 0 {
				t.Fatalf("ips = %v, want none", ips)
			}
		})
	}
}

func TestWebFetchLiteralPublic6to4DialsOriginalIPv6(t *testing.T) {
	t.Parallel()
	rec := &webFetchCallRecorder{}
	wantDial := net.JoinHostPort("2002:808:808::1234", "80")
	bridge := &Bridge{webFetch: webFetchNetwork{
		timeout: time.Second,
		lookupIPAddr: func(context.Context, string) ([]net.IPAddr, error) {
			t.Fatal("literal 6to4 must not lookup DNS")
			return nil, fmt.Errorf("dns")
		},
		proxyForRequest: rec.directProxy(),
		dialContext: rec.dialHTTP(func(address string, req *http.Request) pipeHTTPResponse {
			if address != wantDial {
				t.Errorf("dialed %q, want %q", address, wantDial)
			}
			if req.Host != "[2002:808:808::1234]" && req.Host != "2002:0808:0808::1234" && !strings.HasPrefix(req.Host, "[2002:") {
				t.Errorf("Host = %q", req.Host)
			}
			return pipeHTTPResponse{body: "sixtofour-ok"}
		}),
	}}
	markdown, err := bridge.executeWebFetch("http://[2002:0808:0808::1234]/")
	if err != nil {
		t.Fatalf("executeWebFetch: %v", err)
	}
	if !strings.Contains(markdown, "sixtofour-ok") {
		t.Fatalf("markdown = %q", markdown)
	}
	if got := rec.dialedAddrs(); len(got) != 1 || got[0] != wantDial {
		t.Fatalf("dialed = %v, want [%s]", got, wantDial)
	}
}

func TestWebFetchDNSPublic6to4DialsOriginalIPv6(t *testing.T) {
	t.Parallel()
	rec := &webFetchCallRecorder{}
	wantDial := net.JoinHostPort("2002:808:808::1234", "80")
	bridge := &Bridge{webFetch: webFetchNetwork{
		timeout:         time.Second,
		lookupIPAddr:    rec.lookup("2002:0808:0808::1234"),
		proxyForRequest: rec.directProxy(),
		dialContext: rec.dialHTTP(func(address string, req *http.Request) pipeHTTPResponse {
			if address != wantDial {
				t.Errorf("dialed %q, want %q", address, wantDial)
			}
			if req.Host != "sixtofour.example" {
				t.Errorf("Host = %q, want sixtofour.example", req.Host)
			}
			return pipeHTTPResponse{body: "dns-sixtofour-ok"}
		}),
	}}
	markdown, err := bridge.executeWebFetch("http://sixtofour.example/")
	if err != nil {
		t.Fatalf("executeWebFetch: %v", err)
	}
	if !strings.Contains(markdown, "dns-sixtofour-ok") {
		t.Fatalf("markdown = %q", markdown)
	}
	if got := rec.dialedAddrs(); len(got) != 1 || got[0] != wantDial {
		t.Fatalf("dialed = %v, want [%s]", got, wantDial)
	}
}

func TestWebFetchHTTPSPreservesHostAndTLSServerName(t *testing.T) {
	t.Parallel()
	certificate, roots := testWebFetchTLSCertificate(t, "secure.example")
	rec := &webFetchCallRecorder{}
	var serverName string
	bridge := &Bridge{webFetch: webFetchNetwork{
		timeout:         time.Second,
		lookupIPAddr:    rec.lookup("8.8.8.8"),
		proxyForRequest: rec.directProxy(),
		tlsClientConfig: &tls.Config{RootCAs: roots, NextProtos: []string{"http/1.1"}},
		dialContext: rec.dialTLS(certificate, func(hello *tls.ClientHelloInfo, req *http.Request) pipeHTTPResponse {
			if hello != nil {
				serverName = hello.ServerName
			}
			if req.Host != "secure.example" {
				t.Errorf("Host = %q, want secure.example", req.Host)
			}
			return pipeHTTPResponse{body: "tls-ok"}
		}),
	}}
	markdown, err := bridge.executeWebFetch("https://secure.example/path")
	if err != nil {
		t.Fatalf("executeWebFetch: %v", err)
	}
	if !strings.Contains(markdown, "tls-ok") {
		t.Fatalf("markdown = %q", markdown)
	}
	if serverName != "secure.example" {
		t.Fatalf("TLS ServerName = %q, want secure.example", serverName)
	}
	if got := rec.dialedAddrs(); len(got) != 1 || got[0] != "8.8.8.8:443" {
		t.Fatalf("dialed = %v, want [8.8.8.8:443]", got)
	}
}

func TestWebFetchExplicitProxyRejectsReservedLiteralWithoutDial(t *testing.T) {
	t.Parallel()
	for _, rawURL := range []string{"http://[fec0::1]/", "http://[::2]/", "http://[4000::1]/"} {
		t.Run(rawURL, func(t *testing.T) {
			t.Parallel()
			rec := &webFetchCallRecorder{}
			proxyURL := &neturl.URL{Scheme: "http", Host: "127.0.0.1:8080"}
			bridge := &Bridge{webFetch: webFetchNetwork{
				timeout: time.Second,
				lookupIPAddr: func(context.Context, string) ([]net.IPAddr, error) {
					t.Fatal("blocked literal must not perform local DNS")
					return nil, fmt.Errorf("dns")
				},
				proxyForRequest: rec.fixedProxy(proxyURL),
				dialContext:     rec.failDial(t),
			}}
			_, err := bridge.executeWebFetch(rawURL)
			if err == nil || !strings.Contains(err.Error(), "web fetch host is not public-web accessible") {
				t.Fatalf("error = %v", err)
			}
			if got := rec.dialedAddrs(); len(got) != 0 {
				t.Fatalf("dialed = %v, want none", got)
			}
		})
	}
}

func TestApplyWebFetchResponseSuccessUsesDedicatedNetwork(t *testing.T) {
	t.Parallel()
	rec := &webFetchCallRecorder{}
	bridge := &Bridge{webFetch: webFetchNetwork{
		timeout:         time.Second,
		lookupIPAddr:    rec.lookup("8.8.8.8"),
		proxyForRequest: rec.directProxy(),
		dialContext: rec.dialHTTP(func(_ string, _ *http.Request) pipeHTTPResponse {
			return pipeHTTPResponse{body: "ok"}
		}),
	}}
	result, payload := bridge.applyWebFetchResponse(&agentv1.WebFetchRequestResponse{
		Result: &agentv1.WebFetchRequestResponse_Approved_{
			Approved: &agentv1.WebFetchRequestResponse_Approved{},
		},
	}, &agentv1.WebFetchArgs{Url: "http://example.com/"})
	if result.GetSuccess() == nil || !strings.Contains(payload, "ok") {
		t.Fatalf("result = %#v payload = %q", result, payload)
	}
}

type webFetchCallRecorder struct {
	mu      sync.Mutex
	lookups []string
	dials   []string
	proxies []string
}

func (rec *webFetchCallRecorder) lookupHosts() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]string(nil), rec.lookups...)
}

func (rec *webFetchCallRecorder) dialedAddrs() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]string(nil), rec.dials...)
}

func (rec *webFetchCallRecorder) proxyHosts() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]string(nil), rec.proxies...)
}

func (rec *webFetchCallRecorder) lookup(ips ...string) func(context.Context, string) ([]net.IPAddr, error) {
	return rec.lookupByHost(nil, ips...)
}

func (rec *webFetchCallRecorder) lookupByHost(hosts map[string][]string, defaultIPs ...string) func(context.Context, string) ([]net.IPAddr, error) {
	return func(_ context.Context, host string) ([]net.IPAddr, error) {
		rec.mu.Lock()
		rec.lookups = append(rec.lookups, host)
		rec.mu.Unlock()
		values := defaultIPs
		if hosts != nil {
			if mapped, ok := hosts[host]; ok {
				values = mapped
			} else if len(defaultIPs) == 0 {
				return nil, fmt.Errorf("unexpected host %s", host)
			}
		}
		addrs := make([]net.IPAddr, 0, len(values))
		for _, value := range values {
			ip := net.ParseIP(value)
			if ip == nil {
				return nil, fmt.Errorf("invalid test ip %q", value)
			}
			addrs = append(addrs, net.IPAddr{IP: ip})
		}
		return addrs, nil
	}
}

func (rec *webFetchCallRecorder) directProxy() func(*http.Request) (*neturl.URL, error) {
	return rec.fixedProxy(nil)
}

func (rec *webFetchCallRecorder) fixedProxy(proxyURL *neturl.URL) func(*http.Request) (*neturl.URL, error) {
	return func(request *http.Request) (*neturl.URL, error) {
		rec.mu.Lock()
		if request != nil && request.URL != nil {
			rec.proxies = append(rec.proxies, request.URL.Hostname())
		}
		rec.mu.Unlock()
		return proxyURL, nil
	}
}

func (rec *webFetchCallRecorder) proxyExcept(directHost string, proxyURL *neturl.URL) func(*http.Request) (*neturl.URL, error) {
	return func(request *http.Request) (*neturl.URL, error) {
		rec.mu.Lock()
		if request != nil && request.URL != nil {
			rec.proxies = append(rec.proxies, request.URL.Hostname())
		}
		rec.mu.Unlock()
		if request != nil && request.URL != nil && request.URL.Hostname() == directHost {
			return nil, nil
		}
		return proxyURL, nil
	}
}

func (rec *webFetchCallRecorder) failDial(t *testing.T) func(context.Context, string, string) (net.Conn, error) {
	t.Helper()
	return func(_ context.Context, _, address string) (net.Conn, error) {
		rec.mu.Lock()
		rec.dials = append(rec.dials, address)
		rec.mu.Unlock()
		t.Errorf("unexpected dial %s", address)
		return nil, fmt.Errorf("dial forbidden")
	}
}

type pipeHTTPResponse struct {
	status  int
	headers http.Header
	body    string
}

func (rec *webFetchCallRecorder) dialHTTP(action func(address string, req *http.Request) pipeHTTPResponse) func(context.Context, string, string) (net.Conn, error) {
	return rec.dialPipe(nil, func(_ *tls.ClientHelloInfo, address string, req *http.Request) pipeHTTPResponse {
		return action(address, req)
	})
}

func (rec *webFetchCallRecorder) dialTLS(certificate tls.Certificate, action func(hello *tls.ClientHelloInfo, req *http.Request) pipeHTTPResponse) func(context.Context, string, string) (net.Conn, error) {
	return rec.dialPipe(&certificate, func(hello *tls.ClientHelloInfo, _ string, req *http.Request) pipeHTTPResponse {
		return action(hello, req)
	})
}

func (rec *webFetchCallRecorder) dialPipe(certificate *tls.Certificate, action func(hello *tls.ClientHelloInfo, address string, req *http.Request) pipeHTTPResponse) func(context.Context, string, string) (net.Conn, error) {
	return func(_ context.Context, _, address string) (net.Conn, error) {
		rec.mu.Lock()
		rec.dials = append(rec.dials, address)
		rec.mu.Unlock()
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			conn := net.Conn(server)
			var hello *tls.ClientHelloInfo
			if certificate != nil {
				tlsConn := tls.Server(server, &tls.Config{
					Certificates: []tls.Certificate{*certificate},
					NextProtos:   []string{"http/1.1"},
					GetConfigForClient: func(info *tls.ClientHelloInfo) (*tls.Config, error) {
						copied := *info
						hello = &copied
						return nil, nil
					},
				})
				if err := tlsConn.Handshake(); err != nil {
					return
				}
				conn = tlsConn
			}
			req, err := http.ReadRequest(bufio.NewReader(conn))
			if err != nil {
				return
			}
			writePipeHTTPResponse(conn, action(hello, address, req))
		}()
		return client, nil
	}
}

func testWebFetchTLSCertificate(t *testing.T, dnsName string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: dnsName},
		DNSNames:     []string{dnsName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func writePipeHTTPResponse(writer io.Writer, resp pipeHTTPResponse) {
	if resp.status == 0 {
		resp.status = http.StatusOK
	}
	if resp.headers == nil {
		resp.headers = make(http.Header)
	}
	if resp.headers.Get("Content-Type") == "" {
		resp.headers.Set("Content-Type", "text/plain; charset=utf-8")
	}
	resp.headers.Set("Content-Length", strconv.Itoa(len(resp.body)))
	resp.headers.Set("Connection", "close")
	_, _ = fmt.Fprintf(writer, "HTTP/1.1 %d %s\r\n", resp.status, http.StatusText(resp.status))
	_ = resp.headers.Write(writer)
	_, _ = io.WriteString(writer, "\r\n")
	_, _ = io.WriteString(writer, resp.body)
}
