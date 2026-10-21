package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	runtimecore "cursor/internal/backend/agent/core"
	modeladapter "cursor/internal/backend/agent/model"
	"cursor/internal/backend/forwarder"
)

func TestParseResponsesRequestMapsTypedInputAndTools(t *testing.T) {
	parsed, err := parseResponsesRequest([]byte(`{
		"model":"public-a","instructions":"be concise","stream":true,"store":false,
		"prompt_cache_key":"conversation-a","reasoning":{"effort":"high","summary":"auto"},
		"tools":[{"type":"function","name":"read_file","description":"read","parameters":{"type":"object"},"strict":true}],
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"read it"}]},
			{"type":"reasoning","id":"rs_1","status":"completed","encrypted_content":"opaque","summary":[]},
			{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"a\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"done"}
		]
	}`))
	if err != nil {
		t.Fatalf("parseResponsesRequest() error = %v", err)
	}
	if parsed.Model != "public-a" || parsed.ConversationID != "conversation-a" || parsed.ThinkingEffort != "high" {
		t.Fatalf("parsed identity = %#v", parsed)
	}
	if len(parsed.Messages) != 5 || parsed.Messages[0].Role != "system" || parsed.Messages[1].Content != "read it" {
		t.Fatalf("messages = %#v", parsed.Messages)
	}
	reasoning := parsed.Messages[2]
	if reasoning.ReasoningSignature != "opaque" || reasoning.OpenAIResponsesReasoningID != "rs_1" {
		t.Fatalf("reasoning = %#v", reasoning)
	}
	call := parsed.Messages[3].ToolCalls[0]
	if call.OpenAIResponsesCallID != "call_1" || call.OpenAIResponsesID != "fc_1" || parsed.Messages[4].ToolCallID != "call_1" {
		t.Fatalf("call=%#v output=%#v", call, parsed.Messages[4])
	}
	if len(parsed.Tools) != 1 || !strings.Contains(string(parsed.Tools[0]), `"function"`) {
		t.Fatalf("tools = %s", parsed.Tools)
	}
}

func TestGatewayResponsesStreamsTextToolsUsageAndCompleted(t *testing.T) {
	cfg, _ := gatewayTestConfig(t, "secret-token")
	fake := &fakeStreamer{start: func(_ context.Context, req forwarder.ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
		if req.Messages[0].Content != "hello" || len(req.Tools) != 1 {
			t.Fatalf("provider request = %#v", req)
		}
		if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "hi"}); err != nil {
			return err
		}
		if err := sink(modeladapter.ModelEvent{
			Kind: modeladapter.ModelEventKindToolLikeCompleted,
			ToolInvocation: &runtimecore.ToolInvocation{
				CallID: "internal", ProviderCallID: "call_original", ProviderItemID: "fc_original",
				ToolName: "read_file", ArgsJSON: []byte(`{"path":"a"}`), ProviderStatus: "completed",
			},
		}); err != nil {
			return err
		}
		return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTurnFinished, UsagePresent: true, InputTokens: 8, OutputTokens: 3, CacheReadTokens: 2})
	}}
	server := New(fake, staticConfig{cfg: cfg})
	body := `{"model":"public-a","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]}`
	response := doGatewayRequest(t, server.Handler(), http.MethodPost, responsesPath, "secret-token", body)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	stream := response.Body.String()
	for _, required := range []string{
		`"type":"response.created"`, `"type":"response.output_text.delta"`, `"delta":"hi"`,
		`"type":"response.output_item.done"`, `"call_id":"call_original"`, `"id":"fc_original"`,
		`"type":"response.completed"`, `"input_tokens":8`, `"cached_tokens":2`, `"output_tokens":3`, `"total_tokens":11`,
	} {
		if !strings.Contains(stream, required) {
			t.Fatalf("stream missing %s: %s", required, stream)
		}
	}
	if strings.Count(stream, `"type":"response.completed"`) != 1 {
		t.Fatalf("completed count stream=%s", stream)
	}
}

func TestGatewayResponsesMixedTextAndToolOutputIndicesStayOrdered(t *testing.T) {
	cfg, _ := gatewayTestConfig(t, "secret-token")
	fake := &fakeStreamer{start: func(_ context.Context, _ forwarder.ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
		for _, event := range []modeladapter.ModelEvent{
			{Kind: modeladapter.ModelEventKindTextDelta, Text: "before"},
			{Kind: modeladapter.ModelEventKindToolLikeCompleted, ToolInvocation: &runtimecore.ToolInvocation{
				CallID: "internal", ProviderCallID: "call_mixed", ProviderItemID: "fc_mixed",
				ToolName: "read_file", ArgsJSON: []byte(`{"path":"a"}`), ProviderStatus: "completed",
			}},
			{Kind: modeladapter.ModelEventKindTextDelta, Text: "after"},
		} {
			if err := sink(event); err != nil {
				return err
			}
		}
		return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTurnFinished})
	}}
	body := `{"model":"public-a","stream":true,"input":"hello","tools":[{"type":"function","name":"read_file"}]}`
	response := doGatewayRequest(t, New(fake, staticConfig{cfg: cfg}).Handler(), http.MethodPost, responsesPath, "secret-token", body)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	var events []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(response.Body.String()))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") || strings.TrimSpace(strings.TrimPrefix(line, "data: ")) == "[DONE]" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatalf("decode SSE event %q: %v", line, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	var deltaIndex, toolIndex, textDoneIndex float64
	var completed map[string]any
	for _, event := range events {
		switch event["type"] {
		case "response.output_text.delta":
			if event["delta"] == "before" {
				deltaIndex = event["output_index"].(float64)
			}
		case "response.output_item.done":
			item := event["item"].(map[string]any)
			if item["type"] == "function_call" {
				toolIndex = event["output_index"].(float64)
			}
			if item["type"] == "message" {
				textDoneIndex = event["output_index"].(float64)
			}
		case "response.completed":
			completed = event["response"].(map[string]any)
		}
	}
	if deltaIndex != 0 || toolIndex != 1 || textDoneIndex != 0 {
		t.Fatalf("SSE output indices delta=%v tool=%v text_done=%v stream=%s", deltaIndex, toolIndex, textDoneIndex, response.Body.String())
	}
	output := completed["output"].([]any)
	if len(output) != 2 || output[0].(map[string]any)["type"] != "message" || output[1].(map[string]any)["type"] != "function_call" {
		t.Fatalf("completed output order = %#v", output)
	}
}

func TestGatewayResponsesFailedHasNoCompleted(t *testing.T) {
	cfg, _ := gatewayTestConfig(t, "secret-token")
	fake := &fakeStreamer{start: func(_ context.Context, _ forwarder.ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
		if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "partial"}); err != nil {
			return err
		}
		return errors.New("fixture provider failed")
	}}
	response := doGatewayRequest(t, New(fake, staticConfig{cfg: cfg}).Handler(), http.MethodPost, responsesPath, "secret-token", `{"model":"public-a","stream":true,"input":"hello"}`)
	stream := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(stream, `"type":"response.failed"`) || strings.Contains(stream, `"type":"response.completed"`) {
		t.Fatalf("status=%d stream=%s", response.Code, stream)
	}
}

func TestCodexWriteToolAgainstGateway(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real Codex CLI smoke in short mode")
	}
	codexPath := "/opt/homebrew/bin/codex"
	if _, err := os.Stat(codexPath); err != nil {
		t.Skipf("Codex not installed at %s", codexPath)
	}
	token := "smoke-token"
	cfg, _ := gatewayTestConfig(t, token)
	var calls atomic.Int32
	fake := &fakeStreamer{start: func(_ context.Context, req forwarder.ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
		if calls.Add(1) == 1 {
			return sink(modeladapter.ModelEvent{
				Kind: modeladapter.ModelEventKindToolLikeCompleted,
				ToolInvocation: &runtimecore.ToolInvocation{
					CallID: "codex-write-1", ProviderCallID: "codex-write-1", ProviderItemID: "fc_codex_write_1",
					ToolName: "exec_command", ArgsJSON: []byte(`{"cmd":"printf CODEX_WRITE_OK > gateway-codex-write.txt"}`), ProviderStatus: "completed",
				},
			})
		}
		if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "CODEX_GATEWAY_WRITE_DONE"}); err != nil {
			return err
		}
		return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTurnFinished, UsagePresent: true, InputTokens: 10, OutputTokens: 2})
	}}
	server := New(fake, staticConfig{cfg: cfg})
	gatewayCfg := cfg.Gateway
	gatewayCfg.ListenAddr = "127.0.0.1:0"
	if err := server.Start(gatewayCfg); err != nil {
		t.Fatalf("start Gateway: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })

	root := t.TempDir()
	codexHome := filepath.Join(root, ".codex-smoke")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	config := `model = "public-a"
model_provider = "gateway"

[model_providers.gateway]
name = "Gateway"
base_url = "http://` + server.ListenAddr() + `/v1"
env_key = "GATEWAY_SMOKE_TOKEN"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
`
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write Codex config: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, codexPath, "exec", "Create the exact file requested by the model, then return its exact completion marker.", "--json", "--ephemeral", "--skip-git-repo-check", "--sandbox", "workspace-write", "--cd", root)
	command.Env = append(os.Environ(), "CODEX_HOME="+codexHome, "GATEWAY_SMOKE_TOKEN="+token)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("Codex write smoke timed out: %v output=%s", ctx.Err(), output)
	}
	if err != nil {
		t.Fatalf("Codex write smoke failed: %v output=%s", err, output)
	}
	written, readErr := os.ReadFile(filepath.Join(root, "gateway-codex-write.txt"))
	if readErr != nil || string(written) != "CODEX_WRITE_OK" || calls.Load() < 2 || !strings.Contains(string(output), "CODEX_GATEWAY_WRITE_DONE") {
		t.Fatalf("Codex write calls=%d file=%q read_err=%v output=%s", calls.Load(), written, readErr, output)
	}
}

func TestGatewayResponsesRejectsUnsupportedStateAndTools(t *testing.T) {
	cfg, _ := gatewayTestConfig(t, "secret-token")
	handler := New(&fakeStreamer{}, staticConfig{cfg: cfg}).Handler()
	cases := []string{
		`{"model":"public-a","stream":false,"input":"hi"}`,
		`{"model":"public-a","stream":true,"store":true,"input":"hi"}`,
		`{"model":"public-a","stream":true,"previous_response_id":"resp_1","input":"hi"}`,
		`{"model":"public-a","stream":true,"input":"hi","tools":[{"type":"custom","name":"x"}]}`,
		`{"model":"public-a","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"x"}]}]}`,
	}
	for _, body := range cases {
		response := doGatewayRequest(t, handler, http.MethodPost, responsesPath, "secret-token", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s response=%s", response.Code, body, response.Body.String())
		}
	}
}

func TestCodexToolLoopAgainstGateway(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real Codex CLI smoke in short mode")
	}
	codexPath := "/opt/homebrew/bin/codex"
	if _, err := os.Stat(codexPath); err != nil {
		t.Skipf("Codex not installed at %s", codexPath)
	}
	token := "smoke-token"
	cfg, _ := gatewayTestConfig(t, token)
	var calls atomic.Int32
	var sawToolOutput atomic.Bool
	fake := &fakeStreamer{start: func(_ context.Context, req forwarder.ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
		call := calls.Add(1)
		if call == 1 {
			return sink(modeladapter.ModelEvent{
				Kind: modeladapter.ModelEventKindToolLikeCompleted,
				ToolInvocation: &runtimecore.ToolInvocation{
					CallID: "codex-call-1", ProviderCallID: "codex-call-1", ProviderItemID: "fc_codex_1",
					ToolName: "exec_command", ArgsJSON: []byte(`{"cmd":"cat gateway-codex.txt"}`), ProviderStatus: "completed",
				},
			})
		}
		for _, message := range req.Messages {
			if message.Role == "tool" && message.ToolCallID == "codex-call-1" && strings.Contains(message.Content, "fixture-codex") {
				sawToolOutput.Store(true)
			}
		}
		if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "CODEX_GATEWAY_OK"}); err != nil {
			return err
		}
		return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTurnFinished, UsagePresent: true, InputTokens: 10, OutputTokens: 2, FinishReason: "stop"})
	}}
	server := New(fake, staticConfig{cfg: cfg})
	gatewayCfg := cfg.Gateway
	gatewayCfg.ListenAddr = "127.0.0.1:0"
	if err := server.Start(gatewayCfg); err != nil {
		t.Fatalf("start Gateway: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "gateway-codex.txt"), []byte("fixture-codex\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	codexHome := filepath.Join(root, ".codex-smoke")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	config := `model = "public-a"
model_provider = "gateway"

[model_providers.gateway]
name = "Gateway"
base_url = "http://` + server.ListenAddr() + `/v1"
env_key = "GATEWAY_SMOKE_TOKEN"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
`
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write Codex config: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, codexPath, "exec", "Read gateway-codex.txt using the shell tool, then return the exact marker requested by the model.", "--json", "--ephemeral", "--skip-git-repo-check", "--sandbox", "read-only", "--cd", root)
	command.Env = append(os.Environ(), "CODEX_HOME="+codexHome, "GATEWAY_SMOKE_TOKEN="+token)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("Codex smoke timed out: %v output=%s", ctx.Err(), output)
	}
	if err != nil {
		t.Fatalf("Codex smoke failed: %v output=%s", err, output)
	}
	if calls.Load() < 2 || !sawToolOutput.Load() || !strings.Contains(string(output), "CODEX_GATEWAY_OK") {
		t.Fatalf("Codex calls=%d tool_output=%t output=%s", calls.Load(), sawToolOutput.Load(), output)
	}

	var decoded map[string]any
	_ = json.Unmarshal([]byte(`{"ok":true}`), &decoded)
}
