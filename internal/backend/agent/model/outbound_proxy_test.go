package modeladapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cursor/internal/netproxy"
	legacyruntime "cursor/internal/runtime"
	"cursor/internal/subscriptionauth"
)

func TestApplyChannelToRequestIsolatesOutboundProxy(t *testing.T) {
	req := StreamRequest{MaxTokens: 1, RequestKnobs: map[string]any{"keep": true}}
	model := applyChannelToRequest(req, &legacyruntime.ResolvedChannel{
		Provider:       "openai",
		OpenAIEndpoint: "/v1/chat/completions",
		OutboundProxy:  netproxy.Config{Enabled: true, URL: "http://model.example:1"},
	})
	globalInherit := applyChannelToRequest(req, &legacyruntime.ResolvedChannel{
		Provider:       "openai",
		OpenAIEndpoint: "/v1/chat/completions",
		OutboundProxy:  netproxy.Config{Enabled: false, URL: "http://unused.example"},
	})
	if !model.OutboundProxy.Enabled || model.OutboundProxy.URL != "http://model.example:1" {
		t.Fatalf("model proxy = %+v", model.OutboundProxy)
	}
	if globalInherit.OutboundProxy.Enabled {
		t.Fatal("disabled channel inherited enabled proxy")
	}
}

func TestAttachRequestLivenessKeepsOutboundProxyWhenReused(t *testing.T) {
	req := StreamRequest{OutboundProxy: netproxy.Config{Enabled: true, URL: "http://127.0.0.1:18007"}}
	first, live, owned := attachRequestLiveness(context.Background(), &req)
	if !owned || live == nil {
		t.Fatal("expected owned liveness")
	}
	cfg, ok := netproxy.RequestConfigFromContext(first)
	if !ok || !cfg.Enabled || cfg.URL != "http://127.0.0.1:18007" {
		t.Fatalf("first ctx proxy = %+v ok=%v", cfg, ok)
	}

	second, live2, owned2 := attachRequestLiveness(context.Background(), &req)
	if owned2 || live2 != live {
		t.Fatal("expected reused liveness")
	}
	cfg, ok = netproxy.RequestConfigFromContext(second)
	if !ok || !cfg.Enabled || cfg.URL != "http://127.0.0.1:18007" {
		t.Fatalf("reused liveness dropped outbound proxy: %+v ok=%v", cfg, ok)
	}
}

func TestOpenAIAndAnthropicStreamUseRequestCustomProxy(t *testing.T) {
	netproxy.SetGlobal(netproxy.Config{})
	t.Cleanup(func() { netproxy.SetGlobal(netproxy.Config{}) })

	openaiSSE := "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	anthropicSSE := strings.Join([]string{
		"event: content_block_delta",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}",
		"",
		"event: message_stop",
		"data: {\"type\":\"message_stop\"}",
		"",
		"",
	}, "\n")

	t.Run("openai", func(t *testing.T) {
		var hits atomic.Int32
		proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			hits.Add(1)
			if !strings.Contains(request.URL.String(), "provider.example") {
				t.Errorf("proxied URL = %s", request.URL)
			}
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, openaiSSE)
		}))
		t.Cleanup(proxy.Close)

		adapter := &OpenAIAdapter{client: netproxy.NewHTTPClient(5 * time.Second)}
		err := adapter.Stream(context.Background(), StreamRequest{
			RequestID:       "req-openai-proxy",
			ModelCallID:     "call-openai-proxy",
			BaseURL:         "http://provider.example/v1",
			APIKey:          "test-key",
			ProviderModelID: "m1",
			OpenAIEndpoint:  "/v1/chat/completions",
			Messages:        []Message{{Role: "user", Content: "hi"}},
			MaxTokens:       16,
			Stream:          true,
			OutboundProxy:   netproxy.Config{Enabled: true, URL: proxy.URL},
			RecoverySettings: RecoverySettings{
				ConnectTimeout:    3 * time.Second,
				FirstEventTimeout: 3 * time.Second,
				StreamIdleTimeout: 3 * time.Second,
				CallTimeout:       5 * time.Second,
			},
		}, func(ModelEvent) error { return nil })
		if err != nil {
			t.Fatalf("openai stream: %v", err)
		}
		if hits.Load() == 0 {
			t.Fatal("openai stream did not use request custom proxy")
		}
	})

	t.Run("anthropic", func(t *testing.T) {
		var hits atomic.Int32
		proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			hits.Add(1)
			if !strings.Contains(request.URL.String(), "provider.example") {
				t.Errorf("proxied URL = %s", request.URL)
			}
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, anthropicSSE)
		}))
		t.Cleanup(proxy.Close)

		adapter := &AnthropicAdapter{client: netproxy.NewHTTPClient(5 * time.Second)}
		err := adapter.Stream(context.Background(), StreamRequest{
			RequestID:       "req-anthropic-proxy",
			ModelCallID:     "call-anthropic-proxy",
			BaseURL:         "http://provider.example",
			APIKey:          "test-key",
			ProviderModelID: "m1",
			Messages:        []Message{{Role: "user", Content: "hi"}},
			MaxTokens:       16,
			Stream:          true,
			OutboundProxy:   netproxy.Config{Enabled: true, URL: proxy.URL},
			RecoverySettings: RecoverySettings{
				ConnectTimeout:    3 * time.Second,
				FirstEventTimeout: 3 * time.Second,
				StreamIdleTimeout: 3 * time.Second,
				CallTimeout:       5 * time.Second,
			},
		}, func(ModelEvent) error { return nil })
		if err != nil {
			t.Fatalf("anthropic stream: %v", err)
		}
		if hits.Load() == 0 {
			t.Fatal("anthropic stream did not use request custom proxy")
		}
	})
}

func TestFallbackCandidatesUseOwnOutboundProxy(t *testing.T) {
	netproxy.SetGlobal(netproxy.Config{})
	t.Cleanup(func() { netproxy.SetGlobal(netproxy.Config{}) })

	var (
		mu     sync.Mutex
		hosts  = map[string]int{}
		openai = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	)
	primaryProxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		hosts["primary"]++
		mu.Unlock()
		if !strings.Contains(request.URL.Host, "primary.example") {
			t.Errorf("primary proxy saw %s", request.URL)
		}
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(writer, `{"error":"primary"}`)
	}))
	candidateProxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		hosts["candidate"]++
		mu.Unlock()
		if !strings.Contains(request.URL.Host, "candidate.example") {
			t.Errorf("candidate proxy saw %s", request.URL)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, openai)
	}))
	t.Cleanup(primaryProxy.Close)
	t.Cleanup(candidateProxy.Close)

	plan := &legacyruntime.ChannelPlan{
		FallbackEnabled:       true,
		MaxHttpAttempts:       4,
		MaxAttemptsPerChannel: 2,
		Channels: []legacyruntime.ResolvedChannel{
			{
				ID:             "ch-a",
				Provider:       "openai",
				BaseURL:        "http://primary.example/v1",
				APIKey:         "k1",
				Model:          "m1",
				OpenAIEndpoint: "/v1/chat/completions",
				OutboundProxy:  netproxy.Config{Enabled: true, URL: primaryProxy.URL},
			},
			{
				ID:             "ch-b",
				Provider:       "openai",
				BaseURL:        "http://candidate.example/v1",
				APIKey:         "k2",
				Model:          "m1",
				OpenAIEndpoint: "/v1/chat/completions",
				OutboundProxy:  netproxy.Config{Enabled: true, URL: candidateProxy.URL},
			},
		},
	}
	inner := NewRouter(&stubPlanResolver{plan: plan})
	inner.openai = &OpenAIAdapter{client: netproxy.NewHTTPClient(5 * time.Second), retry: fallbackTestRetry()}
	inner.anthropic = &AnthropicAdapter{client: netproxy.NewHTTPClient(5 * time.Second), retry: fallbackTestRetry()}
	router := NewFallbackAwareRouter(inner, &stubPlanResolver{plan: plan})

	err := router.Stream(context.Background(), fallbackHTTPRequest(), func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("fallback stream: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hosts["primary"] == 0 || hosts["candidate"] == 0 {
		t.Fatalf("proxy hits = %#v", hosts)
	}
}

func TestOpenAIStreamCustomProxyFailureDoesNotUseEnv(t *testing.T) {
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
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	t.Setenv("REQUEST_METHOD", "")
	netproxy.SetGlobal(netproxy.Config{})
	t.Cleanup(func() { netproxy.SetGlobal(netproxy.Config{}) })

	adapter := &OpenAIAdapter{client: netproxy.NewHTTPClient(2 * time.Second)}
	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:       "req-dead-proxy",
		ModelCallID:     "call-dead-proxy",
		BaseURL:         "http://provider.example/v1",
		APIKey:          "test-key",
		ProviderModelID: "m1",
		OpenAIEndpoint:  "/v1/chat/completions",
		Messages:        []Message{{Role: "user", Content: "hi"}},
		MaxTokens:       8,
		Stream:          true,
		OutboundProxy:   netproxy.Config{Enabled: true, URL: deadURL},
		RecoverySettings: RecoverySettings{
			MaxTotalAttempts:      1,
			MaxAttemptsPerChannel: 1,
			ConnectTimeout:        time.Second,
			FirstEventTimeout:     time.Second,
			StreamIdleTimeout:     time.Second,
			CallTimeout:           2 * time.Second,
		},
	}, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("dead custom proxy must fail")
	}
	if envHits.Load() != 0 {
		t.Fatalf("env proxy hits = %d", envHits.Load())
	}
	if strings.Contains(fmt.Sprint(err), "user:") {
		t.Fatalf("error leaked userinfo: %v", err)
	}
}

func TestOpenAICodexHTTPSStreamUsesRequestCustomProxyNotEnv(t *testing.T) {
	envProxy := startRecordingHTTPProxy(t)
	modelProxy := startRecordingHTTPProxy(t)
	isolateModelOutboundProxyEnv(t, envProxy.URL)

	adapter := &OpenAIAdapter{client: netproxy.NewHTTPClient(3 * time.Second)}
	_ = adapter.Stream(context.Background(), StreamRequest{
		RequestID:        "req-codex-proxy",
		ModelCallID:      "call-codex-proxy",
		BaseURL:          subscriptionauth.CodexResponsesURL,
		APIKey:           "codex-token",
		CredentialSource: "codex",
		ChatGPTAccountID: "acct-proxy",
		ProviderModelID:  "gpt-5.1",
		OpenAIEndpoint:   "/v1/responses",
		Messages:         []Message{{Role: "user", Content: "hi"}},
		MaxTokens:        8,
		Stream:           true,
		OutboundProxy:    netproxy.Config{Enabled: true, URL: modelProxy.URL},
		RecoverySettings: RecoverySettings{
			MaxTotalAttempts:      1,
			MaxAttemptsPerChannel: 1,
			ConnectTimeout:        time.Second,
			FirstEventTimeout:     time.Second,
			StreamIdleTimeout:     time.Second,
			CallTimeout:           2 * time.Second,
		},
	}, func(ModelEvent) error { return nil })
	assertRecordingProxyUsedNotEnv(t, "openai codex HTTPS stream", modelProxy, envProxy)
}

func TestCodexRouterRefreshUsesRequestCustomProxyNotEnv(t *testing.T) {
	envProxy := startRecordingHTTPProxy(t)
	modelProxy := startRecordingHTTPProxy(t)
	isolateModelOutboundProxyEnv(t, envProxy.URL)

	creds := newExpiredCodexAuthService(t, netproxy.NewHTTPClient(3*time.Second))
	channel := legacyruntime.ResolvedChannel{
		ID:               "channel-codex",
		Name:             "managed-codex",
		Provider:         "openai",
		BaseURL:          subscriptionauth.CodexResponsesURL,
		CredentialSource: "codex",
		Model:            "gpt-5.1",
		OpenAIEndpoint:   "/v1/responses",
		OutboundProxy:    netproxy.Config{Enabled: true, URL: modelProxy.URL},
	}
	router := &Router{
		openai:      &OpenAIAdapter{client: netproxy.NewHTTPClient(3 * time.Second), retry: fallbackTestRetry()},
		resolver:    staticChannelResolver{channel: &channel},
		credentials: creds,
	}
	_ = router.Stream(context.Background(), StreamRequest{
		RequestID:   "req-codex-refresh-proxy",
		ModelCallID: "call-codex-refresh-proxy",
		ModelID:     "channel-codex",
		Messages:    []Message{{Role: "user", Content: "hi"}},
		MaxTokens:   8,
		Stream:      true,
		RecoverySettings: RecoverySettings{
			MaxTotalAttempts:      1,
			MaxAttemptsPerChannel: 1,
			ConnectTimeout:        time.Second,
			FirstEventTimeout:     time.Second,
			StreamIdleTimeout:     time.Second,
			CallTimeout:           2 * time.Second,
		},
	}, func(ModelEvent) error { return nil })
	assertRecordingProxyUsedNotEnv(t, "router Codex credential refresh", modelProxy, envProxy)
}

type recordingHTTPProxy struct {
	URL   string
	hits  atomic.Int32
	mu    sync.Mutex
	hosts []string
}

func (proxy *recordingHTTPProxy) snapshot() (int, []string) {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	return int(proxy.hits.Load()), append([]string{}, proxy.hosts...)
}

func startRecordingHTTPProxy(t *testing.T) *recordingHTTPProxy {
	t.Helper()
	proxy := &recordingHTTPProxy{}
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

func isolateModelOutboundProxyEnv(t *testing.T, envProxyURL string) {
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
	t.Cleanup(func() { netproxy.SetGlobal(netproxy.Config{}) })
}

func assertRecordingProxyUsedNotEnv(t *testing.T, label string, modelProxy, envProxy *recordingHTTPProxy) {
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

func newExpiredCodexAuthService(t *testing.T, client subscriptionauth.HTTPDoer) *subscriptionauth.Service {
	t.Helper()
	service := subscriptionauth.NewService(t.TempDir(), client)
	payload, err := json.Marshal(map[string]any{
		"email": "codex-proxy@example.test",
		"sub":   "codex-proxy-user",
		"exp":   time.Now().Add(-time.Minute).Unix(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-proxy",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
	body, err := json.Marshal(map[string]any{
		"auth_mode":          "chatgpt",
		"chatgpt_account_id": "acct-proxy",
		"tokens": map[string]string{
			"access_token":  token,
			"refresh_token": "refresh-proxy",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImportCodexAuth(context.Background(), body); err != nil {
		t.Fatalf("ImportCodexAuth: %v", err)
	}
	return service
}
