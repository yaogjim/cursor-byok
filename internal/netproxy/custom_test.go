package netproxy

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeAndParseOutboundProxyURL(t *testing.T) {
	disabled, err := Normalize(Config{Enabled: false, URL: " ftp://bad "})
	if err != nil {
		t.Fatalf("disabled normalize: %v", err)
	}
	if disabled.Enabled || disabled.URL != "ftp://bad" {
		t.Fatalf("disabled retained URL = %+v", disabled)
	}

	enabled, err := Normalize(Config{Enabled: true, URL: " http://user:s3cret@proxy.example:8080 "})
	if err != nil {
		t.Fatalf("enabled normalize: %v", err)
	}
	if !enabled.Enabled || enabled.URL != "http://user:s3cret@proxy.example:8080" {
		t.Fatalf("enabled = %+v", enabled)
	}

	for _, raw := range []string{"", "ftp://proxy.example", "http://", "socks5://", "example.com:8080"} {
		if _, err := Normalize(Config{Enabled: true, URL: raw}); err == nil {
			t.Fatalf("Normalize(%q) accepted invalid enabled URL", raw)
		} else if strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), raw) && raw != "" && strings.Contains(raw, "://") && raw != "ftp://proxy.example" {
			if strings.Contains(err.Error(), "s3cret") {
				t.Fatalf("error leaked credential: %v", err)
			}
		}
	}
	if _, err := Normalize(Config{Enabled: true, URL: "http://user:s3cret@"}); err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("invalid enabled URL error = %v", err)
	}
}

func TestEffectiveProxyPrecedenceForHash(t *testing.T) {
	model := Config{Enabled: true, URL: "http://model.example:1"}
	global := Config{Enabled: true, URL: "http://global.example:2"}
	got := Effective(model, global)
	if !got.Enabled || got.URL != model.URL {
		t.Fatalf("model custom should win: %+v", got)
	}
	got = Effective(Config{Enabled: false, URL: "http://unused.example"}, global)
	if !got.Enabled || got.URL != global.URL {
		t.Fatalf("inherit global: %+v", got)
	}
	got = Effective(Config{Enabled: false, URL: "http://unused.example"}, Config{Enabled: false, URL: "http://saved.example"})
	if got.Enabled || got.URL != "" {
		t.Fatalf("disabled hash URL must be empty: %+v", got)
	}
}

func TestProxySelectorRequestGlobalEnvDirect(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:18001")
	t.Setenv("http_proxy", "http://127.0.0.1:18001")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	t.Setenv("NO_PROXY", "bypass.example")
	t.Setenv("no_proxy", "bypass.example")
	t.Setenv("REQUEST_METHOD", "")
	resetProxyResolverForTest()
	t.Cleanup(resetProxyResolverForTest)

	target := mustParseURL(t, "http://target.example/path")
	envProxy, err := ProxyForRequest(&http.Request{URL: target})
	if err != nil || envProxy == nil || envProxy.Host != "127.0.0.1:18001" {
		t.Fatalf("env proxy = %v, %v", envProxy, err)
	}

	SetGlobal(Config{Enabled: true, URL: "http://127.0.0.1:18002"})
	globalProxy, err := ProxyForRequest(&http.Request{URL: target})
	if err != nil || globalProxy == nil || globalProxy.Host != "127.0.0.1:18002" {
		t.Fatalf("global proxy = %v, %v", globalProxy, err)
	}
	status := CurrentStatus()
	if status.Source != "custom" || !status.UsingCustomProxy || !status.Active {
		t.Fatalf("status = %+v", status)
	}
	if strings.Contains(status.Description, "user:") || strings.Contains(status.HTTPProxy, "@") {
		t.Fatalf("status leaked credentials: %+v", status)
	}

	req := AttachRequest(&http.Request{URL: target}, Config{Enabled: true, URL: "http://127.0.0.1:18003"})
	requestProxy, err := ProxyForRequest(req)
	if err != nil || requestProxy == nil || requestProxy.Host != "127.0.0.1:18003" {
		t.Fatalf("request proxy = %v, %v", requestProxy, err)
	}

	inheritOff := AttachRequest(&http.Request{URL: target}, Config{Enabled: false, URL: "http://127.0.0.1:18009"})
	inherited, err := ProxyForRequest(inheritOff)
	if err != nil || inherited == nil || inherited.Host != "127.0.0.1:18002" {
		t.Fatalf("disabled request must inherit global: %v, %v", inherited, err)
	}

	bypass, err := ProxyForRequest(&http.Request{URL: mustParseURL(t, "http://bypass.example/")})
	if err != nil || bypass == nil || bypass.Host != "127.0.0.1:18002" {
		t.Fatalf("custom must ignore NO_PROXY: %v, %v", bypass, err)
	}

	loopback, err := ProxyForRequest(&http.Request{URL: mustParseURL(t, "http://127.0.0.1/healthz")})
	if err != nil || loopback != nil {
		t.Fatalf("loopback must stay direct: %v, %v", loopback, err)
	}

	SetGlobal(Config{Enabled: false, URL: "http://127.0.0.1:18002"})
	restored, err := ProxyForRequest(&http.Request{URL: target})
	if err != nil || restored == nil || restored.Host != "127.0.0.1:18001" {
		t.Fatalf("disabled global must restore env: %v, %v", restored, err)
	}
	if CurrentStatus().UsingCustomProxy {
		t.Fatalf("disabled global still marked custom: %+v", CurrentStatus())
	}
}

func TestCustomProxyDoesNotFallBackToEnv(t *testing.T) {
	var envHits atomic.Int32
	envProxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		envHits.Add(1)
	}))
	t.Cleanup(envProxy.Close)

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	t.Setenv("HTTP_PROXY", envProxy.URL)
	t.Setenv("http_proxy", envProxy.URL)
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	t.Setenv("REQUEST_METHOD", "")
	resetProxyResolverForTest()
	t.Cleanup(resetProxyResolverForTest)
	SetGlobal(Config{Enabled: true, URL: deadURL})

	client := NewHTTPClient(2 * time.Second)
	_, err := client.Get("http://provider.example/v1/models")
	if err == nil {
		t.Fatal("dead custom proxy must fail")
	}
	if envHits.Load() != 0 {
		t.Fatalf("env proxy hits = %d, want 0 (no silent fallback)", envHits.Load())
	}
	if strings.Contains(err.Error(), "user:") {
		t.Fatalf("error leaked proxy userinfo: %v", err)
	}
}

func TestInvalidRequestCustomDoesNotFallBack(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:18001")
	t.Setenv("http_proxy", "http://127.0.0.1:18001")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	t.Setenv("REQUEST_METHOD", "")
	resetProxyResolverForTest()
	t.Cleanup(resetProxyResolverForTest)

	req := AttachRequest(&http.Request{URL: mustParseURL(t, "http://target.example/")}, Config{Enabled: true, URL: "http://user:s3cret@"})
	proxyURL, err := ProxyForRequest(req)
	if err == nil || proxyURL != nil {
		t.Fatalf("invalid custom must error without fallback: %v, %v", proxyURL, err)
	}
	if !strings.Contains(err.Error(), "outboundProxy.url") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("error leaked credential: %v", err)
	}
}

func TestConcurrentRequestCustomIsolation(t *testing.T) {
	resetProxyResolverForTest()
	t.Cleanup(resetProxyResolverForTest)

	var (
		mu    sync.Mutex
		hosts = map[string][]string{}
	)
	newProxy := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			mu.Lock()
			hosts[request.Host] = append(hosts[request.Host], request.URL.String())
			mu.Unlock()
			writer.WriteHeader(http.StatusNoContent)
		}))
	}
	proxyA := newProxy()
	proxyB := newProxy()
	t.Cleanup(proxyA.Close)
	t.Cleanup(proxyB.Close)

	client := NewHTTPClient(5 * time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodGet, "http://alpha.example/one", nil)
			if err != nil {
				t.Errorf("new request: %v", err)
				return
			}
			resp, err := client.Do(AttachRequest(req, Config{Enabled: true, URL: proxyA.URL}))
			if err != nil {
				t.Errorf("alpha: %v", err)
				return
			}
			_ = resp.Body.Close()
		}()
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodGet, "http://beta.example/two", nil)
			if err != nil {
				t.Errorf("new request: %v", err)
				return
			}
			resp, err := client.Do(AttachRequest(req, Config{Enabled: true, URL: proxyB.URL}))
			if err != nil {
				t.Errorf("beta: %v", err)
				return
			}
			_ = resp.Body.Close()
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(hosts["alpha.example"]) == 0 || len(hosts["beta.example"]) == 0 {
		t.Fatalf("hosts = %#v", hosts)
	}
	for _, url := range hosts["alpha.example"] {
		if strings.Contains(url, "beta.example") {
			t.Fatalf("proxy A saw beta: %s", url)
		}
	}
	for _, url := range hosts["beta.example"] {
		if strings.Contains(url, "alpha.example") {
			t.Fatalf("proxy B saw alpha: %s", url)
		}
	}
}

func TestGlobalUpdateClosesIdleAndLeavesInflight(t *testing.T) {
	resetProxyResolverForTest()
	t.Cleanup(resetProxyResolverForTest)

	started := make(chan struct{})
	release := make(chan struct{})
	var firstHits, secondHits atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		firstHits.Add(1)
		close(started)
		<-release
		writer.WriteHeader(http.StatusNoContent)
	}))
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		secondHits.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)

	SetGlobal(Config{Enabled: true, URL: first.URL})
	client := NewHTTPClient(5 * time.Second)

	done := make(chan error, 1)
	go func() {
		resp, err := client.Get("http://inflight.example/")
		if err != nil {
			done <- err
			return
		}
		_ = resp.Body.Close()
		done <- nil
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("inflight request did not start")
	}

	SetGlobal(Config{Enabled: true, URL: second.URL})
	resp, err := client.Get("http://next.example/")
	if err != nil {
		t.Fatalf("post-update request: %v", err)
	}
	_ = resp.Body.Close()
	if secondHits.Load() == 0 {
		t.Fatal("new request did not use updated global proxy")
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("inflight request: %v", err)
	}
	if firstHits.Load() == 0 {
		t.Fatal("inflight request was interrupted or rerouted")
	}
}

func TestRealLocalHTTPProxyRoundTrip(t *testing.T) {
	resetProxyResolverForTest()
	t.Cleanup(resetProxyResolverForTest)

	var saw atomic.Value
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		saw.Store(request.URL.String())
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(writer, "via-http-proxy")
	}))
	t.Cleanup(proxy.Close)
	SetGlobal(Config{Enabled: true, URL: proxy.URL})

	client := NewHTTPClient(5 * time.Second)
	resp, err := client.Get("http://provider.example/v1/models")
	if err != nil {
		t.Fatalf("http proxy roundtrip: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "via-http-proxy" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	got, _ := saw.Load().(string)
	if !strings.Contains(got, "provider.example") {
		t.Fatalf("proxy URL = %q", got)
	}
}

func TestRealLocalSOCKS5ProxyRoundTrip(t *testing.T) {
	resetProxyResolverForTest()
	t.Cleanup(resetProxyResolverForTest)

	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "via-socks")
	}))
	t.Cleanup(backend.Close)
	_, backendPort, err := net.SplitHostPort(strings.TrimPrefix(backend.URL, "http://"))
	if err != nil {
		t.Fatalf("backend addr: %v", err)
	}

	var destinations []string
	var destMu sync.Mutex
	socksURL := startSOCKS5Proxy(t, func(network, address string) (net.Conn, error) {
		destMu.Lock()
		destinations = append(destinations, address)
		destMu.Unlock()
		return net.DialTimeout(network, net.JoinHostPort("127.0.0.1", backendPort), 2*time.Second)
	})
	SetGlobal(Config{Enabled: true, URL: socksURL})

	client := NewHTTPClient(5 * time.Second)
	resp, err := client.Get("http://provider.test:" + backendPort + "/v1/models")
	if err != nil {
		t.Fatalf("socks5 roundtrip: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "via-socks" {
		t.Fatalf("body = %q", body)
	}
	destMu.Lock()
	defer destMu.Unlock()
	if len(destinations) == 0 || !strings.Contains(destinations[0], "provider.test") {
		t.Fatalf("socks destinations = %#v", destinations)
	}
}

func TestProviderDirectProfileUsesCustomNotEnv(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:18001")
	t.Setenv("http_proxy", "http://127.0.0.1:18001")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	t.Setenv("REQUEST_METHOD", "")
	resetProxyResolverForTest()
	t.Cleanup(resetProxyResolverForTest)

	target := mustParseURL(t, "http://target.example/")
	transport := NewProviderTransport(nil, ProviderTransportProfileDirect)
	if transport.Proxy == nil {
		t.Fatal("direct profile dropped proxy func")
	}
	auto, err := transport.Proxy(&http.Request{URL: target})
	if err != nil || auto != nil {
		t.Fatalf("direct without custom should skip env: %v, %v", auto, err)
	}

	SetGlobal(Config{Enabled: true, URL: "http://127.0.0.1:18004"})
	custom, err := transport.Proxy(&http.Request{URL: target})
	if err != nil || custom == nil || custom.Host != "127.0.0.1:18004" {
		t.Fatalf("direct must honor global custom: %v, %v", custom, err)
	}

	req := AttachRequest(&http.Request{URL: target}, Config{Enabled: true, URL: "http://127.0.0.1:18005"})
	requestCustom, err := transport.Proxy(req)
	if err != nil || requestCustom == nil || requestCustom.Host != "127.0.0.1:18005" {
		t.Fatalf("direct must honor request custom: %v, %v", requestCustom, err)
	}
}

func startSOCKS5Proxy(t *testing.T, dial func(network, address string) (net.Conn, error)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen socks5: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleSOCKS5(conn, dial)
		}
	}()
	return "socks5://" + listener.Addr().String()
}

func handleSOCKS5(client net.Conn, dial func(network, address string) (net.Conn, error)) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	header := make([]byte, 2)
	if _, err := io.ReadFull(client, header); err != nil || header[0] != 5 {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(client, methods); err != nil {
		return
	}
	if _, err := client.Write([]byte{5, 0}); err != nil {
		return
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(client, req); err != nil || req[0] != 5 || req[1] != 1 {
		return
	}
	var host string
	switch req[3] {
	case 1:
		addr := make([]byte, 4)
		if _, err := io.ReadFull(client, addr); err != nil {
			return
		}
		host = net.IP(addr).String()
	case 3:
		var size [1]byte
		if _, err := io.ReadFull(client, size[:]); err != nil {
			return
		}
		name := make([]byte, int(size[0]))
		if _, err := io.ReadFull(client, name); err != nil {
			return
		}
		host = string(name)
	case 4:
		addr := make([]byte, 16)
		if _, err := io.ReadFull(client, addr); err != nil {
			return
		}
		host = net.IP(addr).String()
	default:
		return
	}
	var portBuf [2]byte
	if _, err := io.ReadFull(client, portBuf[:]); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(portBuf[:])
	target := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	remote, err := dial("tcp", target)
	if err != nil {
		_, _ = client.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer remote.Close()
	if _, err := client.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	_ = remote.SetDeadline(time.Time{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(remote, client)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, remote)
	}()
	wg.Wait()
}
