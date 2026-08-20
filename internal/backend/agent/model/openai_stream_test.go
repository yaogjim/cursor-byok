package modeladapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIResponsesRequestsReasoningSummary(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"check the request\"}\n\n")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"model\":\"gpt-5.6\",\"status\":\"completed\",\"output_text\":\"done\"}}\n\n")
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	adapter := &OpenAIAdapter{client: server.Client()}
	events := make([]ModelEvent, 0, 4)
	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:       "request-1",
		RunID:           "run-1",
		ModelCallID:     "model-call-1",
		BaseURL:         server.URL,
		APIKey:          "test-key",
		ProviderModelID: "gpt-5.6",
		OpenAIEndpoint:  "/v1/responses",
		ReasoningEffort: "high",
		Messages:        []Message{{Role: "user", Content: "hello"}},
		MaxTokens:       128,
	}, func(event ModelEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}

	reasoning, ok := requestBody["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("reasoning request body missing: %#v", requestBody)
	}
	if got := reasoning["effort"]; got != "high" {
		t.Fatalf("reasoning.effort = %#v, want high", got)
	}
	if got := reasoning["summary"]; got != "auto" {
		t.Fatalf("reasoning.summary = %#v, want auto", got)
	}
	include, ok := requestBody["include"].([]any)
	if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("reasoning include = %#v, want encrypted content", requestBody["include"])
	}
	assertOpenAIEventKindCount(t, events, ModelEventKindThinkingDelta, 1)
	assertOpenAIEventKindCount(t, events, ModelEventKindThinkingCompleted, 1)
	assertOpenAIEventKindCount(t, events, ModelEventKindTextDelta, 1)
}

func TestOpenAIResponsesOmitsReasoningWhenEffortBlank(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"model\":\"grok-composer-2.5-fast\",\"status\":\"completed\",\"output_text\":\"done\"}}\n\n")
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	adapter := &OpenAIAdapter{client: server.Client()}
	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:       "request-1",
		RunID:           "run-1",
		ModelCallID:     "model-call-1",
		BaseURL:         server.URL,
		APIKey:          "test-key",
		ProviderModelID: "grok-composer-2.5-fast",
		OpenAIEndpoint:  "/v1/responses",
		Messages:        []Message{{Role: "user", Content: "hello"}},
		MaxTokens:       128,
	}, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}

	if _, exists := requestBody["reasoning"]; exists {
		t.Fatalf("reasoning should be omitted when effort is blank: %#v", requestBody["reasoning"])
	}
	if _, exists := requestBody["reasoning_effort"]; exists {
		t.Fatalf("reasoning_effort should be omitted when effort is blank: %#v", requestBody["reasoning_effort"])
	}
}

func TestOpenAIChatCompletionsOmitsReasoningWhenEffortBlank(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "data: {\"model\":\"grok-composer-2.5-fast\",\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	adapter := &OpenAIAdapter{client: server.Client()}
	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:       "request-1",
		RunID:           "run-1",
		ModelCallID:     "model-call-1",
		BaseURL:         server.URL,
		APIKey:          "test-key",
		ProviderModelID: "grok-composer-2.5-fast",
		OpenAIEndpoint:  "/v1/chat/completions",
		Messages:        []Message{{Role: "user", Content: "hello"}},
		MaxTokens:       128,
	}, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}

	for _, field := range []string{"reasoning_effort", "reasoning", "include"} {
		if value, exists := requestBody[field]; exists {
			t.Fatalf("%s should be omitted when effort is blank: %#v", field, value)
		}
	}
}

func TestOpenAIChatCompletionsIgnoresBlankFinishReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"model":"deepseek-v4-flash","choices":[{"delta":{"reasoning_content":"first"},"finish_reason":""}]}`,
			`{"model":"deepseek-v4-flash","choices":[{"delta":{"reasoning_content":" second"},"finish_reason":""}]}`,
			`{"model":"deepseek-v4-flash","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Ls","arguments":""}}]},"finish_reason":""}]}`,
			`{"model":"deepseek-v4-flash","choices":[{"delta":{"tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"","arguments":"{\"path\":"}}]},"finish_reason":""}]}`,
			`{"model":"deepseek-v4-flash","choices":[{"delta":{"tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"","arguments":"\"/tmp\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"model":"deepseek-v4-flash","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":7}}`,
		}
		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(writer, "data: %s\n\n", chunk)
		}
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	adapter := &OpenAIAdapter{client: server.Client()}
	events := make([]ModelEvent, 0, 8)
	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:       "request-1",
		RunID:           "run-1",
		ModelCallID:     "model-call-1",
		BaseURL:         server.URL,
		APIKey:          "test-key",
		ProviderModelID: "deepseek-v4-flash",
		OpenAIEndpoint:  "/v1/chat/completions",
		Messages:        []Message{{Role: "user", Content: "list files"}},
		MaxTokens:       128,
		RequestKnobs:    map[string]any{},
	}, func(event ModelEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}

	assertOpenAIEventKindCount(t, events, ModelEventKindThinkingDelta, 2)
	assertOpenAIEventKindCount(t, events, ModelEventKindThinkingCompleted, 1)
	assertOpenAIEventKindCount(t, events, ModelEventKindToolLikeCompleted, 1)
	assertOpenAIEventKindCount(t, events, ModelEventKindTurnFinished, 1)

	toolEvent := firstOpenAIEventForTest(events, ModelEventKindToolLikeCompleted)
	if toolEvent == nil || toolEvent.ToolInvocation == nil {
		t.Fatalf("completed tool invocation missing: %#v", toolEvent)
	}
	if toolEvent.ToolInvocation.ToolName != "Ls" {
		t.Fatalf("tool name = %q, want Ls", toolEvent.ToolInvocation.ToolName)
	}
	if got := string(toolEvent.ToolInvocation.ArgsJSON); got != `{"path":"/tmp"}` {
		t.Fatalf("tool args = %q, want %q", got, `{"path":"/tmp"}`)
	}

	finished := firstOpenAIEventForTest(events, ModelEventKindTurnFinished)
	if finished == nil || finished.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q, want tool_calls", finished.FinishReason)
	}
	if finished.InputTokens != 12 || finished.OutputTokens != 7 {
		t.Fatalf("usage = input:%d output:%d, want input:12 output:7", finished.InputTokens, finished.OutputTokens)
	}
}

func TestOpenAIChatCompletionsScannerErrorDoesNotCompleteResidualTool(t *testing.T) {
	payload := "data: {\"model\":\"gpt-test\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"Ls\",\"arguments\":\"{\\\"path\\\":\"}}]},\"finish_reason\":\"\"}]}\n\n"
	adapter := &OpenAIAdapter{client: newOpenAIStreamErrorClient(t, payload, errors.New("connection reset"))}
	events, err := collectOpenAIStreamEvents(t, adapter, "/v1/chat/completions")
	assertOpenAIStreamTruncated(t, err, "connection reset")
	assertOpenAIEventKindCount(t, events, ModelEventKindToolLikeCompleted, 0)
	assertOpenAIEventKindCount(t, events, ModelEventKindTurnFinished, 0)
}

func TestOpenAIResponsesScannerErrorDoesNotCompleteResidualTool(t *testing.T) {
	payload := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Ls","arguments":""}}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"path\":"}`,
		"",
	}, "\n\n")
	adapter := &OpenAIAdapter{client: newOpenAIStreamErrorClient(t, payload+"\n", errors.New("connection reset"))}
	events, err := collectOpenAIStreamEvents(t, adapter, "/v1/responses")
	assertOpenAIStreamTruncated(t, err, "connection reset")
	assertOpenAIEventKindCount(t, events, ModelEventKindToolLikeCompleted, 0)
	assertOpenAIEventKindCount(t, events, ModelEventKindTurnFinished, 0)
}

func TestOpenAIChatCompletionsEOFWithoutFinishMarker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "data: {\"model\":\"gpt-test\",\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"\"}]}\n\n")
	}))
	defer server.Close()

	adapter := &OpenAIAdapter{client: server.Client()}
	events, err := collectOpenAIStreamEventsWithServer(t, adapter, server.URL, "/v1/chat/completions")
	assertOpenAIStreamTruncated(t, err, "missing completion marker")
	assertOpenAIEventKindCount(t, events, ModelEventKindTextDelta, 1)
	assertOpenAIEventKindCount(t, events, ModelEventKindTurnFinished, 0)
}

func TestOpenAIResponsesEOFWithoutFinishMarker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
	}))
	defer server.Close()

	adapter := &OpenAIAdapter{client: server.Client()}
	events, err := collectOpenAIStreamEventsWithServer(t, adapter, server.URL, "/v1/responses")
	assertOpenAIStreamTruncated(t, err, "missing completion marker")
	assertOpenAIEventKindCount(t, events, ModelEventKindTextDelta, 1)
	assertOpenAIEventKindCount(t, events, ModelEventKindTurnFinished, 0)
}

func TestOpenAIChatCompletionsNormalCompletionStillSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "data: {\"model\":\"gpt-test\",\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	adapter := &OpenAIAdapter{client: server.Client()}
	events, err := collectOpenAIStreamEventsWithServer(t, adapter, server.URL, "/v1/chat/completions")
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	assertOpenAIEventKindCount(t, events, ModelEventKindTextDelta, 1)
	assertOpenAIEventKindCount(t, events, ModelEventKindTurnFinished, 1)
}

func TestOpenAIResponsesNormalCompletionStillSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\n")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"model\":\"gpt-test\",\"status\":\"completed\"}}\n\n")
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	adapter := &OpenAIAdapter{client: server.Client()}
	events, err := collectOpenAIStreamEventsWithServer(t, adapter, server.URL, "/v1/responses")
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	assertOpenAIEventKindCount(t, events, ModelEventKindTextDelta, 1)
	assertOpenAIEventKindCount(t, events, ModelEventKindTurnFinished, 1)
}

func collectOpenAIStreamEvents(t *testing.T, adapter *OpenAIAdapter, endpoint string) ([]ModelEvent, error) {
	t.Helper()
	return collectOpenAIStreamEventsWithServer(t, adapter, "https://example.test", endpoint)
}

func collectOpenAIStreamEventsWithServer(t *testing.T, adapter *OpenAIAdapter, baseURL string, endpoint string) ([]ModelEvent, error) {
	t.Helper()
	events := make([]ModelEvent, 0, 8)
	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:       "request-1",
		RunID:           "run-1",
		ModelCallID:     "model-call-1",
		BaseURL:         baseURL,
		APIKey:          "test-key",
		ProviderModelID: "gpt-test",
		OpenAIEndpoint:  endpoint,
		Messages:        []Message{{Role: "user", Content: "hello"}},
		MaxTokens:       128,
	}, func(event ModelEvent) error {
		events = append(events, event)
		return nil
	})
	return events, err
}

func newOpenAIStreamErrorClient(t *testing.T, payload string, readErr error) *http.Client {
	t.Helper()
	return &http.Client{Transport: streamErrorRoundTripper{payload: payload, err: readErr}}
}

type streamErrorRoundTripper struct {
	payload string
	err     error
}

func (transport streamErrorRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       &errorAfterReader{data: []byte(transport.payload), err: transport.err},
		Request:    request,
	}, nil
}

type errorAfterReader struct {
	data []byte
	err  error
}

func (reader *errorAfterReader) Read(p []byte) (int, error) {
	if len(reader.data) == 0 {
		return 0, reader.err
	}
	n := copy(p, reader.data)
	reader.data = reader.data[n:]
	return n, nil
}

func (reader *errorAfterReader) Close() error { return nil }

func assertOpenAIStreamTruncated(t *testing.T, err error, wantSubstring string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected truncated stream error, got nil")
	}
	var truncated *StreamTruncatedError
	if !errors.As(err, &truncated) {
		t.Fatalf("error type = %T (%v), want StreamTruncatedError", err, err)
	}
	if ClassifyProviderError(err) != ProviderErrorStreamDecode {
		t.Fatalf("category = %q, want %q", ClassifyProviderError(err), ProviderErrorStreamDecode)
	}
	if wantSubstring != "" && !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("error = %q, want substring %q", err.Error(), wantSubstring)
	}
}

func assertOpenAIEventKindCount(t *testing.T, events []ModelEvent, kind ModelEventKind, want int) {
	t.Helper()
	got := 0
	for _, event := range events {
		if event.Kind == kind {
			got++
		}
	}
	if got != want {
		t.Fatalf("event kind %s count = %d, want %d; events=%#v", kind, got, want, events)
	}
}

func firstOpenAIEventForTest(events []ModelEvent, kind ModelEventKind) *ModelEvent {
	for index := range events {
		if events[index].Kind == kind {
			return &events[index]
		}
	}
	return nil
}
