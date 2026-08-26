package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runtimecore "cursor/internal/backend/agent/core"
	modeladapter "cursor/internal/backend/agent/model"
	"cursor/internal/backend/forwarder"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/observability"
)

type gatewayCaptureSink struct {
	mu     sync.Mutex
	events []observability.Event
}

func (sink *gatewayCaptureSink) Record(_ context.Context, capture observability.Capture) bool {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.events = append(sink.events, capture.Event)
	return true
}

func (sink *gatewayCaptureSink) snapshot() []observability.Event {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]observability.Event(nil), sink.events...)
}

type staticConfig struct {
	cfg serverconfig.Config
}

func (source staticConfig) Current() serverconfig.Config {
	return source.cfg
}

type fakeStreamer struct {
	mu      sync.Mutex
	start   func(ctx context.Context, req forwarder.ProviderRequest, sink func(modeladapter.ModelEvent) error) error
	lastReq forwarder.ProviderRequest
}

func (fake *fakeStreamer) StartStream(ctx context.Context, req forwarder.ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
	fake.mu.Lock()
	fake.lastReq = req
	fake.mu.Unlock()
	if fake.start != nil {
		return fake.start(ctx, req, sink)
	}
	_ = sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "hello"})
	_ = sink(modeladapter.ModelEvent{
		Kind:         modeladapter.ModelEventKindTurnFinished,
		FinishReason: "stop",
		UsagePresent: true,
		InputTokens:  3,
		OutputTokens: 1,
	})
	return nil
}

func gatewayTestConfig(t *testing.T, token string) (serverconfig.Config, string) {
	t.Helper()
	adapter := serverconfig.ModelAdapterConfig{
		DisplayName:    "physical",
		Type:           "openai",
		BaseURL:        "https://api.example.com/v1",
		APIKey:         "provider-secret",
		TooltipData:    "physical",
		ModelID:        "provider-model",
		OpenAIEndpoint: "/v1/chat/completions",
	}
	adapters, err := serverconfig.NormalizeModelAdapterConfigs([]serverconfig.ModelAdapterConfig{adapter})
	if err != nil {
		t.Fatalf("NormalizeModelAdapterConfigs() error = %v", err)
	}
	cfg := serverconfig.DefaultConfig()
	cfg.Gateway.Enabled = true
	cfg.Gateway.Token = token
	cfg.Gateway.PublicModels = []serverconfig.GatewayPublicModel{{
		ID:              "public-a",
		TargetAdapterID: adapters[0].ID,
	}}
	cfg.ModelAdapters = adapters
	normalized, err := serverconfig.NormalizeConfig(cfg)
	if err != nil {
		t.Fatalf("NormalizeConfig() error = %v", err)
	}
	return normalized, adapters[0].ID
}

func doGatewayRequest(t *testing.T, handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:4321"
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestGatewayObservabilityStoresOnlyProtocolAndPublicModelMetadata(t *testing.T) {
	sink := &gatewayCaptureSink{}
	observability.SetProcessSink(sink)
	t.Cleanup(func() { observability.ClearProcessSink(sink) })
	cfg, _ := gatewayTestConfig(t, "secret-token")
	body := `{"model":"public-a","messages":[{"role":"user","content":"sensitive request text"}]}`
	response := doGatewayRequest(t, New(&fakeStreamer{}, staticConfig{cfg: cfg}).Handler(), http.MethodPost, chatCompletionsPath, "secret-token", body)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	events := sink.snapshot()
	if len(events) != 2 {
		t.Fatalf("events=%#v", events)
	}
	for index, event := range events {
		if event.Layer != "gateway" || event.Protocol != "http" || event.Fields["client_protocol"] != "openai_chat" {
			t.Fatalf("event=%#v", event)
		}
		if index == 1 && event.Fields["public_model_id"] != "public-a" {
			t.Fatalf("finished event missing public model: %#v", event)
		}
		raw, _ := json.Marshal(event)
		if strings.Contains(string(raw), "secret-token") || strings.Contains(string(raw), "sensitive request text") {
			t.Fatalf("gateway event leaked request content: %s", raw)
		}
	}
	if events[1].Event != "request_finished" || events[1].Status != "ok" || events[1].Fields["status_code"] != http.StatusOK {
		t.Fatalf("finished=%#v", events[1])
	}
}

func TestGatewayHTTPServerSmoke(t *testing.T) {
	token, err := serverconfig.GenerateGatewayToken()
	if err != nil {
		t.Fatalf("GenerateGatewayToken() error = %v", err)
	}
	cfg, _ := gatewayTestConfig(t, token)
	server := New(&fakeStreamer{}, staticConfig{cfg: cfg})
	gatewayCfg := cfg.Gateway
	gatewayCfg.ListenAddr = serverconfig.DefaultGatewayListenAddr
	if err := server.Start(gatewayCfg); err != nil {
		t.Fatalf("Start(%s) error = %v", gatewayCfg.ListenAddr, err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Stop(stopCtx)
	})

	client := &http.Client{Timeout: 5 * time.Second}
	request := func(method, path, body string) *http.Response {
		t.Helper()
		request, err := http.NewRequest(method, "http://"+server.ListenAddr()+path, strings.NewReader(body))
		if err != nil {
			t.Fatalf("NewRequest() error = %v", err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("HTTP %s %s error = %v", method, path, err)
		}
		return response
	}

	models := request(http.MethodGet, modelsPath, "")
	defer models.Body.Close()
	if models.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d", modelsPath, models.StatusCode)
	}
	modelsBody, err := io.ReadAll(models.Body)
	if err != nil || !strings.Contains(string(modelsBody), `"public-a"`) {
		t.Fatalf("models body = %s, read error = %v", modelsBody, err)
	}

	body := `{"model":"public-a","messages":[{"role":"user","content":"hello"}]}`
	completion := request(http.MethodPost, chatCompletionsPath, body)
	defer completion.Body.Close()
	if completion.StatusCode != http.StatusOK {
		completionBody, _ := io.ReadAll(completion.Body)
		t.Fatalf("POST %s status = %d body=%s", chatCompletionsPath, completion.StatusCode, completionBody)
	}
	completionBody, err := io.ReadAll(completion.Body)
	if err != nil || !strings.Contains(string(completionBody), `"object":"chat.completion"`) {
		t.Fatalf("completion body = %s, read error = %v", completionBody, err)
	}

	stream := request(http.MethodPost, chatCompletionsPath, `{"model":"public-a","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	defer stream.Body.Close()
	streamBody, err := io.ReadAll(stream.Body)
	if err != nil || stream.StatusCode != http.StatusOK || !strings.Contains(string(streamBody), `"role":"assistant"`) || !strings.Contains(string(streamBody), "data: [DONE]") {
		t.Fatalf("stream status = %d body=%s read error = %v", stream.StatusCode, streamBody, err)
	}
}

func TestGatewayRequiresBearerAndLoopback(t *testing.T) {
	cfg, _ := gatewayTestConfig(t, "secret-token")
	server := New(&fakeStreamer{}, staticConfig{cfg: cfg})
	handler := server.Handler()

	missing := doGatewayRequest(t, handler, http.MethodGet, "/v1/models", "", "")
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d", missing.Code)
	}
	wrong := doGatewayRequest(t, handler, http.MethodGet, "/v1/models", "other", "")
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d", wrong.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.RemoteAddr = "8.8.8.8:80"
	request.Header.Set("Authorization", "Bearer secret-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-loopback status = %d", recorder.Code)
	}
}

func TestGatewayModelsOmitsSecretsAndUnknownModelIs404(t *testing.T) {
	cfg, adapterID := gatewayTestConfig(t, "secret-token")
	server := New(&fakeStreamer{}, staticConfig{cfg: cfg})
	handler := server.Handler()

	response := doGatewayRequest(t, handler, http.MethodGet, "/v1/models", "secret-token", "")
	if response.Code != http.StatusOK {
		t.Fatalf("models status = %d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, leaked := range []string{"secret-token", "provider-secret", "api.example.com", adapterID, "token", "apiKey", "baseURL"} {
		if leaked == "token" && strings.Contains(body, `"token"`) {
			t.Fatalf("models leaked token field: %s", body)
		}
		if leaked != "token" && strings.Contains(body, leaked) {
			t.Fatalf("models leaked %q: %s", leaked, body)
		}
	}
	if !strings.Contains(body, `"public-a"`) {
		t.Fatalf("models missing public alias: %s", body)
	}

	unknown := doGatewayRequest(t, handler, http.MethodPost, "/v1/chat/completions", "secret-token", `{"model":"missing","messages":[{"role":"user","content":"hi"}]}`)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown model status = %d body=%s", unknown.Code, unknown.Body.String())
	}
	hash := doGatewayRequest(t, handler, http.MethodPost, "/v1/chat/completions", "secret-token", `{"model":"`+adapterID+`","messages":[{"role":"user","content":"hi"}]}`)
	if hash.Code != http.StatusNotFound {
		t.Fatalf("hash fallback status = %d body=%s", hash.Code, hash.Body.String())
	}
	providerID := doGatewayRequest(t, handler, http.MethodPost, "/v1/chat/completions", "secret-token", `{"model":"provider-model","messages":[{"role":"user","content":"hi"}]}`)
	if providerID.Code != http.StatusNotFound {
		t.Fatalf("provider modelID fallback status = %d", providerID.Code)
	}
}

func TestGatewayChatNonStreamAndStream(t *testing.T) {
	cfg, adapterID := gatewayTestConfig(t, "secret-token")
	fake := &fakeStreamer{}
	server := New(fake, staticConfig{cfg: cfg})
	handler := server.Handler()

	nonStream := doGatewayRequest(t, handler, http.MethodPost, "/v1/chat/completions", "secret-token", `{"model":"public-a","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	if nonStream.Code != http.StatusOK {
		t.Fatalf("non-stream status = %d body=%s", nonStream.Code, nonStream.Body.String())
	}
	if fake.lastReq.ModelID != adapterID {
		t.Fatalf("provider model = %q, want adapter %q", fake.lastReq.ModelID, adapterID)
	}
	var completion map[string]any
	if err := json.Unmarshal(nonStream.Body.Bytes(), &completion); err != nil {
		t.Fatalf("decode completion: %v", err)
	}
	choices, _ := completion["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices = %#v", completion["choices"])
	}
	usage, _ := completion["usage"].(map[string]any)
	if usage["prompt_tokens"] != float64(3) || usage["completion_tokens"] != float64(1) {
		t.Fatalf("usage = %#v", usage)
	}

	stream := doGatewayRequest(t, handler, http.MethodPost, "/v1/chat/completions", "secret-token", `{"model":"public-a","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if stream.Code != http.StatusOK {
		t.Fatalf("stream status = %d body=%s", stream.Code, stream.Body.String())
	}
	if !strings.Contains(stream.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content-type = %q", stream.Header().Get("Content-Type"))
	}
	text := stream.Body.String()
	if !strings.Contains(text, `"content":"hello"`) || !strings.Contains(text, "data: [DONE]") {
		t.Fatalf("sse body = %s", text)
	}

	textArray := doGatewayRequest(t, handler, http.MethodPost, "/v1/chat/completions", "secret-token", `{"model":"public-a","messages":[{"role":"user","content":[{"type":"text","text":"hello "},{"type":"text","text":"array"}]}]}`)
	if textArray.Code != http.StatusOK {
		t.Fatalf("text content array status = %d body=%s", textArray.Code, textArray.Body.String())
	}
	if fake.lastReq.Messages[0].Content != "hello array" {
		t.Fatalf("text content array = %q", fake.lastReq.Messages[0].Content)
	}
}

func TestGatewayChatToolsNonStreamStreamAndReplay(t *testing.T) {
	cfg, _ := gatewayTestConfig(t, "secret-token")
	fake := &fakeStreamer{start: func(_ context.Context, _ forwarder.ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
		for _, invocation := range []*runtimecore.ToolInvocation{
			{CallID: "call-read", ToolName: "read", ArgsJSON: []byte(`{"path":"a.txt"}`)},
			{CallID: "call-shell", ToolName: "shell", ArgsJSON: []byte(`{"command":"pwd"}`)},
		} {
			if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindToolLikeCompleted, ToolInvocation: invocation}); err != nil {
				return err
			}
		}
		return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTurnFinished, FinishReason: "tool_use"})
	}}
	handler := New(fake, staticConfig{cfg: cfg}).Handler()
	body := `{"model":"public-a","messages":[{"role":"user","content":"inspect"},{"role":"assistant","content":"","tool_calls":[{"id":"previous","type":"function","function":{"name":"read","arguments":"{\"path\":\"old.txt\"}"}}]},{"role":"tool","tool_call_id":"previous","name":"read","content":"old"}],"tools":[{"type":"function","function":{"name":"read","description":"read file","parameters":{"type":"object"}}},{"type":"function","function":{"name":"shell","parameters":{"type":"object"}}}],"tool_choice":"auto"}`

	nonStream := doGatewayRequest(t, handler, http.MethodPost, chatCompletionsPath, "secret-token", body)
	if nonStream.Code != http.StatusOK {
		t.Fatalf("non-stream tools status = %d body=%s", nonStream.Code, nonStream.Body.String())
	}
	if len(fake.lastReq.Tools) != 2 || len(fake.lastReq.Messages) != 3 {
		t.Fatalf("provider request tools=%d messages=%d", len(fake.lastReq.Tools), len(fake.lastReq.Messages))
	}
	if fake.lastReq.Messages[1].ToolCalls[0].ID != "previous" || fake.lastReq.Messages[2].ToolCallID != "previous" {
		t.Fatalf("tool replay = %#v %#v", fake.lastReq.Messages[1], fake.lastReq.Messages[2])
	}
	var completion map[string]any
	if err := json.Unmarshal(nonStream.Body.Bytes(), &completion); err != nil {
		t.Fatalf("decode tools completion: %v", err)
	}
	encoded, _ := json.Marshal(completion["choices"])
	for _, expected := range []string{`"finish_reason":"tool_calls"`, `"id":"call-read"`, `"id":"call-shell"`, `"arguments":"{\"path\":\"a.txt\"}"`} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("completion missing %s: %s", expected, encoded)
		}
	}

	stream := doGatewayRequest(t, handler, http.MethodPost, chatCompletionsPath, "secret-token", strings.TrimSuffix(body, "}")+`,"stream":true}`)
	if stream.Code != http.StatusOK {
		t.Fatalf("stream tools status = %d body=%s", stream.Code, stream.Body.String())
	}
	streamBody := stream.Body.String()
	for _, expected := range []string{`"tool_calls":[{"function":{"arguments":"{\"path\":\"a.txt\"}","name":"read"}`, `"index":1`, `"finish_reason":"tool_calls"`, "data: [DONE]"} {
		if !strings.Contains(streamBody, expected) {
			t.Fatalf("stream missing %s: %s", expected, streamBody)
		}
	}
}

func TestOpenCodeToolLoopAgainstGateway(t *testing.T) {
	opencodePath, err := exec.LookPath("opencode")
	if err != nil {
		candidate := filepath.Join(os.Getenv("HOME"), ".opencode", "bin", "opencode")
		if _, statErr := os.Stat(candidate); statErr != nil {
			t.Skip("OpenCode is not installed")
		}
		opencodePath = candidate
	}

	token, err := serverconfig.GenerateGatewayToken()
	if err != nil {
		t.Fatalf("GenerateGatewayToken() error = %v", err)
	}
	cfg, _ := gatewayTestConfig(t, token)
	var calls atomic.Int32
	var sawToolResult atomic.Bool
	fake := &fakeStreamer{start: func(_ context.Context, req forwarder.ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
		calls.Add(1)
		for _, message := range req.Messages {
			if message.Role == "tool" && strings.TrimSpace(message.ToolCallID) == "opencode-read" {
				sawToolResult.Store(true)
			}
		}
		if sawToolResult.Load() {
			if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "OPENCODE_GATEWAY_OK"}); err != nil {
				return err
			}
			return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTurnFinished, FinishReason: "stop"})
		}
		return sink(modeladapter.ModelEvent{
			Kind: modeladapter.ModelEventKindToolLikeCompleted,
			ToolInvocation: &runtimecore.ToolInvocation{
				CallID:   "opencode-read",
				ToolName: "read",
				ArgsJSON: []byte(`{"filePath":"gateway-smoke.txt"}`),
			},
		})
	}}
	server := New(fake, staticConfig{cfg: cfg})
	gatewayCfg := cfg.Gateway
	gatewayCfg.ListenAddr = serverconfig.DefaultGatewayListenAddr
	if err := server.Start(gatewayCfg); err != nil {
		t.Skipf("isolated Gateway port unavailable: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Stop(ctx)
	})

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "gateway-smoke.txt"), []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write smoke file: %v", err)
	}
	config := map[string]any{
		"provider": map[string]any{
			"gateway": map[string]any{
				"npm":  "@ai-sdk/openai-compatible",
				"name": "Gateway",
				"options": map[string]any{
					"baseURL": "http://" + server.ListenAddr() + "/v1",
					"apiKey":  token,
				},
				"models": map[string]any{
					"public-a": map[string]any{"name": "Public A", "tool_call": true},
				},
			},
		},
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal OpenCode config: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, opencodePath, "run", "Read gateway-smoke.txt once, then answer with the exact marker requested by the model.", "--model", "gateway/public-a", "--format", "json", "--dir", root)
	command.Env = append(os.Environ(),
		"OPENCODE_CONFIG_CONTENT="+string(configJSON),
		"OPENCODE_DISABLE_AUTOUPDATE=true",
		"OPENCODE_DISABLE_MODELS_FETCH=true",
	)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("OpenCode smoke timed out: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("OpenCode smoke failed: %v output=%s", err, output)
	}
	if !sawToolResult.Load() || calls.Load() < 2 || !strings.Contains(string(output), "OPENCODE_GATEWAY_OK") {
		t.Fatalf("OpenCode loop calls=%d tool_result=%t output=%s", calls.Load(), sawToolResult.Load(), output)
	}
}

func TestGatewayRejectsToolsMultimodalAndReasoning(t *testing.T) {
	cfg, _ := gatewayTestConfig(t, "secret-token")
	server := New(&fakeStreamer{}, staticConfig{cfg: cfg})
	handler := server.Handler()
	cases := []string{
		`{"model":"public-a","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`,
		`{"model":"public-a","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`,
		`{"model":"public-a","messages":[{"role":"user","content":"hi"}],"tool_choice":"required"}`,
		`{"model":"public-a","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"web_search"}]}`,
	}
	for _, body := range cases {
		response := doGatewayRequest(t, handler, http.MethodPost, "/v1/chat/completions", "secret-token", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d for %s body=%s", response.Code, body, response.Body.String())
		}
	}
}

func TestGatewayStaleMappingIs400(t *testing.T) {
	cfg, _ := gatewayTestConfig(t, "secret-token")
	cfg.Gateway.PublicModels = []serverconfig.GatewayPublicModel{{ID: "stale-a", TargetAdapterID: "missing"}}
	normalized, err := serverconfig.NormalizeConfig(cfg)
	if err != nil {
		t.Fatalf("NormalizeConfig() error = %v", err)
	}
	server := New(&fakeStreamer{}, staticConfig{cfg: normalized})
	response := doGatewayRequest(t, server.Handler(), http.MethodPost, "/v1/chat/completions", "secret-token", `{"model":"stale-a","messages":[{"role":"user","content":"hi"}]}`)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "mapping_invalid") {
		t.Fatalf("stale mapping = %d %s", response.Code, response.Body.String())
	}
}

func TestGatewayCancelStopsStream(t *testing.T) {
	cfg, _ := gatewayTestConfig(t, "secret-token")
	started := make(chan struct{})
	fake := &fakeStreamer{start: func(ctx context.Context, req forwarder.ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}}
	server := New(fake, staticConfig{cfg: cfg})
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"public-a","messages":[{"role":"user","content":"hi"}]}`))
	request = request.WithContext(ctx)
	request.RemoteAddr = "127.0.0.1:1"
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("Content-Type", "application/json")
	go func() {
		<-started
		cancel()
	}()
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != 499 && recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusOK {
		t.Fatalf("cancel status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestGatewayDoesNotWriteLastAgentModelHash(t *testing.T) {
	cfg, _ := gatewayTestConfig(t, "secret-token")
	if cfg.LastAgentModelHash != "" {
		t.Fatalf("seed hash = %q", cfg.LastAgentModelHash)
	}
	source := &recordingConfig{cfg: cfg}
	server := New(&fakeStreamer{}, source)
	_ = doGatewayRequest(t, server.Handler(), http.MethodPost, "/v1/chat/completions", "secret-token", `{"model":"public-a","messages":[{"role":"user","content":"hi"}]}`)
	if source.cfg.LastAgentModelHash != "" {
		t.Fatalf("gateway wrote lastAgentModelHash = %q", source.cfg.LastAgentModelHash)
	}
}

type recordingConfig struct {
	cfg serverconfig.Config
}

func (source *recordingConfig) Current() serverconfig.Config {
	return source.cfg
}

func TestParseChatCompletionRequestRejectsUnsupported(t *testing.T) {
	_, err := parseChatCompletionRequest([]byte(`{"model":"a","messages":[{"role":"user","content":"hi"}],"functions":[]}`))
	if err == nil || !strings.Contains(err.Error(), "functions") {
		t.Fatalf("functions error = %v", err)
	}
}

func TestWriteAPIErrorShape(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeAPIError(recorder, http.StatusUnauthorized, "invalid_request_error", "invalid_api_key", "invalid API key")
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errorObject, _ := payload["error"].(map[string]any)
	if errorObject["code"] != "invalid_api_key" {
		t.Fatalf("error = %#v", payload)
	}
}

func TestGatewayListenRequiresLoopback(t *testing.T) {
	cfg, _ := gatewayTestConfig(t, "secret-token")
	server := New(&fakeStreamer{}, staticConfig{cfg: cfg})
	gatewayCfg := cfg.Gateway
	gatewayCfg.ListenAddr = "0.0.0.0:0"
	if err := server.Start(gatewayCfg); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("Start(non-loopback) error = %v", err)
	}
	gatewayCfg.ListenAddr = "127.0.0.1:0"
	if err := server.Start(gatewayCfg); err != nil {
		t.Fatalf("Start(loopback) error = %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	if !server.Running() || !strings.HasPrefix(server.ListenAddr(), "127.0.0.1:") {
		t.Fatalf("listen = %q running=%t", server.ListenAddr(), server.Running())
	}
	if _, err := io.Copy(io.Discard, bytes.NewReader(nil)); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayAuthorizeUsesConstantTimeCompare(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("ReadFile(server.go): %v", err)
	}
	text := string(source)
	if strings.Contains(text, "provided != expected") || strings.Contains(text, "provided == expected") {
		t.Fatal("Bearer token compare must not use ordinary string equality")
	}
	if !strings.Contains(text, "subtle.ConstantTimeCompare") {
		t.Fatal("Bearer token compare must use crypto/subtle.ConstantTimeCompare")
	}
	if !strings.Contains(text, "sha256.Sum256") {
		t.Fatal("Bearer token compare must normalize lengths before constant-time comparison")
	}
}

func TestGatewayChatRequiresJSONContentType(t *testing.T) {
	cfg, _ := gatewayTestConfig(t, "secret-token")
	handler := New(&fakeStreamer{}, staticConfig{cfg: cfg}).Handler()
	body := `{"model":"public-a","messages":[{"role":"user","content":"hi"}]}`

	post := func(contentType string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		request.RemoteAddr = "127.0.0.1:4321"
		request.Header.Set("Authorization", "Bearer secret-token")
		if contentType != "" {
			request.Header.Set("Content-Type", contentType)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	missing := post("")
	if missing.Code != http.StatusUnsupportedMediaType || !strings.Contains(missing.Body.String(), "invalid_content_type") {
		t.Fatalf("missing content-type = %d %s", missing.Code, missing.Body.String())
	}
	plain := post("text/plain")
	if plain.Code != http.StatusUnsupportedMediaType || !strings.Contains(plain.Body.String(), "invalid_content_type") {
		t.Fatalf("text/plain = %d %s", plain.Code, plain.Body.String())
	}
	charset := post("application/json; charset=utf-8")
	if charset.Code != http.StatusOK {
		t.Fatalf("json charset = %d %s", charset.Code, charset.Body.String())
	}
}

func TestGatewayStartRestartClosesPreviousListener(t *testing.T) {
	cfg, _ := gatewayTestConfig(t, "secret-token")
	server := New(&fakeStreamer{}, staticConfig{cfg: cfg})
	gatewayCfg := cfg.Gateway
	gatewayCfg.ListenAddr = "127.0.0.1:0"
	if err := server.Start(gatewayCfg); err != nil {
		t.Fatalf("first Start() error = %v", err)
	}
	first := server.ListenAddr()
	if err := server.Start(gatewayCfg); err != nil {
		t.Fatalf("restart Start() error = %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	second := server.ListenAddr()
	if first == "" || second == "" || !server.Running() {
		t.Fatalf("listen first=%q second=%q running=%t", first, second, server.Running())
	}
	if first == second {
		return
	}
	conn, err := net.DialTimeout("tcp", first, 200*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("old listener %s still accepting after restart to %s", first, second)
	}
}
