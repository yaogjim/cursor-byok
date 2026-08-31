package modeladapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestOpenAIResponsesCompletedIgnoresTrailingUnexpectedEOF(t *testing.T) {
	payload := strings.Join([]string{
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Ls","arguments":"{\"path\":\".\"}","status":"completed"}}`,
		`data: {"type":"response.completed","response":{"id":"resp-1","model":"gpt-test","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Ls","arguments":"{\"path\":\".\"}","status":"completed"}]}}`,
		"",
	}, "\n\n")
	adapter := &OpenAIAdapter{client: newOpenAIStreamErrorClient(t, payload+"\n", io.ErrUnexpectedEOF)}
	events, err := collectOpenAIStreamEvents(t, adapter, "/v1/responses")
	if err != nil {
		t.Fatalf("completed response failed on trailing unexpected EOF: %v", err)
	}
	assertOpenAIEventKindCount(t, events, ModelEventKindToolLikeCompleted, 1)
	assertOpenAIEventKindCount(t, events, ModelEventKindTurnFinished, 1)
}

func TestOpenAIResponsesCompletedReturnsBeforeBodyCloses(t *testing.T) {
	payload := `data: {"type":"response.completed","response":{"id":"resp-1","model":"gpt-test","status":"completed","output_text":"done"}}` + "\n\n"
	reader := newBlockingAfterReader(payload)
	adapter := &OpenAIAdapter{client: &http.Client{Transport: streamBodyRoundTripper{body: reader}}}

	result := make(chan error, 1)
	go func() {
		_, err := collectOpenAIStreamEvents(t, adapter, "/v1/responses")
		result <- err
	}()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("completed response failed before body close: %v", err)
		}
	case <-time.After(time.Second):
		_ = reader.Close()
		<-result
		t.Fatal("completed response waited for the provider body to close")
	}
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

type streamBodyRoundTripper struct {
	body io.ReadCloser
}

func (transport streamBodyRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       transport.body,
		Request:    request,
	}, nil
}

type blockingAfterReader struct {
	data      []byte
	release   chan struct{}
	closeOnce sync.Once
}

func newBlockingAfterReader(payload string) *blockingAfterReader {
	return &blockingAfterReader{
		data:    []byte(payload),
		release: make(chan struct{}),
	}
}

func (reader *blockingAfterReader) Read(p []byte) (int, error) {
	if len(reader.data) > 0 {
		n := copy(p, reader.data)
		reader.data = reader.data[n:]
		return n, nil
	}
	<-reader.release
	return 0, io.EOF
}

func (reader *blockingAfterReader) Close() error {
	reader.closeOnce.Do(func() { close(reader.release) })
	return nil
}

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

func TestOpenAIChatPreEventEOFRetriesAndDeadlineDoesNot(t *testing.T) {
	t.Run("retries before first event", func(t *testing.T) {
		hits := 0
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			hits++
			writer.Header().Set("Content-Type", "text/event-stream")
			if hits == 2 {
				_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
			}
		}))
		defer server.Close()
		adapter := &OpenAIAdapter{client: server.Client(), retry: instantRetry()}
		_, err := collectOpenAIStreamEventsWithServer(t, adapter, server.URL, "/v1/chat/completions")
		if err != nil || hits != 2 {
			t.Fatalf("err=%v hits=%d, want nil/2", err, hits)
		}
	})
	t.Run("stops after one zero-byte retry", func(t *testing.T) {
		hits := 0
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			hits++
			writer.Header().Set("Content-Type", "text/event-stream")
		}))
		defer server.Close()
		diagnostics := &StreamDiagnostics{}
		adapter := &OpenAIAdapter{client: server.Client(), retry: instantRetry()}
		err := adapter.Stream(context.Background(), StreamRequest{
			RequestID: "request-1", ModelCallID: "model-call-1", BaseURL: server.URL, APIKey: "test-key",
			ProviderModelID: "gpt-test", Messages: []Message{{Role: "user", Content: "hello"}}, MaxTokens: 128,
			StreamDiagnostics: diagnostics,
		}, func(ModelEvent) error { return nil })
		assertOpenAIStreamTruncated(t, err, "")
		if hits != 2 || diagnostics.Snapshot().StreamRecoveryAttempts != 1 {
			t.Fatalf("hits=%d diagnostics=%#v, want 2 requests/1 recovery", hits, diagnostics.Snapshot())
		}
	})
	t.Run("deadline does not retry", func(t *testing.T) {
		hits := 0
		ctx, cancel := context.WithCancel(context.Background())
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { hits++; cancel() }))
		defer server.Close()
		adapter := &OpenAIAdapter{client: server.Client(), retry: instantRetry()}
		err := adapter.Stream(ctx, StreamRequest{RequestID: "request-1", ModelCallID: "model-call-1", BaseURL: server.URL, APIKey: "test-key", ProviderModelID: "gpt-test", Messages: []Message{{Role: "user", Content: "hello"}}, MaxTokens: 128}, func(ModelEvent) error { return nil })
		if !errors.Is(err, context.Canceled) || hits != 1 {
			t.Fatalf("err=%v hits=%d, want canceled/1", err, hits)
		}
	})
}

func TestOpenAIHalfFrameDoesNotRetryOrConcatResponses(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, `data: {"choices":[]}`+"\n\n")
	}))
	defer server.Close()
	adapter := &OpenAIAdapter{client: server.Client(), retry: instantRetry()}
	_, err := collectOpenAIStreamEventsWithServer(t, adapter, server.URL, "/v1/chat/completions")
	assertOpenAIStreamTruncated(t, err, "missing completion marker")
	if hits != 1 {
		t.Fatalf("hits=%d, want 1", hits)
	}
}

type rewriteHostRoundTripper struct {
	target *url.URL
}

func (rt rewriteHostRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = rt.target.Scheme
	clone.URL.Host = rt.target.Host
	clone.Host = rt.target.Host
	clone.RequestURI = ""
	return http.DefaultTransport.RoundTrip(clone)
}

func chatgptLoopbackClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: rewriteHostRoundTripper{target: target}}
}

func TestIsChatGPTCodexHost(t *testing.T) {
	if !isChatGPTCodexHost("https://chatgpt.com/backend-api/codex/responses") {
		t.Fatal("chatgpt.com host should match")
	}
	if isChatGPTCodexHost("https://api.openai.com/v1/responses") {
		t.Fatal("api.openai.com must not match")
	}
}

func TestFilterCodexResponsesBodyWhitelist(t *testing.T) {
	body := map[string]any{
		"model":                "gpt-5.6-luna",
		"input":                []any{map[string]any{"role": "user", "content": "hi"}},
		"instructions":         "",
		"stream":               true,
		"include":              []any{"reasoning.encrypted_content"},
		"tools":                []any{},
		"reasoning":            map[string]any{"effort": "high", "summary": "auto"},
		"max_output_tokens":    128000,
		"prompt_cache_key":     "cursor-byok",
		"service_tier":         "fast",
		"previous_response_id": "resp_prev",
	}
	req := StreamRequest{CredentialSource: "codex"}
	filterCodexResponsesBody(body, req, "https://chatgpt.com/backend-api/codex/responses")
	if body["store"] != false {
		t.Fatalf("store = %#v, want false", body["store"])
	}
	if _, ok := body["max_output_tokens"]; ok {
		t.Fatal("max_output_tokens should be dropped")
	}
	if _, ok := body["prompt_cache_key"]; ok {
		t.Fatal("prompt_cache_key should be dropped")
	}
	if _, ok := body["service_tier"]; ok {
		t.Fatal("service_tier should be dropped")
	}
	if _, ok := body["previous_response_id"]; ok {
		t.Fatal("managed Codex previous_response_id should be dropped")
	}
	for _, key := range []string{"model", "input", "instructions", "stream", "include", "tools", "reasoning", "store"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("missing allowed key %s", key)
		}
	}

	platform := map[string]any{
		"model":                "gpt-5.4",
		"max_output_tokens":    4096,
		"prompt_cache_key":     "cursor-byok",
		"previous_response_id": "resp_static",
	}
	filterCodexResponsesBody(platform, StreamRequest{CredentialSource: "static"}, "https://api.openai.com/v1/responses")
	if platform["max_output_tokens"] != 4096 {
		t.Fatalf("static openai body changed: %#v", platform)
	}
	if platform["previous_response_id"] != "resp_static" {
		t.Fatalf("static openai previous_response_id changed: %#v", platform)
	}
	if _, ok := platform["store"]; ok {
		t.Fatalf("static openai body should not gain store: %#v", platform)
	}
}

func TestManagedCodexReservedHeadersOverrideCustomValues(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	streamReq := StreamRequest{
		CredentialSource: "codex",
		ChatGPTAccountID: "account-real",
		CodexAffinity: CodexAffinity{
			SessionID:       "session-real",
			ThreadID:        "thread-real",
			ClientRequestID: "request-real",
		},
		CustomHeadersEnabled: true,
		CustomHeadersJSON:    `{"Authorization":"Bearer attacker","originator":"attacker","ChatGPT-Account-Id":"account-attacker","session-id":"session-attacker","thread-id":"thread-attacker","x-client-request-id":"request-attacker"}`,
	}
	if err := ApplyCustomHeaders(req, streamReq.CustomHeadersEnabled, streamReq.CustomHeadersJSON); err != nil {
		t.Fatal(err)
	}
	applyManagedCodexReservedHeaders(req, streamReq, req.URL.String(), "token-real")
	if req.Header.Get("Authorization") != "Bearer token-real" || req.Header.Get("originator") != "codex_cli_rs" || req.Header.Get("ChatGPT-Account-Id") != "account-real" {
		t.Fatalf("managed identity headers were overridden: %#v", req.Header)
	}
	if req.Header.Get("session-id") != "session-real" || req.Header.Get("thread-id") != "thread-real" || req.Header.Get("x-client-request-id") != "request-real" {
		t.Fatalf("managed affinity headers were overridden: %#v", req.Header)
	}
}

func TestOpenAIManagedCodexResponsesHeadersAndWhitelist(t *testing.T) {
	var (
		gotUA, gotOriginator, gotAccountID, gotAuthorization string
		gotSessionID, gotThreadID, gotClientRequestID        string
		requestBody                                          map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotUA = request.Header.Get("User-Agent")
		gotOriginator = request.Header.Get("originator")
		gotAccountID = request.Header.Get("ChatGPT-Account-Id")
		gotAuthorization = request.Header.Get("Authorization")
		gotSessionID = request.Header.Get("session-id")
		gotThreadID = request.Header.Get("thread-id")
		gotClientRequestID = request.Header.Get("x-client-request-id")
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"model\":\"gpt-5.6\",\"status\":\"completed\",\"output_text\":\"done\"}}\n\n")
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	adapter := &OpenAIAdapter{client: chatgptLoopbackClient(t, server)}
	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:        "request-1",
		RunID:            "run-1",
		ModelCallID:      "model-call-1",
		BaseURL:          "https://chatgpt.com/backend-api/codex/responses",
		APIKey:           "codex-token",
		CredentialSource: "codex",
		ChatGPTAccountID: "acct-9",
		CodexAffinity: CodexAffinity{
			PromptCacheKey:  "derived-prompt-key",
			SessionID:       "derived-session",
			ThreadID:        "derived-thread",
			ClientRequestID: "derived-request",
		},
		ProviderModelID:          "gpt-5.6",
		OpenAIEndpoint:           "/v1/responses",
		OpenAIExtraParamsEnabled: true,
		OpenAIExtraParamsJSON:    `{"max_output_tokens":128000,"prompt_cache_key":"cursor-byok","service_tier":"fast"}`,
		Messages:                 []Message{{Role: "user", Content: "hello"}},
		MaxTokens:                128,
	}, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if gotOriginator != "codex_cli_rs" {
		t.Fatalf("originator = %q", gotOriginator)
	}
	if gotAccountID != "acct-9" {
		t.Fatalf("ChatGPT-Account-Id = %q", gotAccountID)
	}
	if gotAuthorization != "Bearer codex-token" {
		t.Fatalf("Authorization = %q", gotAuthorization)
	}
	if gotSessionID != "derived-session" || gotThreadID != "derived-thread" || gotClientRequestID != "derived-request" {
		t.Fatalf("affinity headers = (%q, %q, %q)", gotSessionID, gotThreadID, gotClientRequestID)
	}
	if gotUA == ClaudeCodeUserAgent {
		t.Fatal("managed Codex Responses must not send Claude UA")
	}
	if requestBody["store"] != false {
		t.Fatalf("store = %#v, want false", requestBody["store"])
	}
	if requestBody["prompt_cache_key"] != "derived-prompt-key" {
		t.Fatalf("prompt_cache_key = %#v", requestBody["prompt_cache_key"])
	}
	for _, key := range []string{"max_output_tokens", "service_tier"} {
		if _, ok := requestBody[key]; ok {
			t.Fatalf("%s should be stripped from Codex Responses: %#v", key, requestBody[key])
		}
	}
}

func TestOpenAIStaticResponsesKeepsClaudeUAAndExtraParams(t *testing.T) {
	var (
		gotUA, gotOriginator, gotAccountID string
		requestBody                        map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotUA = request.Header.Get("User-Agent")
		gotOriginator = request.Header.Get("originator")
		gotAccountID = request.Header.Get("ChatGPT-Account-Id")
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"model\":\"gpt-5.6\",\"status\":\"completed\",\"output_text\":\"done\"}}\n\n")
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	adapter := &OpenAIAdapter{client: server.Client()}
	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:                "request-1",
		RunID:                    "run-1",
		ModelCallID:              "model-call-1",
		BaseURL:                  server.URL,
		APIKey:                   "static-key",
		CredentialSource:         "static",
		ProviderModelID:          "gpt-5.6",
		OpenAIEndpoint:           "/v1/responses",
		OpenAIExtraParamsEnabled: true,
		OpenAIExtraParamsJSON:    `{"service_tier":"fast"}`,
		Messages:                 []Message{{Role: "user", Content: "hello"}},
		MaxTokens:                128,
	}, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if gotUA != ClaudeCodeUserAgent {
		t.Fatalf("User-Agent = %q, want Claude UA", gotUA)
	}
	if gotOriginator != "" {
		t.Fatalf("static path must not send originator, got %q", gotOriginator)
	}
	if gotAccountID != "" {
		t.Fatalf("static path must not send ChatGPT-Account-Id, got %q", gotAccountID)
	}
	if requestBody["service_tier"] != "fast" {
		t.Fatalf("static extra params dropped: %#v", requestBody)
	}
}

func runOpenAIResponsesFixture(t *testing.T, body string) (error, StreamDiagnosticsSnapshot) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, body)
	}))
	defer server.Close()
	diagnostics := &StreamDiagnostics{}
	adapter := &OpenAIAdapter{client: server.Client(), retry: instantRetry()}
	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:         "fixture-request",
		RunID:             "fixture-run",
		ModelCallID:       "fixture-call",
		BaseURL:           server.URL,
		APIKey:            "test-key",
		ProviderModelID:   "gpt-test",
		OpenAIEndpoint:    "/v1/responses",
		Messages:          []Message{{Role: "user", Content: "hello"}},
		MaxTokens:         128,
		StreamDiagnostics: diagnostics,
	}, func(ModelEvent) error { return nil })
	return err, diagnostics.Snapshot()
}

func TestOpenAIResponsesCompletedEventWithoutDoneSucceeds(t *testing.T) {
	err, snapshot := runOpenAIResponsesFixture(t, "data: {\"type\":\"response.completed\",\"sequence_number\":9,\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"output_text\":\"done\"}}\n\n")
	if err != nil {
		t.Fatalf("completed event followed by EOF failed: %v", err)
	}
	if snapshot.LastSSEEventType != "response.completed" || snapshot.LastSSESequence != 9 || snapshot.LastResponseStatus != "completed" {
		t.Fatalf("terminal diagnostics = %#v", snapshot)
	}
}

func TestOpenAIResponsesCRLFAndMultilineData(t *testing.T) {
	body := "data: {\"type\":\"response.completed\",\r\ndata: \"response\":{\"id\":\"resp-2\",\"status\":\"completed\",\"output_text\":\"done\"}}\r\n\r\n"
	if err, snapshot := runOpenAIResponsesFixture(t, body); err != nil {
		t.Fatalf("CRLF multiline event failed: %v diagnostics=%#v", err, snapshot)
	}
}

func TestOpenAIResponsesExplicitTerminalStatusesAreNotTransportErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		event  string
		status string
	}{
		{name: "failed", event: `{"type":"response.failed","response":{"id":"resp-f","status":"failed","error":{"message":"upstream rejected"}}}`, status: "failed"},
		{name: "cancelled", event: `{"type":"response.cancelled","response":{"id":"resp-c","status":"cancelled"}}`, status: "cancelled"},
		{name: "incomplete", event: `{"type":"response.incomplete","response":{"id":"resp-i","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`, status: "incomplete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err, snapshot := runOpenAIResponsesFixture(t, "data: "+test.event+"\n\n")
			var terminal *ProviderTerminalStatusError
			if !errors.As(err, &terminal) || terminal.Status != test.status {
				t.Fatalf("err=%v terminal=%#v", err, terminal)
			}
			if ClassifyProviderError(err) != ProviderErrorTerminal || snapshot.CloseCause != StreamCloseCauseProviderTerminal {
				t.Fatalf("classification=%q diagnostics=%#v", ClassifyProviderError(err), snapshot)
			}
		})
	}
}

func TestOpenAIResponsesUnknownTerminalLikeEventStillTruncates(t *testing.T) {
	err, snapshot := runOpenAIResponsesFixture(t, "data: {\"type\":\"response.future_terminal\",\"response\":{\"id\":\"resp-u\",\"status\":\"completed\"}}\n\n")
	var truncated *StreamTruncatedError
	if !errors.As(err, &truncated) {
		t.Fatalf("err=%v, want truncated", err)
	}
	if snapshot.LastSSEEventType != "response.future_terminal" || snapshot.LastResponseStatus != "completed" {
		t.Fatalf("diagnostics=%#v", snapshot)
	}
}

func TestOpenAIStreamRejectsOversizedEncodedRequestBody(t *testing.T) {
	hits := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		hits++
		return nil, errors.New("http must not be attempted")
	})}
	override := map[string]any{"pad": strings.Repeat("x", maxEncodedRequestBodyBytes)}
	for _, endpoint := range []string{"/v1/chat/completions", "/v1/responses"} {
		hits = 0
		adapter := &OpenAIAdapter{client: client, retry: instantRetry()}
		safety := &FallbackSafetyInfo{}
		err := adapter.Stream(context.Background(), StreamRequest{
			RequestID:           "request-1",
			RunID:               "run-1",
			ModelCallID:         "model-call-1",
			BaseURL:             "https://example.test",
			APIKey:              "test-key",
			ProviderModelID:     "gpt-test",
			OpenAIEndpoint:      endpoint,
			RequestBodyOverride: override,
			MaxTokens:           128,
			FallbackSafety:      safety,
		}, func(ModelEvent) error { return nil })
		assertRequestBuildLimitError(t, err, hits)
		if !safety.Snapshot().RequestBuildFailed {
			t.Fatal("MarkRequestBuildFailed was not called")
		}
	}
}
