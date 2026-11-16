package modeladapter

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSummarizeRequestCompositionCountsImagesAndToolResults(t *testing.T) {
	imageURL := "data:image/png;base64," + strings.Repeat("A", 400)
	body := map[string]any{
		"instructions": "system prompt",
		"tools": []any{
			map[string]any{"type": "function", "name": "read_file"},
		},
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "hello"},
					map[string]any{"type": "input_image", "image_url": imageURL},
				},
			},
			map[string]any{"type": "function_call_output", "call_id": "call-1", "output": "tool result"},
		},
	}

	got := summarizeRequestComposition(body, 12345)
	for _, want := range []string{
		"total_bytes=12345",
		"message_count=2",
		"image_count=1",
		"image_bytes=422",
		"instructions_bytes=15",
		"tool_result_bytes=",
		"largest_message_bytes=",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("composition missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "hello") || strings.Contains(got, imageURL) {
		t.Fatalf("composition leaked body content: %s", got)
	}
}

func TestOpenAIRequestCompositionReachesSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"output_text\":\"done\"}}\n\n")
	}))
	defer server.Close()
	observer := &codexSummaryObserver{}
	adapter := &OpenAIAdapter{client: server.Client(), retry: instantRetry()}
	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:       "request-1",
		RunID:           "run-1",
		ModelCallID:     "model-call-1",
		BaseURL:         server.URL,
		APIKey:          "test-key",
		ProviderModelID: "gpt-test",
		OpenAIEndpoint:  "/v1/responses",
		Messages:        []Message{{Role: "user", Content: "hello"}},
		MaxTokens:       128,
		Observer:        observer,
	}, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	composition, _ := observer.summary["request_composition"].(string)
	if !strings.Contains(composition, "total_bytes=") || !strings.Contains(composition, "message_count=1") {
		t.Fatalf("summary composition = %q payload=%#v", composition, observer.summary)
	}
}

func TestSummarizeRequestCompositionCountsImagesInsideToolOutput(t *testing.T) {
	imageURL := "data:image/png;base64," + strings.Repeat("A", 400)
	body := map[string]any{
		"input": []any{
			map[string]any{
				"type":    "function_call_output",
				"call_id": "call-1",
				"output": []any{
					map[string]any{"type": "input_text", "text": "see attached image"},
					map[string]any{"type": "input_image", "image_url": imageURL},
				},
			},
		},
	}

	got := summarizeRequestComposition(body, 999)
	if !strings.Contains(got, "image_count=1") || !strings.Contains(got, "image_bytes=422") {
		t.Fatalf("tool output images not counted: %s", got)
	}
	if !strings.Contains(got, "tool_result_bytes=") {
		t.Fatalf("tool result bytes missing: %s", got)
	}
	if strings.Contains(got, imageURL) || strings.Contains(got, "see attached image") {
		t.Fatalf("composition leaked tool output content: %s", got)
	}
}

func TestSummarizeRequestCompositionCountsImagesInsideTypedToolOutput(t *testing.T) {
	imageURL := "data:image/jpeg;base64," + strings.Repeat("B", 100)
	body := map[string]any{
		"input": []any{
			map[string]any{
				"type":    "function_call_output",
				"call_id": "call-2",
				"output": []map[string]any{
					map[string]any{"type": "input_image", "image_url": imageURL},
				},
			},
		},
	}

	got := summarizeRequestComposition(body, 500)
	if !strings.Contains(got, "image_count=1") || !strings.Contains(got, "image_bytes=123") {
		t.Fatalf("typed tool output images not counted: %s", got)
	}
}

func TestOpenAIRequestArtifactKeepsImageRetention(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"output_text\":\"done\"}}\n\n")
	}))
	defer server.Close()
	observer := &privacyArtifactObserver{}
	adapter := &OpenAIAdapter{client: server.Client(), retry: instantRetry()}
	const retention = "deduped=1;omitted=2;omitted_bytes=2048;retained_image_bytes=4096;budget_bytes=7864320;estimated_bytes=6000"
	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:       "request-1",
		RunID:           "run-1",
		ModelCallID:     "model-call-1",
		BaseURL:         server.URL,
		APIKey:          "test-key",
		ProviderModelID: "gpt-test",
		OpenAIEndpoint:  "/v1/responses",
		Messages:        []Message{{Role: "user", Content: "hello"}},
		MaxTokens:       128,
		ImageRetention:  retention,
		Observer:        observer,
	}, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if got, _ := observer.request["image_retention"].(string); got != retention {
		t.Fatalf("image_retention = %q payload=%#v", got, observer.request)
	}
}

func TestSummarizeRequestCompositionAcceptsTypedMessageSlice(t *testing.T) {
	body := map[string]any{
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	}
	got := summarizeRequestComposition(body, 10)
	if !strings.Contains(got, "message_count=1") || !strings.Contains(got, "total_bytes=10") {
		t.Fatalf("typed message slice composition = %s", got)
	}
}
