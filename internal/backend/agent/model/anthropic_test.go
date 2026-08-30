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

func TestAnthropicMessageCacheBreakpointsPreserveAppendOnlyHistory(t *testing.T) {
	for size := 1; size <= 32; size++ {
		t.Run(fmt.Sprintf("size_%02d", size), func(t *testing.T) {
			previous := anthropicMessagesForAppendOnlyTest(size)
			current := anthropicMessagesForAppendOnlyTest(size + 1)

			applyAnthropicMessageCacheBreakpoints(previous)
			applyAnthropicMessageCacheBreakpoints(current)

			want := mustMarshalAnthropicMessagesForTest(t, previous)
			got := mustMarshalAnthropicMessagesForTest(t, current[:len(previous)])
			if got != want {
				t.Fatalf("historical message prefix changed after append\nwant: %s\ngot:  %s", want, got)
			}
		})
	}
}

func anthropicMessagesForAppendOnlyTest(count int) []anthropicMessage {
	messages := make([]anthropicMessage, 0, count)
	for index := 0; index < count; index++ {
		messages = append(messages, anthropicMessage{
			Role: "user",
			Content: []map[string]any{{
				"type": "text",
				"text": fmt.Sprintf("message-%02d", index),
			}},
		})
	}
	return messages
}

func mustMarshalAnthropicMessagesForTest(t *testing.T, messages []anthropicMessage) string {
	t.Helper()
	payload, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("marshal anthropic messages: %v", err)
	}
	return string(payload)
}

func TestAnthropicEOFWithoutMessageStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "event: content_block_delta\n")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
	}))
	defer server.Close()

	events, err := collectAnthropicStreamEvents(t, server)
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
	if !strings.Contains(err.Error(), "missing completion marker") {
		t.Fatalf("error = %q, want missing completion marker", err)
	}
	assertAnthropicEventKindCount(t, events, ModelEventKindTextDelta, 1)
	assertAnthropicEventKindCount(t, events, ModelEventKindTurnFinished, 0)
}

func TestAnthropicNormalCompletionStillSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "event: content_block_delta\n")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n")
		_, _ = fmt.Fprint(writer, "event: message_stop\n")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	events, err := collectAnthropicStreamEvents(t, server)
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	assertAnthropicEventKindCount(t, events, ModelEventKindTextDelta, 1)
	assertAnthropicEventKindCount(t, events, ModelEventKindTurnFinished, 1)
}

func TestAnthropicCRLFMultilineAndTerminalAtEOF(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "event: message_stop\r\n")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\r\n")
		_, _ = fmt.Fprint(writer, "data: \"message_stop\"}")
	}))
	defer server.Close()

	diagnostics := &StreamDiagnostics{}
	adapter := &AnthropicAdapter{client: server.Client(), retry: instantRetry()}
	events := make([]ModelEvent, 0, 1)
	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID: "request-1", RunID: "run-1", ModelCallID: "model-call-1", BaseURL: server.URL,
		APIKey: "test-key", ProviderModelID: "claude-test", Messages: []Message{{Role: "user", Content: "hello"}},
		MaxTokens: 128, StreamDiagnostics: diagnostics,
	}, func(event ModelEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("CRLF multiline terminal-at-EOF failed: %v", err)
	}
	assertAnthropicEventKindCount(t, events, ModelEventKindTurnFinished, 1)
	snapshot := diagnostics.Snapshot()
	if snapshot.LastSSEEventType != "message_stop" || snapshot.LastResponseStatus != "completed" {
		t.Fatalf("terminal diagnostics = %#v", snapshot)
	}
}

func TestAnthropicExplicitErrorIsProviderTerminalAndDoesNotRetry(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "event: error\ndata: {\"type\":\"error\",\"request_id\":\"req-secret\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n")
	}))
	defer server.Close()

	diagnostics := &StreamDiagnostics{}
	adapter := &AnthropicAdapter{client: server.Client(), retry: instantRetry()}
	req := anthropicTestRequest(server.URL)
	req.StreamDiagnostics = diagnostics
	err := adapter.Stream(context.Background(), req, func(ModelEvent) error { return nil })
	var terminal *ProviderTerminalStatusError
	if !errors.As(err, &terminal) || terminal.Status != "failed" || hits != 1 {
		t.Fatalf("err=%v terminal=%#v hits=%d", err, terminal, hits)
	}
	snapshot := diagnostics.Snapshot()
	if ClassifyProviderError(err) != ProviderErrorTerminal || snapshot.CloseCause != StreamCloseCauseProviderTerminal || snapshot.LastResponseStatus != "failed" {
		t.Fatalf("classification=%q diagnostics=%#v", ClassifyProviderError(err), snapshot)
	}
	if snapshot.LastSSEEventIDHash == "" || strings.Contains(snapshot.LastSSEEventIDHash, "req-secret") {
		t.Fatalf("request ID was not safely hashed: %#v", snapshot)
	}
}

func collectAnthropicStreamEvents(t *testing.T, server *httptest.Server) ([]ModelEvent, error) {
	t.Helper()
	adapter := &AnthropicAdapter{client: server.Client()}
	events := make([]ModelEvent, 0, 8)
	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:       "request-1",
		RunID:           "run-1",
		ModelCallID:     "model-call-1",
		BaseURL:         server.URL,
		APIKey:          "test-key",
		ProviderModelID: "claude-test",
		Messages:        []Message{{Role: "user", Content: "hello"}},
		MaxTokens:       128,
	}, func(event ModelEvent) error {
		events = append(events, event)
		return nil
	})
	return events, err
}

func assertAnthropicEventKindCount(t *testing.T, events []ModelEvent, kind ModelEventKind, want int) {
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

func TestAnthropicPreEventEOFRetriesAndPostEventDoesNot(t *testing.T) {
	t.Run("retries before first event", func(t *testing.T) {
		hits := 0
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			hits++
			writer.Header().Set("Content-Type", "text/event-stream")
			if hits == 2 {
				_, _ = fmt.Fprint(writer, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			}
		}))
		defer server.Close()
		adapter := &AnthropicAdapter{client: server.Client(), retry: instantRetry()}
		err := adapter.Stream(context.Background(), anthropicTestRequest(server.URL), func(ModelEvent) error { return nil })
		if err != nil || hits != 2 {
			t.Fatalf("err=%v hits=%d, want nil/2", err, hits)
		}
	})
	t.Run("does not retry after partial tool", func(t *testing.T) {
		hits := 0
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			hits++
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(writer, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool-1\",\"name\":\"x\"}}\n\n")
		}))
		defer server.Close()
		adapter := &AnthropicAdapter{client: server.Client(), retry: instantRetry()}
		err := adapter.Stream(context.Background(), anthropicTestRequest(server.URL), func(ModelEvent) error { return nil })
		if err == nil || hits != 1 {
			t.Fatalf("err=%v hits=%d, want error/1", err, hits)
		}
	})
}

func TestAnthropicPreEventContextCanceledDoesNotRetry(t *testing.T) {
	hits := 0
	ctx, cancel := context.WithCancel(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { hits++; cancel() }))
	defer server.Close()
	adapter := &AnthropicAdapter{client: server.Client(), retry: instantRetry()}
	err := adapter.Stream(ctx, anthropicTestRequest(server.URL), func(ModelEvent) error { return nil })
	if !errors.Is(err, context.Canceled) || hits != 1 {
		t.Fatalf("err=%v hits=%d, want canceled/1", err, hits)
	}
}

func anthropicTestRequest(baseURL string) StreamRequest {
	return StreamRequest{RequestID: "request-1", RunID: "run-1", ModelCallID: "model-call-1", BaseURL: baseURL, APIKey: "test-key", ProviderModelID: "claude-test", Messages: []Message{{Role: "user", Content: "hello"}}, MaxTokens: 128}
}
