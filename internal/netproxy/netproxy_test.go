package netproxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestNormalizeProviderTransportProfile(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "", want: ProviderTransportProfileAuto},
		{input: " AUTO ", want: ProviderTransportProfileAuto},
		{input: "HTTP1", want: ProviderTransportProfileHTTP1},
		{input: "no_compression", want: ProviderTransportProfileNoCompression},
		{input: "fresh_connection", want: ProviderTransportProfileFreshConnection},
		{input: "direct", want: ProviderTransportProfileDirect},
		{input: "http1,no_compression", want: ProviderTransportProfileAuto},
		{input: "unknown", want: ProviderTransportProfileAuto},
	} {
		if got := NormalizeProviderTransportProfile(test.input); got != test.want {
			t.Fatalf("NormalizeProviderTransportProfile(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestNewProviderTransportAppliesOneExperiment(t *testing.T) {
	t.Parallel()
	base := initialDefaultTransport.Clone()
	base.DisableCompression = false
	base.DisableKeepAlives = false

	tests := []struct {
		name    string
		profile string
		check   func(*testing.T, *http.Transport)
	}{
		{name: "auto", profile: ProviderTransportProfileAuto, check: func(t *testing.T, transport *http.Transport) {
			if transport.Proxy == nil || transport.DisableCompression || transport.DisableKeepAlives {
				t.Fatalf("auto transport changed defaults: %#v", transport)
			}
		}},
		{name: "http1", profile: ProviderTransportProfileHTTP1, check: func(t *testing.T, transport *http.Transport) {
			if transport.Protocols == nil || !transport.Protocols.HTTP1() || transport.Protocols.HTTP2() || transport.ForceAttemptHTTP2 || transport.DisableCompression || transport.DisableKeepAlives || transport.Proxy == nil {
				t.Fatalf("http1 transport mixed profiles: %#v", transport)
			}
		}},
		{name: "no compression", profile: ProviderTransportProfileNoCompression, check: func(t *testing.T, transport *http.Transport) {
			if !transport.DisableCompression || transport.DisableKeepAlives || transport.Proxy == nil {
				t.Fatalf("no_compression transport mixed profiles: %#v", transport)
			}
		}},
		{name: "fresh connection", profile: ProviderTransportProfileFreshConnection, check: func(t *testing.T, transport *http.Transport) {
			if transport.DisableCompression || !transport.DisableKeepAlives || transport.Proxy == nil {
				t.Fatalf("fresh_connection transport mixed profiles: %#v", transport)
			}
		}},
		{name: "direct", profile: ProviderTransportProfileDirect, check: func(t *testing.T, transport *http.Transport) {
			if transport.Proxy != nil || transport.DisableCompression || transport.DisableKeepAlives {
				t.Fatalf("direct transport mixed profiles: %#v", transport)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := NewProviderTransport(base, test.profile)
			if transport == base {
				t.Fatal("provider transport mutated caller-owned base")
			}
			test.check(t, transport)
		})
	}
}

func TestNewProviderHTTPClientReadsProfileEnvironment(t *testing.T) {
	t.Setenv(ProviderTransportProfileEnv, ProviderTransportProfileNoCompression)
	client := NewProviderHTTPClient(3 * time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("client transport = %T", client.Transport)
	}
	if client.Timeout != 3*time.Second || !transport.DisableCompression {
		t.Fatalf("provider client timeout/profile = %v/%#v", client.Timeout, transport)
	}
}

func TestProviderHTTP1ProfileNegotiatesHTTP1(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	base, ok := server.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("test server transport = %T", server.Client().Transport)
	}
	client := &http.Client{Transport: NewProviderTransport(base, ProviderTransportProfileHTTP1)}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("HTTP/1 profile request: %v", err)
	}
	_ = response.Body.Close()
	if response.ProtoMajor != 1 {
		t.Fatalf("response protocol = %s, want HTTP/1.x", response.Proto)
	}
}

func TestProxyForRequestHonorsEnvProxyAndNoProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:8080")
	t.Setenv("http_proxy", "http://127.0.0.1:8080")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:8443")
	t.Setenv("https_proxy", "http://127.0.0.1:8443")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	t.Setenv("NO_PROXY", "bypass.example")
	t.Setenv("no_proxy", "bypass.example")
	t.Setenv("REQUEST_METHOD", "")
	resetProxyResolverForTest()

	proxied, err := ProxyForRequest(&http.Request{URL: mustParseURL(t, "http://target.example/path")})
	if err != nil {
		t.Fatalf("ProxyForRequest(http target): %v", err)
	}
	if proxied == nil || proxied.Host != "127.0.0.1:8080" {
		t.Fatalf("http proxy = %v, want 127.0.0.1:8080", proxied)
	}

	httpsProxied, err := ProxyForRequest(&http.Request{URL: mustParseURL(t, "https://target.example/")})
	if err != nil {
		t.Fatalf("ProxyForRequest(https target): %v", err)
	}
	if httpsProxied == nil || httpsProxied.Host != "127.0.0.1:8443" {
		t.Fatalf("https proxy = %v, want 127.0.0.1:8443", httpsProxied)
	}

	direct, err := ProxyForRequest(&http.Request{URL: mustParseURL(t, "http://bypass.example/")})
	if err != nil {
		t.Fatalf("ProxyForRequest(no_proxy): %v", err)
	}
	if direct != nil {
		t.Fatalf("no_proxy host still proxied: %v", direct)
	}

	loopback, err := ProxyForRequest(&http.Request{URL: mustParseURL(t, "http://127.0.0.1/")})
	if err != nil {
		t.Fatalf("ProxyForRequest(loopback): %v", err)
	}
	if loopback != nil {
		t.Fatalf("loopback still proxied: %v", loopback)
	}
}

func TestProxyForRequestNilRequest(t *testing.T) {
	t.Parallel()
	proxyURL, err := ProxyForRequest(nil)
	if proxyURL != nil || err != nil {
		t.Fatalf("ProxyForRequest(nil) = %v, %v", proxyURL, err)
	}
}

func resetProxyResolverForTest() {
	defaultResolver.mu.Lock()
	defaultResolver.snapshot = proxySnapshot{}
	defaultResolver.mu.Unlock()
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	return parsed
}
