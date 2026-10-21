package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	modeladapter "cursor/internal/backend/agent/model"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/netproxy"
	"cursor/internal/subscriptionauth"
)

func TestNormalizeModelAdapterTestProviderReasoningPreservesBlank(t *testing.T) {
	adapter := serverconfig.ModelAdapterConfig{Type: "openai", ReasoningEffort: ""}

	if got := normalizeModelAdapterTestProviderReasoning(adapter); got != "" {
		t.Fatalf("reasoning effort = %q, want blank", got)
	}
}

func TestModelAdapterManagedResolvesTokenWithoutWritingBack(t *testing.T) {
	const token = "managed-test-token-secret"
	stubModelAdapterCredentialForSource(t, subscriptionauth.CredentialSourceCodex, subscriptionauth.Credential{
		AccessToken:      token,
		AccountID:        "credential-account",
		ChatGPTAccountID: "chatgpt-account",
	}, nil)

	originalStream := streamModelAdapterTestOpenAI
	t.Cleanup(func() { streamModelAdapterTestOpenAI = originalStream })
	var captured modeladapter.StreamRequest
	streamModelAdapterTestOpenAI = func(_ context.Context, req modeladapter.StreamRequest, sink func(modeladapter.ModelEvent) error) error {
		captured = req
		if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "done"}); err != nil {
			return err
		}
		return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTurnFinished, OutputTokens: 1})
	}

	adapter := serverconfig.ModelAdapterConfig{
		DisplayName:      "Managed Codex",
		Type:             "openai",
		BaseURL:          "http://127.0.0.1:18091/v1",
		CredentialSource: "codex",
		TooltipData:      "备注",
		ModelID:          "gpt-5.1",
		OpenAIEndpoint:   "/v1/chat/completions",
	}
	service := &ProxyService{modelTestResults: map[string]ModelAdapterTestResult{}}
	result, err := service.TestModelAdapter(adapter)
	if err != nil {
		t.Fatalf("TestModelAdapter 返回错误：%v", err)
	}
	if result.Status != string(ModelAdapterTestStatusSuccess) {
		t.Fatalf("status = %q error=%q raw=%q", result.Status, result.Error, result.RawResponse)
	}
	if captured.BaseURL != subscriptionauth.CodexResponsesURL || captured.OpenAIEndpoint != "/v1/responses" {
		t.Fatalf("Codex 测试地址 = %q %q", captured.BaseURL, captured.OpenAIEndpoint)
	}
	if captured.APIKey != token || captured.CredentialID != "credential-account" || captured.ChatGPTAccountID != "chatgpt-account" {
		t.Fatalf("运行时凭据元数据不匹配：key=%t account=%q chatgptAccount=%q", captured.APIKey == token, captured.CredentialID, captured.ChatGPTAccountID)
	}
	if adapter.APIKey != "" || adapter.BaseURL != "http://127.0.0.1:18091/v1" {
		t.Fatalf("原始 adapter 被写回：apiKey=%q baseURL=%q", adapter.APIKey, adapter.BaseURL)
	}
	encoded, err := json.Marshal(service.GetModelAdapterTestResults())
	if err != nil {
		t.Fatalf("marshal stored results: %v", err)
	}
	if bytes.Contains(encoded, []byte(token)) {
		t.Fatalf("测速缓存泄漏了临时 token：%s", encoded)
	}
}

func TestModelAdapterTestRequestHashIncludesOpenAIImageGenerationEnabled(t *testing.T) {
	base := serverconfig.ModelAdapterConfig{
		DisplayName:    "gpt",
		Type:           "openai",
		BaseURL:        "https://api.example.com/v1",
		APIKey:         "test-key",
		TooltipData:    "gpt",
		ModelID:        "gpt-test",
		OpenAIEndpoint: "/v1/responses",
	}
	off := buildModelAdapterTestRequestHash(base)
	enabled := base
	enabled.OpenAIImageGenerationEnabled = true
	on := buildModelAdapterTestRequestHash(enabled)
	if off == "" || off == on {
		t.Fatalf("toggling OpenAIImageGenerationEnabled must change request hash: off=%q on=%q", off, on)
	}
}

func TestModelAdapterTestStreamRequestProjectsOpenAIImageGenerationEnabled(t *testing.T) {
	originalStream := streamModelAdapterTestOpenAI
	t.Cleanup(func() { streamModelAdapterTestOpenAI = originalStream })
	var captured modeladapter.StreamRequest
	streamModelAdapterTestOpenAI = func(_ context.Context, req modeladapter.StreamRequest, sink func(modeladapter.ModelEvent) error) error {
		captured = req
		if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "done"}); err != nil {
			return err
		}
		return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTurnFinished, OutputTokens: 1})
	}

	adapter := serverconfig.ModelAdapterConfig{
		DisplayName:                  "gpt",
		Type:                         "openai",
		BaseURL:                      "https://api.example.com/v1",
		APIKey:                       "test-key",
		TooltipData:                  "gpt",
		ModelID:                      "gpt-test",
		OpenAIEndpoint:               "/v1/responses",
		OpenAIImageGenerationEnabled: true,
	}
	service := &ProxyService{modelTestResults: map[string]ModelAdapterTestResult{}}
	if _, err := service.TestModelAdapter(adapter); err != nil {
		t.Fatalf("TestModelAdapter 返回错误：%v", err)
	}
	if !captured.OpenAIImageGenerationEnabled {
		t.Fatal("StreamRequest lost OpenAIImageGenerationEnabled")
	}
}

func TestModelAdapterTestRequestHashUsesEffectiveSavedProxy(t *testing.T) {
	netproxy.SetGlobal(netproxy.Config{})
	t.Cleanup(func() { netproxy.SetGlobal(netproxy.Config{}) })

	base := serverconfig.ModelAdapterConfig{
		DisplayName:    "gpt",
		Type:           "openai",
		BaseURL:        "https://api.example.com/v1",
		APIKey:         "test-key",
		TooltipData:    "gpt",
		ModelID:        "gpt-test",
		OpenAIEndpoint: "/v1/responses",
	}
	off := buildModelAdapterTestRequestHash(base)
	netproxy.SetGlobal(netproxy.Config{Enabled: false, URL: "http://global.example:8080"})
	stillOff := buildModelAdapterTestRequestHash(base)
	if off != stillOff {
		t.Fatalf("disabled global with retained URL must not change hash: %q vs %q", off, stillOff)
	}

	netproxy.SetGlobal(netproxy.Config{Enabled: true, URL: "http://user:s3cret@global.example:8080"})
	inherited := buildModelAdapterTestRequestHash(base)
	if inherited == off {
		t.Fatal("saved global custom must change request hash")
	}

	model := base
	model.OutboundProxy.Enabled = true
	model.OutboundProxy.URL = "socks5://model.example:1080"
	modelHash := buildModelAdapterTestRequestHash(model)
	if modelHash == inherited || modelHash == off {
		t.Fatal("model custom must change hash independently of global")
	}

	disabledModel := model
	disabledModel.OutboundProxy.Enabled = false
	if buildModelAdapterTestRequestHash(disabledModel) != inherited {
		t.Fatal("disabled model must inherit saved global for hash")
	}
}

func TestModelAdapterTestDoesNotReuseRunningResultWhenProxyChanges(t *testing.T) {
	netproxy.SetGlobal(netproxy.Config{})
	t.Cleanup(func() { netproxy.SetGlobal(netproxy.Config{}) })

	adapter := serverconfig.ModelAdapterConfig{
		DisplayName:    "gpt",
		Type:           "openai",
		BaseURL:        "https://api.example.com/v1",
		APIKey:         "test-key",
		TooltipData:    "gpt",
		ModelID:        "gpt-test",
		OpenAIEndpoint: "/v1/responses",
	}
	hash1 := buildModelAdapterTestRequestHash(adapter)
	id := buildModelAdapterTestCacheKey(adapter, hash1)
	service := &ProxyService{modelTestResults: map[string]ModelAdapterTestResult{
		id: {AdapterID: id, RequestHash: hash1, Status: string(ModelAdapterTestStatusRunning)},
	}}
	if _, ok := service.getRunningModelAdapterTestResult(id, hash1); !ok {
		t.Fatal("running result should match original hash")
	}
	netproxy.SetGlobal(netproxy.Config{Enabled: true, URL: "http://global.example:9"})
	hash2 := buildModelAdapterTestRequestHash(adapter)
	if hash1 == hash2 {
		t.Fatal("effective proxy change must change hash")
	}
	if _, ok := service.getRunningModelAdapterTestResult(id, hash2); ok {
		t.Fatal("running test must not be reused across effective proxy change")
	}
}

func TestModelAdapterTestStreamRequestProjectsOutboundProxy(t *testing.T) {
	originalStream := streamModelAdapterTestOpenAI
	t.Cleanup(func() { streamModelAdapterTestOpenAI = originalStream })
	var captured modeladapter.StreamRequest
	streamModelAdapterTestOpenAI = func(_ context.Context, req modeladapter.StreamRequest, sink func(modeladapter.ModelEvent) error) error {
		captured = req
		if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "done"}); err != nil {
			return err
		}
		return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTurnFinished, OutputTokens: 1})
	}

	adapter := serverconfig.ModelAdapterConfig{
		DisplayName:    "gpt",
		Type:           "openai",
		BaseURL:        "https://api.example.com/v1",
		APIKey:         "test-key",
		TooltipData:    "gpt",
		ModelID:        "gpt-test",
		OpenAIEndpoint: "/v1/responses",
		OutboundProxy:  netproxy.Config{Enabled: true, URL: "http://model.example:8080"},
	}
	service := &ProxyService{modelTestResults: map[string]ModelAdapterTestResult{}}
	if _, err := service.TestModelAdapter(adapter); err != nil {
		t.Fatalf("TestModelAdapter 返回错误：%v", err)
	}
	if !captured.OutboundProxy.Enabled || captured.OutboundProxy.URL != "http://model.example:8080" {
		t.Fatalf("StreamRequest outboundProxy = %+v", captured.OutboundProxy)
	}
}

func TestModelAdapterTestUsesObservableFakeProxy(t *testing.T) {
	netproxy.SetGlobal(netproxy.Config{})
	t.Cleanup(func() { netproxy.SetGlobal(netproxy.Config{}) })

	var hits int
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(proxy.Close)

	originalStream := streamModelAdapterTestOpenAI
	t.Cleanup(func() { streamModelAdapterTestOpenAI = originalStream })
	streamModelAdapterTestOpenAI = func(ctx context.Context, req modeladapter.StreamRequest, sink func(modeladapter.ModelEvent) error) error {
		return modeladapter.NewOpenAIAdapter().Stream(ctx, req, sink)
	}

	adapter := serverconfig.ModelAdapterConfig{
		DisplayName:    "gpt",
		Type:           "openai",
		BaseURL:        "http://provider.example/v1",
		APIKey:         "test-key",
		TooltipData:    "gpt",
		ModelID:        "gpt-test",
		OpenAIEndpoint: "/v1/chat/completions",
		OutboundProxy:  netproxy.Config{Enabled: true, URL: proxy.URL},
	}
	service := &ProxyService{modelTestResults: map[string]ModelAdapterTestResult{}}
	result, err := service.TestModelAdapter(adapter)
	if err != nil {
		t.Fatalf("TestModelAdapter 返回错误：%v", err)
	}
	if result.Status != string(ModelAdapterTestStatusSuccess) {
		t.Fatalf("status = %q error=%q", result.Status, result.Error)
	}
	if hits == 0 {
		t.Fatal("model test did not go through observable proxy")
	}
	if result.RequestHash != buildModelAdapterTestRequestHash(adapter) {
		t.Fatalf("result hash does not represent used route: %q", result.RequestHash)
	}
}

func TestModelAdapterCodexRefreshAndInferenceSucceedsThroughHTTPSConnectProxy(t *testing.T) {
	netproxy.SetGlobal(netproxy.Config{})
	t.Cleanup(func() { netproxy.SetGlobal(netproxy.Config{}) })

	expiredToken := fakeCodexJWT(t, time.Now().Add(-time.Minute), "acct-proxy")
	rotatedToken := fakeCodexJWT(t, time.Now().Add(2*time.Hour), "acct-proxy")
	upstream := &codexFakeUpstream{
		refreshToken: "refresh-proxy",
		rotatedToken: rotatedToken,
		streamText:   "1 2 3",
	}
	modelProxy := startTLSConnectProxy(t, upstream)
	envProxy := startRecordingOutboundProxy(t)
	isolateOutboundProxyEnv(t, envProxy.URL)

	authClient := newProxyTrustingHTTPClient(t, modelProxy, envProxy, false, 5*time.Second)
	providerClient := newProxyTrustingHTTPClient(t, modelProxy, envProxy, true, 5*time.Second)

	originalStream := streamModelAdapterTestOpenAI
	t.Cleanup(func() { streamModelAdapterTestOpenAI = originalStream })
	streamModelAdapterTestOpenAI = func(ctx context.Context, req modeladapter.StreamRequest, sink func(modeladapter.ModelEvent) error) error {
		return modeladapter.NewOpenAIAdapterWithClient(providerClient).Stream(ctx, req, sink)
	}

	service := newCodexProxyServiceWithAccessToken(t, authClient, expiredToken)
	result, err := service.TestModelAdapter(serverconfig.ModelAdapterConfig{
		DisplayName:      "Managed Codex",
		Type:             "openai",
		BaseURL:          "https://ignored.example/v1",
		CredentialSource: "codex",
		TooltipData:      "备注",
		ModelID:          "gpt-5.1",
		OpenAIEndpoint:   "/v1/responses",
		OutboundProxy:    netproxy.Config{Enabled: true, URL: modelProxy.URL},
	})
	if err != nil {
		t.Fatalf("TestModelAdapter 返回错误：%v status=%q raw=%q hosts=%v", err, result.Status, result.RawResponse, modelProxy.snapshotHosts())
	}
	if result.Status != string(ModelAdapterTestStatusSuccess) {
		t.Fatalf("status = %q error=%q raw=%q hosts=%v", result.Status, result.Error, result.RawResponse, modelProxy.snapshotHosts())
	}
	if !strings.Contains(result.RawResponse, "1 2 3") {
		t.Fatalf("raw response missing streamed text: %q", result.RawResponse)
	}

	gotAuth, gotPaths := upstream.snapshot()
	if gotAuth != "Bearer "+rotatedToken {
		t.Fatalf("inference Authorization = %q, want rotated token (not expired=%t)", gotAuth, gotAuth == "Bearer "+expiredToken)
	}
	if gotAuth == "Bearer "+expiredToken {
		t.Fatal("inference used expired token; refresh did not apply")
	}
	assertContainsPath(t, gotPaths, "/oauth/token")
	assertContainsPath(t, gotPaths, "/backend-api/codex/responses")

	hosts := modelProxy.snapshotHosts()
	assertContainsHost(t, hosts, "auth.openai.com:443")
	assertContainsHost(t, hosts, "chatgpt.com:443")
	for _, host := range hosts {
		if host != "auth.openai.com:443" && host != "chatgpt.com:443" {
			t.Fatalf("unexpected extra outbound CONNECT host %q in %v", host, hosts)
		}
	}
	if envHits, envHosts := envProxy.snapshot(); envHits != 0 {
		t.Fatalf("env/system proxy hits = %d hosts=%v modelHosts=%v", envHits, envHosts, hosts)
	}
}

type recordingOutboundProxy struct {
	URL   string
	hits  atomic.Int32
	mu    sync.Mutex
	hosts []string
}

func (proxy *recordingOutboundProxy) snapshot() (int, []string) {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	copied := append([]string{}, proxy.hosts...)
	return int(proxy.hits.Load()), copied
}

func startRecordingOutboundProxy(t *testing.T) *recordingOutboundProxy {
	t.Helper()
	proxy := &recordingOutboundProxy{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		host := strings.TrimSpace(request.Host)
		if request.URL != nil && strings.TrimSpace(request.URL.Host) != "" {
			host = request.URL.Host
		}
		proxy.hits.Add(1)
		proxy.mu.Lock()
		proxy.hosts = append(proxy.hosts, request.Method+" "+host)
		proxy.mu.Unlock()
		writer.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)
	proxy.URL = server.URL
	return proxy
}

func isolateOutboundProxyEnv(t *testing.T, envProxyURL string) {
	t.Helper()
	t.Setenv("HTTP_PROXY", envProxyURL)
	t.Setenv("http_proxy", envProxyURL)
	t.Setenv("HTTPS_PROXY", envProxyURL)
	t.Setenv("https_proxy", envProxyURL)
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	t.Setenv("REQUEST_METHOD", "")
	netproxy.SetGlobal(netproxy.Config{})
}

func fakeCodexJWT(t *testing.T, exp time.Time, accountID string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"email": "codex-proxy@example.test",
		"sub":   "codex-proxy-user",
		"exp":   exp.Unix(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func newCodexProxyServiceWithAuth(t *testing.T, client subscriptionauth.HTTPDoer, exp time.Time) *ProxyService {
	t.Helper()
	return newCodexProxyServiceWithAccessToken(t, client, fakeCodexJWT(t, exp, "acct-proxy"))
}

func newCodexProxyServiceWithAccessToken(t *testing.T, client subscriptionauth.HTTPDoer, accessToken string) *ProxyService {
	t.Helper()
	auth := subscriptionauth.NewService(t.TempDir(), client)
	body, err := json.Marshal(map[string]any{
		"auth_mode":          "chatgpt",
		"chatgpt_account_id": "acct-proxy",
		"tokens": map[string]string{
			"access_token":  accessToken,
			"refresh_token": "refresh-proxy",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.ImportCodexAuth(context.Background(), body); err != nil {
		t.Fatalf("ImportCodexAuth: %v", err)
	}
	return &ProxyService{
		subscriptionAuth: auth,
		modelTestResults: map[string]ModelAdapterTestResult{},
	}
}

func newCodexProxyServiceWithExpiredAuth(t *testing.T) *ProxyService {
	t.Helper()
	return newCodexProxyServiceWithAuth(t, netproxy.NewHTTPClient(3*time.Second), time.Now().Add(-time.Minute))
}

type tlsConnectProxy struct {
	URL          string
	upstreamAddr string
	roots        *x509.CertPool
	mu           sync.Mutex
	hosts        []string
}

func (proxy *tlsConnectProxy) snapshotHosts() []string {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	return append([]string{}, proxy.hosts...)
}

func (proxy *tlsConnectProxy) addHost(host string) {
	proxy.mu.Lock()
	proxy.hosts = append(proxy.hosts, host)
	proxy.mu.Unlock()
}

type codexFakeUpstream struct {
	refreshToken string
	rotatedToken string
	streamText   string
	mu           sync.Mutex
	auth         string
	paths        []string
}

func (upstream *codexFakeUpstream) snapshot() (string, []string) {
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	return upstream.auth, append([]string{}, upstream.paths...)
}

func (upstream *codexFakeUpstream) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	path := request.URL.Path
	upstream.mu.Lock()
	upstream.paths = append(upstream.paths, path)
	if strings.Contains(path, "/backend-api/codex/responses") {
		upstream.auth = request.Header.Get("Authorization")
	}
	upstream.mu.Unlock()

	switch {
	case strings.Contains(path, "/oauth/token"):
		if err := request.ParseForm(); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		if request.PostForm.Get("grant_type") != "refresh_token" || request.PostForm.Get("refresh_token") != upstream.refreshToken {
			http.Error(writer, "invalid refresh", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]string{
			"access_token":  upstream.rotatedToken,
			"refresh_token": "rotated-refresh",
			"id_token":      upstream.rotatedToken,
		})
	case strings.Contains(path, "/backend-api/codex/responses"):
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", upstream.streamText)
		_, _ = fmt.Fprintf(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"output_text\":%q}}\n\n", upstream.streamText)
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
	default:
		http.NotFound(writer, request)
	}
}

func startTLSConnectProxy(t *testing.T, handler http.Handler) *tlsConnectProxy {
	t.Helper()
	cert, roots := testHTTPSProxyCertificate(t, "auth.openai.com", "chatgpt.com")
	upstream := httptest.NewUnstartedServer(handler)
	upstream.EnableHTTP2 = true
	upstream.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	upstream.StartTLS()
	t.Cleanup(upstream.Close)

	proxy := &tlsConnectProxy{roots: roots, upstreamAddr: upstream.Listener.Addr().String()}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	proxy.URL = "http://" + listener.Addr().String()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go proxy.handleCONNECT(conn)
		}
	}()
	return proxy
}

func (proxy *tlsConnectProxy) handleCONNECT(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(conn)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	_ = req.Body.Close()
	if req.Method != http.MethodConnect {
		proxy.addHost(req.Method + " " + req.Host)
		_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	proxy.addHost(req.Host)
	up, err := net.DialTimeout("tcp", proxy.upstreamAddr, 3*time.Second)
	if err != nil {
		_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer up.Close()
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	_ = up.SetDeadline(time.Time{})
	tunnelProxyConns(&bufferedConn{Conn: conn, reader: reader}, up)
}

func tunnelProxyConns(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, a)
		_ = b.Close()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		_ = a.Close()
	}()
	wg.Wait()
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (conn *bufferedConn) Read(p []byte) (int, error) {
	return conn.reader.Read(p)
}

func testHTTPSProxyCertificate(t *testing.T, dnsNames ...string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		DNSNames:     dnsNames,
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

func newProxyTrustingHTTPClient(t *testing.T, modelProxy *tlsConnectProxy, envProxy *recordingOutboundProxy, provider bool, timeout time.Duration) *http.Client {
	t.Helper()
	var client *http.Client
	if provider {
		client = netproxy.NewProviderHTTPClient(timeout)
	} else {
		client = netproxy.NewHTTPClient(timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("http client transport is not *http.Transport")
	}
	tlsConfig := &tls.Config{}
	if transport.TLSClientConfig != nil {
		tlsConfig = transport.TLSClientConfig.Clone()
	}
	tlsConfig.RootCAs = modelProxy.roots
	transport.TLSClientConfig = tlsConfig
	allowed := map[string]struct{}{}
	if parsed, err := url.Parse(modelProxy.URL); err == nil {
		allowed[parsed.Host] = struct{}{}
	}
	if parsed, err := url.Parse(envProxy.URL); err == nil {
		allowed[parsed.Host] = struct{}{}
	}
	baseDial := transport.DialContext
	if baseDial == nil {
		baseDial = (&net.Dialer{Timeout: 3 * time.Second}).DialContext
	}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if _, ok := allowed[addr]; !ok {
			return nil, fmt.Errorf("direct dial to %s forbidden", addr)
		}
		return baseDial(ctx, network, addr)
	}
	return client
}

func assertContainsHost(t *testing.T, hosts []string, want string) {
	t.Helper()
	for _, host := range hosts {
		if host == want {
			return
		}
	}
	t.Fatalf("CONNECT hosts %v missing %q", hosts, want)
}

func assertContainsPath(t *testing.T, paths []string, want string) {
	t.Helper()
	for _, path := range paths {
		if path == want {
			return
		}
	}
	t.Fatalf("upstream paths %v missing %q", paths, want)
}

func assertUsedModelProxyNotEnv(t *testing.T, label string, modelProxy, envProxy *recordingOutboundProxy) {
	t.Helper()
	modelHits, modelHosts := modelProxy.snapshot()
	envHits, envHosts := envProxy.snapshot()
	if modelHits == 0 {
		t.Fatalf("%s did not use model outbound proxy; envHits=%d envHosts=%v", label, envHits, envHosts)
	}
	if envHits != 0 {
		t.Fatalf("%s fell back to env/system proxy: modelHosts=%v envHosts=%v", label, modelHosts, envHosts)
	}
}
