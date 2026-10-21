package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	modeladapter "cursor/internal/backend/agent/model"
	"cursor/internal/backend/forwarder"
	serverconfig "cursor/internal/backend/server/config"
)

func TestGatewayAndDirectProviderShareCapacityLimit(t *testing.T) {
	var active atomic.Int32
	var peak atomic.Int32
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := peak.Load()
			if current <= observed || peak.CompareAndSwap(observed, current) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(upstream.Close)

	adapter := serverconfig.ModelAdapterConfig{
		DisplayName:           "shared-capacity",
		Type:                  "openai",
		BaseURL:               upstream.URL,
		APIKey:                "provider-secret",
		TooltipData:           "shared-capacity",
		ModelID:               "provider-model",
		OpenAIEndpoint:        "/v1/chat/completions",
		MaxConcurrentRequests: 1,
	}
	adapters, err := serverconfig.NormalizeModelAdapterConfigs([]serverconfig.ModelAdapterConfig{adapter})
	if err != nil {
		t.Fatalf("normalize adapter: %v", err)
	}
	root := t.TempDir()
	manager, err := serverconfig.NewManager(context.Background(), serverconfig.NewStore(filepath.Join(root, "config.yaml"), filepath.Join(root, "logs")))
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	cfg := serverconfig.DefaultConfig()
	cfg.ModelAdapters = adapters
	cfg.Gateway.Enabled = true
	cfg.Gateway.Token = "secret-token"
	cfg.Gateway.PublicModels = []serverconfig.GatewayPublicModel{{ID: "public-a", TargetAdapterID: adapters[0].ID}}
	if _, err := manager.Save(context.Background(), cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	gatewayServer := New(forwarder.NewProviderGateway(manager), manager)
	gatewayDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		gatewayDone <- doGatewayRequest(t, gatewayServer.Handler(), http.MethodPost, chatCompletionsPath, "secret-token", `{"model":"public-a","messages":[{"role":"user","content":"one"}]}`)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Gateway request did not reach upstream")
	}

	directDone := make(chan error, 1)
	direct := forwarder.NewProviderGateway(manager)
	go func() {
		directDone <- direct.StartStream(context.Background(), forwarder.ProviderRequest{
			RequestID: "cursor-like", RunID: "cursor-like", ModelCallID: "cursor-like",
			ConversationID: "cursor-like", ModelID: adapters[0].ID,
			Messages: []modeladapter.Message{{Role: "user", Content: "two"}},
		}, func(modeladapter.ModelEvent) error { return nil })
	}()
	select {
	case <-entered:
		t.Fatal("direct Provider request bypassed the shared capacity slot")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("direct Provider request did not enter after capacity release")
	}
	if response := <-gatewayDone; response.Code != http.StatusOK {
		t.Fatalf("Gateway response = %d %s", response.Code, response.Body.String())
	}
	if err := <-directDone; err != nil {
		t.Fatalf("direct Provider error = %v", err)
	}
	if peak.Load() != 1 {
		t.Fatalf("shared upstream peak = %d, want 1", peak.Load())
	}
}

func TestGatewayFallbackBeforeOutputDoesNotWriteHash(t *testing.T) {
	var failHits, okHits atomic.Int32
	failServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		failHits.Add(1)
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"error":{"message":"down"}}`))
	}))
	t.Cleanup(failServer.Close)
	okServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		okHits.Add(1)
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"recovered\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(okServer.Close)

	physical := func(name, baseURL string, sort int) serverconfig.ModelAdapterConfig {
		return serverconfig.ModelAdapterConfig{
			Sort:           sort,
			DisplayName:    name,
			Type:           "openai",
			BaseURL:        baseURL,
			APIKey:         "provider-secret",
			TooltipData:    name,
			ModelID:        name,
			OpenAIEndpoint: "/v1/chat/completions",
		}
	}
	primary := physical("primary", failServer.URL, 1)
	backup := physical("backup", okServer.URL, 2)
	logical := physical("logical", "https://logical.example/v1", 3)
	normalizedPhysical, err := serverconfig.NormalizeModelAdapterConfigs([]serverconfig.ModelAdapterConfig{primary, backup, logical})
	if err != nil {
		t.Fatalf("normalize physical: %v", err)
	}
	logical = normalizedPhysical[2]
	logical.ProviderFallback = serverconfig.ProviderFallbackConfig{
		Enabled:             true,
		PrimaryChannelID:    normalizedPhysical[0].ID,
		CandidateChannelIDs: []string{normalizedPhysical[1].ID},
		MaxHttpAttempts:     2,
		MaxWaitSeconds:      1,
	}
	adapters, err := serverconfig.NormalizeModelAdapterConfigs([]serverconfig.ModelAdapterConfig{normalizedPhysical[0], normalizedPhysical[1], logical})
	if err != nil {
		t.Fatalf("normalize fallback adapters: %v", err)
	}

	root := t.TempDir()
	store := serverconfig.NewStore(filepath.Join(root, "config.yaml"), filepath.Join(root, "logs"))
	manager, err := serverconfig.NewManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	cfg := serverconfig.DefaultConfig()
	cfg.LastAgentModelHash = "keep-me"
	cfg.ModelAdapters = adapters
	cfg.Gateway.Enabled = true
	cfg.Gateway.Token = "secret-token"
	cfg.Gateway.PublicModels = []serverconfig.GatewayPublicModel{{
		ID:              "public-a",
		TargetAdapterID: adapters[2].ID,
	}}
	saved, err := manager.Save(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	server := New(forwarder.NewProviderGateway(manager), manager)
	response := doGatewayRequest(t, server.Handler(), http.MethodPost, "/v1/chat/completions", "secret-token", `{"model":"public-a","messages":[{"role":"user","content":"hi"}]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	encoded, _ := json.Marshal(payload["choices"])
	if !strings.Contains(string(encoded), "recovered") {
		t.Fatalf("choices = %s", encoded)
	}
	if failHits.Load() == 0 || okHits.Load() == 0 {
		t.Fatalf("hits fail=%d ok=%d", failHits.Load(), okHits.Load())
	}
	if manager.Current().LastAgentModelHash != "keep-me" || saved.LastAgentModelHash != "keep-me" {
		t.Fatalf("hash changed to %q", manager.Current().LastAgentModelHash)
	}
}
