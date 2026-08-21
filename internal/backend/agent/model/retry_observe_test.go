package modeladapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cursor/internal/observability"
)

func TestProviderAttemptsWriteObservabilityJSONL(t *testing.T) {
	controller, eventsPath := newObservabilityController(t)
	correlation := observability.Correlation{
		TraceID:         "trace-shared",
		HTTPRequestID:   "http-req-1",
		CursorRequestID: "cursor-req-1",
		TurnID:          "conversation-1:3",
		TurnSequence:    3,
		ModelCallID:     "model-call-1",
	}
	ctx := observability.WithCorrelation(context.Background(), correlation)

	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("connection refused")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok-body")),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})}
	response, err := doProviderRequest(
		ctx,
		client,
		"openai",
		"cursor-req-1",
		"model-call-1",
		func(requestContext context.Context) (*http.Request, error) {
			return http.NewRequestWithContext(requestContext, http.MethodPost, "https://example.test/v1/responses", bytes.NewReader([]byte(`{"prompt":"do-not-record"}`)))
		},
		nil,
		instantRetry(),
	)
	if err != nil {
		t.Fatalf("retry after transport error: %v", err)
	}
	_ = response.Body.Close()
	if err := controller.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	events := readObservabilityEvents(t, eventsPath)
	requests := filterObservabilityEvents(events, "provider_request")
	responses := filterObservabilityEvents(events, "provider_response")
	decisions := filterObservabilityEvents(events, "retry_decision")
	if len(requests) != 2 || len(responses) != 2 || len(decisions) != 2 {
		t.Fatalf("attempt events request=%d response=%d retry_decision=%d, want 2/2/2 in %v", len(requests), len(responses), len(decisions), eventNames(events))
	}
	if got := observabilityFieldString(responses[0], "retry_decision"); got != retryDecisionRetry {
		t.Fatalf("first retry_decision = %q", got)
	}
	if got := observabilityFieldString(responses[1], "retry_decision"); got != retryDecisionSuccessAfterRetry {
		t.Fatalf("final retry_decision = %q", got)
	}
	if observabilityFieldInt(responses[0], "attempt") != 1 || observabilityFieldInt(responses[1], "attempt") != 2 {
		t.Fatalf("attempt numbers = %v", []int{observabilityFieldInt(responses[0], "attempt"), observabilityFieldInt(responses[1], "attempt")})
	}
	if observabilityFieldString(decisions[0], "retry_decision") != retryDecisionRetry {
		t.Fatalf("retry_decision event = %q", observabilityFieldString(decisions[0], "retry_decision"))
	}
	for _, event := range append(append(requests, responses...), decisions...) {
		if event.TraceID != correlation.TraceID || event.HTTPRequestID != correlation.HTTPRequestID || event.TurnID != correlation.TurnID || event.CursorRequestID != "cursor-req-1" || event.ModelCallID != "model-call-1" {
			t.Fatalf("correlation not shared: %+v", event)
		}
	}
	payload, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	if strings.Contains(text, "do-not-record") || strings.Contains(text, "/v1/responses") || strings.Contains(text, "https://") {
		t.Fatalf("events.jsonl leaked request content or raw URL: %s", payload)
	}
}

func TestProviderObservabilityOmitsSecretsAndDoesNotForgeIDs(t *testing.T) {
	controller, eventsPath := newObservabilityController(t)
	client := &http.Client{Transport: secretErrorTransport{}}
	_, err := doProviderRequest(
		context.Background(),
		client,
		"openai",
		"request-id",
		"model-call-id",
		func(ctx context.Context) (*http.Request, error) {
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test/v1/messages?api_key=query-secret", bytes.NewReader([]byte(`{"prompt":"do-not-record"}`)))
			if err != nil {
				return nil, err
			}
			request.Header.Set("Authorization", "Bearer header-secret")
			request.Header.Set("Cookie", "session=cookie-secret")
			return request, nil
		},
		nil,
		instantRetry(),
	)
	if err == nil {
		t.Fatal("transport error expected")
	}
	if err := controller.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	payload, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, secret := range []string{"query-secret", "header-secret", "cookie-secret", "do-not-record", "api_key=query-secret", "https://", "/v1/messages"} {
		if strings.Contains(text, secret) {
			t.Fatalf("events.jsonl leaked %q: %s", secret, text)
		}
	}
	events := readObservabilityEvents(t, eventsPath)
	if len(filterObservabilityEvents(events, "provider_request")) != providerRequestMaxAttempts {
		t.Fatalf("provider_request count = %d", len(filterObservabilityEvents(events, "provider_request")))
	}
	responses := filterObservabilityEvents(events, "provider_response")
	if len(responses) != providerRequestMaxAttempts {
		t.Fatalf("provider_response count = %d", len(responses))
	}
	if observabilityFieldString(responses[len(responses)-1], "retry_decision") != retryDecisionExhausted {
		t.Fatalf("final decision = %q", observabilityFieldString(responses[len(responses)-1], "retry_decision"))
	}
	for _, event := range events {
		if event.TraceID != "" || event.TurnID != "" || event.HTTPRequestID != "" {
			t.Fatalf("forged unavailable correlation: %+v", event)
		}
		if event.CursorRequestID != "request-id" || event.ModelCallID != "model-call-id" {
			t.Fatalf("param correlation missing: %+v", event)
		}
	}
}

func TestProviderObservabilitySuccessIsNotWarning(t *testing.T) {
	controller, eventsPath := newObservabilityController(t)
	providerStatus := http.StatusOK
	serverCalls := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		serverCalls++
		return &http.Response{
			StatusCode: providerStatus,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})}
	response, err := doProviderRequest(
		context.Background(),
		client,
		"openai",
		"request-id",
		"model-call-id",
		func(ctx context.Context) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
		},
		nil,
		instantRetry(),
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if err := controller.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	events := readObservabilityEvents(t, eventsPath)
	responses := filterObservabilityEvents(events, "provider_response")
	if len(responses) != 1 || serverCalls != 1 {
		t.Fatalf("success response count = %d calls = %d", len(responses), serverCalls)
	}
	if responses[0].Severity != observability.SeverityInfo || responses[0].Status != "completed" {
		t.Fatalf("success event severity/status = %q/%q", responses[0].Severity, responses[0].Status)
	}
	if observabilityFieldString(responses[0], "retry_decision") != retryDecisionSuccess {
		t.Fatalf("success retry_decision = %q", observabilityFieldString(responses[0], "retry_decision"))
	}
}

func newObservabilityController(t *testing.T) (*observability.Controller, string) {
	t.Helper()
	previous := observability.ProcessSink()
	t.Cleanup(func() { observability.SetProcessSink(previous) })
	controller, err := observability.NewController(t.TempDir(), observability.Settings{
		Mode:          observability.ModeBasic,
		RetentionDays: 7,
		MaxDiskMB:     64,
	})
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	status := controller.Status()
	if strings.TrimSpace(status.SessionPath) == "" {
		t.Fatal("controller missing session path")
	}
	return controller, filepath.Join(status.SessionPath, "events.jsonl")
}

func readObservabilityEvents(t *testing.T, path string) []observability.Event {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	var events []observability.Event
	for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event observability.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		t.Fatalf("events.jsonl empty: %s", payload)
	}
	return events
}

func filterObservabilityEvents(events []observability.Event, name string) []observability.Event {
	filtered := make([]observability.Event, 0, len(events))
	for _, event := range events {
		if event.Event == name {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func eventNames(events []observability.Event) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Event)
	}
	return names
}

func observabilityFieldString(event observability.Event, key string) string {
	if event.Fields == nil {
		return ""
	}
	value, _ := event.Fields[key]
	text, _ := value.(string)
	return text
}

func observabilityFieldInt(event observability.Event, key string) int {
	if event.Fields == nil {
		return 0
	}
	switch value := event.Fields[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}
